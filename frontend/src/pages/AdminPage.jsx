import React, { useEffect, useState, useCallback, useRef, useReducer } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Loader2,
  CheckCircle2,
  XCircle,
  Square,
  Trash2,
  Plus,
  RefreshCw,
  ChevronDown,
  AlertTriangle,
  Info,
  Activity,
  Clock3,
  Database,
  GitBranch,
  TerminalSquare,
  Zap,
} from 'lucide-react';
import {
  createTask,
  deleteTask,
  getTasks,
  getThumbStats,
  getSimilarityDistribution,
  getEmbeddingsStatus,
  updateThreshold,
  startTask,
  stopTask,
  retryTask,
} from '../api/admin';
import { useCountUp } from '../hooks/useCountUp';

// ─── Constants ───────────────────────────────────────────────────────────────

const CATEGORY_OPTIONS = [
  'Misc', 'Doujinshi', 'Manga', 'Artist CG', 'Game CG',
  'Image Set', 'Cosplay', 'Asian Porn', 'Non-H', 'Western',
];
const MIXED_CATEGORY = 'Mixed';
const DEFAULT_INCREMENTAL_CATEGORIES = ['Doujinshi', 'Manga', 'Cosplay'];

const DEFAULT_FULL = { start_gid: '' };
const DEFAULT_INCREMENTAL = {
  categories: [...DEFAULT_INCREMENTAL_CATEGORIES],
  scan_window: 10000,
  rating_diff_threshold: 0.5,
};
const DEFAULT_FAVORITES = { run_interval_hours: 6 };
const ACTIVE_JOB_STATES = ['available', 'pending', 'scheduled', 'running', 'retryable'];
const RETRYABLE_TERMINAL_STATES = ['cancelled', 'discarded', 'completed'];
const ADMIN_TABS = [
  { id: 'sync', label: 'Sync Runs' },
  { id: 'queues', label: 'Queues' },
  { id: 'recommendations', label: 'Recommendations' },
];

function getDefaultConfig(type) {
  if (type === 'favorites') return { ...DEFAULT_FAVORITES };
  return type === 'full' ? { ...DEFAULT_FULL } : { ...DEFAULT_INCREMENTAL };
}

function buildPayload(form) {
  if (form.type === 'full') {
    return {
      name: form.name.trim(),
      type: form.type,
      category: form.category,
      config: { start_gid: form.config.start_gid === '' ? null : Number(form.config.start_gid) },
    };
  }
  if (form.type === 'favorites') {
    return {
      name: form.name.trim(),
      type: 'favorites',
      category: 'Favorites',
      config: { run_interval_hours: Number(form.config.run_interval_hours || 6) },
    };
  }
  return {
    name: form.name.trim(),
    type: form.type,
    category: MIXED_CATEGORY,
    config: {
      categories: (form.config.categories || []).slice(),
      scan_window: Number(form.config.scan_window),
      rating_diff_threshold: Number(form.config.rating_diff_threshold),
    },
  };
}

function isTransitioning(task) {
  return Boolean(task.requested_action);
}

function formatTaskKind(value) {
  if (value === 'gallery_sync') return 'Gallery Sync';
  if (value === 'favorites_sync') return 'Favorites Sync';
  return value || 'Sync Task';
}

function formatTaskScope(task) {
  if (task.source === 'favorites') return 'source: favorites · scope: user_favorites';
  if (task.strategy === 'incremental') {
    const categories = Array.isArray(task.scope?.categories)
      ? task.scope.categories
      : (Array.isArray(task.config?.categories) ? task.config.categories : []);
    return categories.length
      ? `source: gallery_list · scope: ${categories.join(', ')}`
      : 'source: gallery_list · scope: mixed categories';
  }
  return `source: gallery_list · scope: ${task.scope?.category || task.category || 'category'}`;
}

function formatTaskSchedule(task) {
  if (task.schedule_kind === 'periodic') {
    const seconds = Number(task.schedule_interval_sec || 0);
    if (seconds >= 3600) return `periodic · every ${Math.round(seconds / 3600)}h`;
    if (seconds > 0) return `periodic · every ${seconds}s`;
    return 'periodic';
  }
  return 'manual';
}

function formatJobKind(value) {
  if (value === 'ehstash_incremental_sync') return 'incremental kick';
  if (value === 'ehstash_incremental_slice') return 'incremental slice';
  if (value === 'ehstash_full_sync') return 'full sync';
  if (value === 'ehstash_favorites_sync') return 'favorites sync';
  return value || 'none';
}

function formatTimestamp(value) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function formatNumber(value) {
  if (value == null || value === '') return '—';
  const n = Number(value);
  if (Number.isNaN(n)) return String(value);
  return n.toLocaleString();
}

function activeCurrentJob(task) {
  return ACTIVE_JOB_STATES.includes(task.current_job_state);
}

function getCheckpoint(task) {
  return task.checkpoint || task.state || {};
}

function getTaskMode(task) {
  if (task.source === 'favorites') return 'favorites';
  if (task.source === 'gallery_list' && task.strategy === 'incremental') return 'incremental';
  return 'full';
}

function getPrimaryState(task) {
  if (task.requested_action) return `request: ${task.requested_action}`;
  if (task.current_job_state) return task.current_job_state;
  if (task.enabled && task.schedule_kind === 'periodic') return 'waiting';
  if (task.latest_job_state) return task.latest_job_state;
  return task.enabled ? 'enabled' : 'disabled';
}

function getProgressSummary(task) {
  const checkpoint = getCheckpoint(task);
  if (getTaskMode(task) === 'incremental') {
    const scanned = Number(checkpoint.scanned_count || 0);
    const scanWindow = Number(task.config?.scan_window || 10000);
    const phase = task.current_job_kind === 'ehstash_incremental_sync'
      ? 'kick'
      : task.current_job_kind === 'ehstash_incremental_slice'
        ? 'slice'
        : checkpoint.run_id
          ? 'chain'
          : 'idle';
    return `${phase} · ${formatNumber(scanned)} / ${formatNumber(scanWindow)}`;
  }
  const pct = Number(task.progress_pct || 0);
  return `${pct < 1 ? pct.toFixed(3) : pct.toFixed(1)}%`;
}

function getProgressPercent(task) {
  const checkpoint = getCheckpoint(task);
  if (getTaskMode(task) === 'incremental') {
    const scanned = Number(checkpoint.scanned_count || 0);
    const scanWindow = Number(task.config?.scan_window || 10000);
    return Math.max(0, Math.min(100, scanWindow ? (scanned / scanWindow) * 100 : 0));
  }
  return Math.max(0, Math.min(100, Number(task.progress_pct || 0)));
}

function getTaskSubtitle(task) {
  const mode = getTaskMode(task);
  const schedule = formatTaskSchedule(task);
  if (mode === 'incremental') return `incremental · ${schedule}`;
  if (mode === 'favorites') return `favorites · ${schedule}`;
  return `${task.scope?.category || task.category || 'gallery'} · ${schedule}`;
}

// ─── Sub-components ──────────────────────────────────────────────────────────

const STATUS_CONFIG = {
  available: { text: 'text-cyan-300', ring: 'ring-cyan-500/30', bg: 'bg-cyan-500/10' },
  scheduled: { text: 'text-cyan-400', ring: 'ring-cyan-500/30', bg: 'bg-cyan-500/10' },
  running: { text: 'text-blue-400', ring: 'ring-blue-500/30', bg: 'bg-blue-500/10' },
  retryable: { text: 'text-amber-300', ring: 'ring-amber-500/30', bg: 'bg-amber-500/10' },
  completed: { text: 'text-emerald-400', ring: 'ring-emerald-500/30', bg: 'bg-emerald-500/10' },
  cancelled: { text: 'text-gray-400', ring: 'ring-gray-500/30', bg: 'bg-gray-500/10' },
  discarded: { text: 'text-rose-400', ring: 'ring-rose-500/30', bg: 'bg-rose-500/10' },
  enabled: { text: 'text-sky-300', ring: 'ring-sky-500/30', bg: 'bg-sky-500/10' },
  waiting: { text: 'text-slate-300', ring: 'ring-slate-500/30', bg: 'bg-slate-500/10' },
  disabled: { text: 'text-gray-400', ring: 'ring-gray-500/30', bg: 'bg-gray-500/10' },
};

function StatusBadge({ status }) {
  const cfg = STATUS_CONFIG[status] || STATUS_CONFIG.disabled;
  const spinning = status === 'available' || status === 'scheduled' || status === 'running' || status === 'retryable';

  return (
    <span className={`inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium ring-1 ${cfg.bg} ${cfg.text} ${cfg.ring}`}>
      {spinning ? (
        <Loader2 size={11} className="animate-spin" />
      ) : (
        <span className={`w-1.5 h-1.5 rounded-full ${status === 'discarded' ? 'bg-rose-400' : status === 'completed' ? 'bg-emerald-400' : status === 'enabled' ? 'bg-sky-400' : 'bg-gray-400'}`} aria-hidden="true" />
      )}
      {status}
    </span>
  );
}

function GradientProgressBar({ progress, dbCount, totalCount }) {
  const animatedDb = useCountUp(dbCount);
  const clampedPct = Math.max(0, Math.min(100, progress));
  const displayPct = progress < 1 ? progress.toFixed(3) : progress.toFixed(1);

  const numerator = animatedDb != null ? Number(animatedDb).toLocaleString() : '—';
  const denominator = totalCount != null ? Number(totalCount).toLocaleString() : '—';

  return (
    <div className="min-w-[180px]">
      <div className="flex items-center justify-between mb-1">
        <span className="text-xs text-gray-400 font-mono">
          {numerator} / {denominator}
        </span>
        <span className="text-xs font-semibold text-white ml-2">{displayPct}%</span>
      </div>
      <progress
        value={clampedPct}
        max={100}
        className="gradient-progress w-full"
        aria-label="progress"
      >
        {clampedPct}%
      </progress>
    </div>
  );
}

function MetaLine({ label, value, tone = 'default' }) {
  const color = tone === 'strong' ? 'text-white' : tone === 'warn' ? 'text-amber-300' : tone === 'error' ? 'text-rose-300' : 'text-gray-300';
  return (
    <div className="flex items-center justify-between gap-3 text-xs">
      <span className="text-gray-500">{label}</span>
      <span className={`font-mono text-right truncate ${color}`} title={value == null ? '' : String(value)}>
        {value ?? '—'}
      </span>
    </div>
  );
}

const RIVER_STAGES = ['available', 'scheduled', 'running', 'retryable', 'terminal'];

function RiverStateRail({ state }) {
  const terminal = ['completed', 'cancelled', 'discarded'].includes(state);
  const active = terminal ? 'terminal' : state;

  return (
    <div className="flex items-center gap-1.5" aria-label={`River job state ${state || 'none'}`}>
      {RIVER_STAGES.map((stage, index) => {
        const isActive = active === stage;
        const complete = !terminal && RIVER_STAGES.indexOf(active) > index;
        return (
          <React.Fragment key={stage}>
            <span
              title={stage}
              className={`h-2 w-2 rounded-full border ${isActive
                ? state === 'discarded'
                  ? 'bg-rose-400 border-rose-300 shadow-[0_0_0_3px_rgba(244,63,94,0.12)]'
                  : state === 'retryable'
                    ? 'bg-amber-300 border-amber-200 shadow-[0_0_0_3px_rgba(251,191,36,0.12)]'
                    : 'bg-cyan-300 border-cyan-200 shadow-[0_0_0_3px_rgba(34,211,238,0.12)]'
                : complete
                  ? 'bg-gray-500/70 border-gray-500/70'
                  : 'bg-transparent border-white/15'
                }`}
            />
            {index < RIVER_STAGES.length - 1 && (
              <span className={`h-px w-5 ${complete ? 'bg-gray-500/70' : 'bg-white/10'}`} aria-hidden="true" />
            )}
          </React.Fragment>
        );
      })}
    </div>
  );
}

function TaskHeaderStrip({ tasks }) {
  const activeJobs = tasks.filter(activeCurrentJob).length;
  const enabled = tasks.filter((task) => task.enabled).length;
  const requested = tasks.filter((task) => task.requested_action).length;
  return (
    <div className="flex items-center gap-3 rounded-lg border border-white/10 bg-zinc-950/60 px-4 py-2.5 text-xs text-gray-400">
      <span><span className="font-mono text-white">{activeJobs}</span> active</span>
      <span><span className="font-mono text-white">{enabled}</span> enabled</span>
      {requested > 0 && <span className="text-amber-300"><span className="font-mono">{requested}</span> pending</span>}
    </div>
  );
}

function DefinitionPanel({ task }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Database size={14} className={task.enabled ? 'text-emerald-400' : 'text-gray-500'} />
        <span className="text-xs uppercase tracking-wider text-gray-500">Definition</span>
      </div>
      <div>
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm font-semibold text-white">{task.name}</span>
          <span className={`text-xs font-mono ${task.enabled ? 'text-emerald-300' : 'text-gray-500'}`}>
            {task.enabled ? 'enabled' : 'disabled'}
          </span>
        </div>
        <p className="mt-1 text-xs text-gray-500">{formatTaskScope(task)}</p>
      </div>
      <div className="grid gap-1.5">
        <MetaLine label="kind" value={formatTaskKind(task.task_kind)} />
        <MetaLine label="strategy" value={task.strategy || task.type || 'sync'} />
        <MetaLine label="schedule" value={formatTaskSchedule(task)} />
        {task.requested_action && (
          <MetaLine label="request" value={task.requested_action} tone="warn" />
        )}
      </div>
    </div>
  );
}

function RiverJobPanel({ task }) {
  const state = task.current_job_state || (task.enabled && task.schedule_kind === 'periodic' ? 'waiting' : task.latest_job_state);
  const active = activeCurrentJob(task);
  const jobID = active ? task.current_job_id : task.last_job_id;
  const kind = active ? task.current_job_kind : task.latest_job_kind;
  const attempt = active ? task.current_job_attempt : task.latest_job_attempt;
  const maxAttempts = active ? task.current_job_max_attempts : task.latest_job_max_attempts;
  const attemptedAt = active ? task.current_job_attempted_at : task.latest_job_attempted_at;
  const finalizedAt = active ? task.current_job_finalized_at : task.latest_job_finalized_at;

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <TerminalSquare size={14} className={active ? 'text-cyan-300' : 'text-gray-500'} />
          <span className="text-xs uppercase tracking-wider text-gray-500">River Job</span>
        </div>
        {task.current_job_state ? <RiverStateRail state={task.current_job_state} /> : null}
      </div>
      {task.current_job_state ? (
        <StatusBadge status={task.current_job_state} />
      ) : task.enabled && task.schedule_kind === 'periodic' ? (
        <span className="inline-flex items-center gap-1.5 text-xs text-gray-400">
          <Clock3 size={12} />
          waiting for periodic kick
        </span>
      ) : (
        <StatusBadge status={state || 'disabled'} />
      )}
      <div className="grid gap-1.5">
        <MetaLine label={active ? 'current job' : 'last job'} value={jobID ? `#${jobID}` : 'none'} tone={active ? 'strong' : 'default'} />
        <MetaLine label="kind" value={formatJobKind(kind)} />
        <MetaLine label="attempt" value={attempt ? `${attempt}/${maxAttempts || '—'}` : '—'} />
        <MetaLine label={active ? 'attempted' : 'finalized'} value={formatTimestamp(active ? attemptedAt : finalizedAt)} />
      </div>
      {task.error_message && (
        <div className="rounded-md border border-rose-500/20 bg-rose-500/10 px-2.5 py-2 text-xs text-rose-300 truncate" title={task.error_message}>
          {task.error_message}
        </div>
      )}
    </div>
  );
}

function IncrementalCheckpoint({ task, checkpoint }) {
  const scanned = Number(checkpoint.scanned_count || 0);
  const scanWindow = Number(task.config?.scan_window || 10000);
  const pct = Math.max(0, Math.min(100, scanWindow ? (scanned / scanWindow) * 100 : 0));
  const activeKind = task.current_job_kind === 'ehstash_incremental_sync'
    ? 'kick'
    : task.current_job_kind === 'ehstash_incremental_slice'
      ? 'slice'
      : checkpoint.run_id
        ? 'chain'
        : 'idle';

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <GitBranch size={14} className={checkpoint.run_id ? 'text-cyan-300' : 'text-gray-500'} />
          <span className="text-xs uppercase tracking-wider text-gray-500">Checkpoint</span>
        </div>
        <span className="text-xs font-mono text-gray-400">{activeKind}</span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/10">
        <div className="h-full bg-cyan-400/80 transition-all duration-700" style={{ width: `${pct}%` }} />
      </div>
      <div className="grid gap-1.5">
        <MetaLine label="round" value={formatNumber(checkpoint.round || 0)} />
        <MetaLine label="run_id" value={checkpoint.run_id || 'none'} tone={checkpoint.run_id ? 'strong' : 'default'} />
        <MetaLine label="window" value={`${formatNumber(scanned)} / ${formatNumber(scanWindow)}`} />
        <MetaLine label="next_gid" value={formatNumber(checkpoint.next_gid)} />
        <MetaLine label="latest_gid" value={formatNumber(checkpoint.latest_gid)} />
      </div>
    </div>
  );
}

function GenericCheckpoint({ task, checkpoint }) {
  const mode = getTaskMode(task);
  const progress = Number(task.progress_pct || 0);
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Activity size={14} className="text-gray-400" />
        <span className="text-xs uppercase tracking-wider text-gray-500">Checkpoint</span>
      </div>
      <GradientProgressBar
        progress={progress}
        dbCount={mode === 'favorites' ? null : (checkpoint.db_count ?? null)}
        totalCount={mode === 'favorites' ? null : (checkpoint.total_count ?? null)}
      />
      <div className="grid gap-1.5">
        <MetaLine label="round" value={formatNumber(checkpoint.round || 0)} />
        <MetaLine label="next_gid" value={formatNumber(checkpoint.next_gid)} />
        {checkpoint.done != null && <MetaLine label="done" value={String(Boolean(checkpoint.done))} />}
        {checkpoint.anchor_gid != null && <MetaLine label="anchor_gid" value={formatNumber(checkpoint.anchor_gid)} />}
      </div>
    </div>
  );
}

function CheckpointPanel({ task }) {
  const checkpoint = getCheckpoint(task);
  if (getTaskMode(task) === 'incremental') {
    return <IncrementalCheckpoint task={task} checkpoint={checkpoint} />;
  }
  return <GenericCheckpoint task={task} checkpoint={checkpoint} />;
}

function SyncTaskRunRow({ task, rowAction, runTaskAction, setDeleteTarget, expanded, onToggle }) {
  const transition = isTransitioning(task);
  const rowBusy = Boolean(rowAction);
  const currentJobActive = activeCurrentJob(task);
  const canStart = !rowBusy && !transition && !task.enabled && !currentJobActive;
  const canStop = !rowBusy && !transition && (task.enabled || currentJobActive);
  const canRetry = !rowBusy && !transition && !currentJobActive && RETRYABLE_TERMINAL_STATES.includes(task.latest_job_state);
  const canDelete = !rowBusy && !transition && !task.enabled && !currentJobActive;
  const isIncremental = getTaskMode(task) === 'incremental';
  const primaryState = getPrimaryState(task);
  const progress = getProgressPercent(task);
  const attention = task.error_message || task.current_job_state === 'retryable' || task.latest_job_state === 'discarded' || transition;

  return (
    <div className={`rounded-lg border bg-zinc-950/45 transition-colors ${attention ? 'border-amber-500/30' : 'border-white/10 hover:border-white/20'}`}>
      <div className="grid items-center gap-3 px-4 py-3 lg:grid-cols-[minmax(0,1fr)_210px_150px_auto]">
        <div className="min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <span className={`h-2 w-2 rounded-full shrink-0 ${activeCurrentJob(task)
              ? 'bg-cyan-300'
              : task.enabled
                ? 'bg-emerald-400'
                : 'bg-gray-600'
              }`}
            />
            <span className="truncate text-sm font-semibold text-white">{task.name}</span>
            {attention && <AlertTriangle size={13} className="shrink-0 text-amber-300" />}
          </div>
          <p className="mt-0.5 truncate text-xs text-gray-500">{getTaskSubtitle(task)}</p>
        </div>

        <div className="flex items-center gap-2 min-w-0">
          <StatusBadge status={primaryState.startsWith('request:') ? 'retryable' : primaryState} />
          <span className="truncate text-xs font-mono text-gray-500">
            {task.current_job_kind ? formatJobKind(task.current_job_kind) : task.enabled && task.schedule_kind === 'periodic' ? 'next kick' : formatJobKind(task.latest_job_kind)}
          </span>
        </div>

        <div className="min-w-0">
          <div className="mb-1 flex items-center justify-between gap-2">
            <span className="truncate text-xs font-mono text-gray-300">{getProgressSummary(task)}</span>
          </div>
          <div className="h-1 w-full overflow-hidden rounded-full bg-white/10">
            <div className="h-full bg-cyan-400/75 transition-all duration-700" style={{ width: `${progress}%` }} />
          </div>
        </div>

        <div className="flex items-center justify-end gap-1">
          <button
            type="button"
            title={isIncremental ? '启用并投递 kick' : '启动任务'}
            aria-label={`启动任务 ${task.name}`}
            disabled={!canStart}
            onClick={() => runTaskAction(task.id, 'start', () => startTask(task.id))}
            className="inline-flex items-center justify-center gap-2 rounded-md border border-emerald-500/20 px-3 py-2 text-xs text-emerald-300 hover:bg-emerald-500/10 transition-all disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
          >
            {rowAction === 'start' ? <Loader2 size={14} className="animate-spin" /> : <Zap size={14} />}
          </button>
          <button
            type="button"
            title="取消当前 job 或停用定义"
            aria-label={`停止任务 ${task.name}`}
            disabled={!canStop}
            onClick={() => runTaskAction(task.id, 'stop', () => stopTask(task.id))}
            className="inline-flex items-center justify-center gap-2 rounded-md border border-amber-500/20 px-3 py-2 text-xs text-amber-300 hover:bg-amber-500/10 transition-all disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
          >
            {rowAction === 'stop' ? <Loader2 size={14} className="animate-spin" /> : <Square size={14} />}
          </button>
          <button
            type="button"
            title="重试最近 job"
            aria-label={`重试任务 ${task.name}`}
            disabled={!canRetry}
            onClick={() => runTaskAction(task.id, 'retry', () => retryTask(task.id))}
            className="inline-flex items-center justify-center gap-2 rounded-md border border-cyan-500/20 px-3 py-2 text-xs text-cyan-300 hover:bg-cyan-500/10 transition-all disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
          >
            {rowAction === 'retry' ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
          </button>
          <button
            type="button"
            title="删除任务定义"
            aria-label={`删除任务 ${task.name}`}
            disabled={!canDelete}
            onClick={() => setDeleteTarget(task)}
            className="inline-flex items-center justify-center gap-2 rounded-md border border-rose-500/20 px-3 py-2 text-xs text-rose-300 hover:bg-rose-500/10 transition-all disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
          >
            {rowAction === 'delete' ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
          </button>
          <button
            type="button"
            title={expanded ? '收起详情' : '展开详情'}
            aria-label={`${expanded ? '收起' : '展开'}任务 ${task.name} 详情`}
            onClick={onToggle}
            className="inline-flex items-center justify-center rounded-md border border-white/10 px-2.5 py-2 text-xs text-gray-400 hover:bg-white/5 hover:text-white transition-all"
          >
            <ChevronDown size={14} className={`transition-transform ${expanded ? 'rotate-180' : ''}`} />
          </button>
        </div>
      </div>
      {transition && (
        <div className="mx-4 mb-3 rounded-md border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
          manager request pending: <span className="font-mono">{task.requested_action}</span>
        </div>
      )}
      {task.error_message && !expanded && (
        <div className="mx-4 mb-3 truncate rounded-md border border-rose-500/20 bg-rose-500/10 px-3 py-2 text-xs text-rose-300" title={task.error_message}>
          {task.error_message}
        </div>
      )}
      {expanded && (
        <div className="grid gap-5 border-t border-white/10 px-4 py-4 lg:grid-cols-3">
          <DefinitionPanel task={task} />
          <RiverJobPanel task={task} />
          <CheckpointPanel task={task} />
        </div>
      )}
    </div>
  );
}

function QueueStage({ icon: Icon, label, value, color, infoTitle }) {
  const animated = useCountUp(value ?? 0);
  return (
    <div className="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2.5">
      <div className="flex items-center gap-2 text-xs text-gray-400 uppercase tracking-wider">
        <Icon size={13} className={color} />
        <span>{label}</span>
        {infoTitle && (
          <Info
            size={12}
            className="text-gray-500"
            title={infoTitle}
            aria-label={infoTitle}
          />
        )}
      </div>
      <p className={`mt-1 text-2xl font-bold tabular-nums ${color}`}>
        {animated?.toLocaleString() ?? '—'}
      </p>
    </div>
  );
}

function InputField({ label, type = 'text', value, onChange, placeholder, step, id }) {
  return (
    <div>
      <label htmlFor={id} className="block text-xs font-medium text-gray-400 mb-1.5">{label}</label>
      <input
        id={id}
        type={type}
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        step={step}
        className="w-full px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-white text-sm
                   placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-blue-500/50
                   focus:border-blue-500/50 transition-all"
      />
    </div>
  );
}

function SelectField({ label, value, onChange, options, id }) {
  return (
    <div>
      <label htmlFor={id} className="block text-xs font-medium text-gray-400 mb-1.5">{label}</label>
      <div className="relative">
        <select
          id={id}
          value={value}
          onChange={onChange}
          className="w-full appearance-none px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-white text-sm
                     focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500/50
                     transition-all cursor-pointer"
        >
          {options.map((opt) => (
            <option key={opt.value ?? opt} value={opt.value ?? opt} className="bg-zinc-900">
              {opt.label ?? opt}
            </option>
          ))}
        </select>
        <ChevronDown size={14} className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 pointer-events-none" />
      </div>
    </div>
  );
}

// ─── Create Task Modal ────────────────────────────────────────────────────────

function CreateTaskModal({ onClose, onCreated, tasks }) {
  const [busy, setBusy] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');
  const [form, setForm] = useState({
    name: '', type: 'full', category: 'Cosplay', config: getDefaultConfig('full'),
  });
  const hasIncrementalTask = (tasks || []).some((task) => (
    task.source === 'gallery_list' && task.strategy === 'incremental'
  ) || task.type === 'incremental');
  const hasFavoritesSource = (tasks || []).some((task) => task.source === 'favorites' || task.type === 'favorites');

  const handleClose = useCallback(() => {
    if (!busy) onClose();
  }, [busy, onClose]);
  const dialogRef = useRef(null);

  // Prevent closing (Escape) while a request is in-flight
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const preventClose = (e) => { if (busy) e.preventDefault(); };
    dialog.addEventListener('cancel', preventClose);
    return () => dialog.removeEventListener('cancel', preventClose);
  }, [busy]);

  // Open the modal on mount
  useEffect(() => {
    dialogRef.current?.showModal();
  }, []);

  const handleTypeChange = (nextType) => {
    setForm((prev) => ({
      ...prev,
      type: nextType,
      category: nextType === 'full'
        ? (CATEGORY_OPTIONS.includes(prev.category) ? prev.category : 'Cosplay')
        : nextType === 'favorites'
          ? 'Favorites'
          : MIXED_CATEGORY,
      config: getDefaultConfig(nextType),
    }));
  };

  const toggleIncrementalCategory = (category) => {
    setForm((prev) => {
      const current = Array.isArray(prev.config.categories) ? prev.config.categories : [];
      const exists = current.includes(category);
      const nextCategories = exists
        ? current.filter((c) => c !== category)
        : [...current, category];
      return { ...prev, config: { ...prev.config, categories: nextCategories } };
    });
  };

  const handleSubmit = async () => {
    setErrorMsg('');
    if (!form.name.trim()) { setErrorMsg('名称不能为空'); return; }
    if (form.type === 'incremental' && hasIncrementalTask) {
      setErrorMsg('仅允许创建一个 gallery incremental sync');
      return;
    }
    if (form.type === 'favorites' && hasFavoritesSource) {
      setErrorMsg('仅允许创建一个 favorites source sync');
      return;
    }
    if (form.type === 'incremental' && (!Array.isArray(form.config.categories) || form.config.categories.length === 0)) {
      setErrorMsg('incremental 至少选择一个分类');
      return;
    }
    setBusy(true);
    try {
      await createTask(buildPayload(form));
      setForm({ name: '', type: 'full', category: 'Cosplay', config: getDefaultConfig('full') });
      onCreated();
      onClose();
    } catch (err) {
      setErrorMsg(err.message || '创建任务失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <dialog
      ref={dialogRef}
      onClose={handleClose}
      aria-label="新建同步任务"
      className="m-auto w-full max-w-md mx-4 rounded-2xl border border-white/10 bg-zinc-900 text-white shadow-2xl p-0"
    >
        <div className="flex items-center justify-between px-6 py-4 border-b border-white/10">
          <h2 className="text-base font-semibold text-white">新建同步任务</h2>
          <button
            type="button"
            onClick={handleClose}
            className="p-2 -mr-1 text-gray-400 hover:text-white transition-colors rounded-lg hover:bg-white/10"
            aria-label="关闭"
          >
            <XCircle size={18} />
          </button>
        </div>

        <div className="px-6 py-5 space-y-4">
          <InputField
            id="task-name"
            label="任务名称"
            value={form.name}
            onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
            placeholder="e.g. cosplay-full-01"
          />
          <SelectField
            id="task-type"
            label="类型"
            value={form.type}
            onChange={(e) => handleTypeChange(e.target.value)}
            options={[
              { label: 'Gallery Full Scan', value: 'full' },
              { label: 'Gallery Incremental Sync', value: 'incremental' },
              { label: 'Favorites Source Sync', value: 'favorites' },
            ]}
          />
          {form.type === 'full' ? (
            <SelectField
              id="task-category"
              label="分类 (Category)"
              value={form.category}
              onChange={(e) => setForm((p) => ({ ...p, category: e.target.value }))}
              options={CATEGORY_OPTIONS}
            />
          ) : form.type === 'incremental' ? (
            <div>
              <span className="block text-xs font-medium text-gray-400 mb-1.5">分类 (categories)</span>
              <div className="rounded-lg border border-white/10 bg-white/5 p-2.5 space-y-1.5 max-h-44 overflow-y-auto">
                {CATEGORY_OPTIONS.map((category) => {
                  const checked = (form.config.categories || []).includes(category);
                  return (
                    <label key={category} className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggleIncrementalCategory(category)}
                        className="rounded border-white/20 bg-white/10 text-blue-500 focus:ring-blue-500/40"
                      />
                      <span>{category}</span>
                    </label>
                  );
                })}
              </div>
              <p className="mt-1.5 text-xs text-gray-500">Gallery incremental 使用 Mixed scope，按上传活跃度抓取。</p>
            </div>
          ) : null /* favorites: no category selector */}

          {/* Config fields */}
          <div className="rounded-xl border border-white/10 bg-white/3 p-4 space-y-3">
            <p className="text-xs font-medium text-gray-500 uppercase tracking-wider">Config</p>
            {form.type === 'full' ? (
              <InputField
                id="config-start-gid"
                label="start_gid (可选)"
                type="number"
                value={form.config.start_gid}
                onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, start_gid: e.target.value } }))}
                placeholder="留空从最新开始"
              />
            ) : form.type === 'favorites' ? (
              <InputField
                id="config-interval"
                label="run_interval_hours (同步间隔/小时)"
                type="number"
                step="1"
                value={form.config.run_interval_hours}
                onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, run_interval_hours: e.target.value } }))}
              />
            ) : (
              <>
                <InputField
                  id="config-scan-window"
                  label="scan_window"
                  type="number"
                  value={form.config.scan_window}
                  onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, scan_window: e.target.value } }))}
                />
                <InputField
                  id="config-rating-diff"
                  label="rating_diff_threshold"
                  type="number"
                  step="0.1"
                  value={form.config.rating_diff_threshold}
                  onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, rating_diff_threshold: e.target.value } }))}
                />
              </>
            )}
          </div>
          {form.type === 'incremental' && hasIncrementalTask && (
            <div role="alert" className="rounded-lg bg-amber-500/10 border border-amber-500/30 px-3 py-2 text-sm text-amber-300">
              已存在 gallery incremental sync。系统仅允许一个。
            </div>
          )}
          {form.type === 'favorites' && hasFavoritesSource && (
            <div role="alert" className="rounded-lg bg-amber-500/10 border border-amber-500/30 px-3 py-2 text-sm text-amber-300">
              已存在 favorites source sync。系统仅允许一个。
            </div>
          )}

          {errorMsg && (
            <div role="alert" className="rounded-lg bg-rose-500/10 border border-rose-500/30 px-3 py-2 text-sm text-rose-400">
              {errorMsg}
            </div>
          )}
        </div>

        <div className="flex justify-end gap-3 px-6 py-4 border-t border-white/10">
          <button
            type="button"
            onClick={handleClose}
            disabled={busy}
            className="px-4 py-2 text-sm rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-all disabled:opacity-50"
          >
            取消
          </button>
          <button
            type="submit"
            onClick={handleSubmit}
            disabled={busy || (form.type === 'incremental' && hasIncrementalTask) || (form.type === 'favorites' && hasFavoritesSource)}
            className="px-4 py-2 text-sm rounded-lg bg-blue-600 hover:bg-blue-500 text-white font-medium transition-all disabled:opacity-50 flex items-center gap-2"
          >
            {busy && <Loader2 size={14} className="animate-spin" />}
            创建任务
          </button>
        </div>
    </dialog>
  );
}

function DeleteTaskModal({ task, busy, onClose, onConfirm }) {
  const [value, setValue] = useState('');

  const handleClose = useCallback(() => {
    if (!busy) onClose();
  }, [busy, onClose]);
  const dialogRef = useRef(null);

  // Prevent closing (Escape) while a delete is in-flight
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const preventClose = (e) => { if (busy) e.preventDefault(); };
    dialog.addEventListener('cancel', preventClose);
    return () => dialog.removeEventListener('cancel', preventClose);
  }, [busy]);

  // Open the modal on mount
  useEffect(() => {
    dialogRef.current?.showModal();
  }, []);

  const canDelete = task && value.trim() === task.name;

  return (
    <dialog
      ref={dialogRef}
      onClose={handleClose}
      aria-label="确认删除任务"
      className="m-auto w-full max-w-md mx-4 rounded-2xl border border-rose-500/30 bg-zinc-900 text-white shadow-2xl p-0"
    >
        <div className="flex items-center gap-2 px-6 py-4 border-b border-white/10">
          <AlertTriangle size={16} className="text-rose-400" />
          <h2 className="text-base font-semibold text-white">确认删除任务</h2>
        </div>

        <div className="px-6 py-5 space-y-3">
          <p className="text-sm text-gray-300">
            请输入任务名以确认删除：
            <span className="ml-1 font-mono text-white">{task.name}</span>
          </p>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={task.name}
            aria-label="输入任务名以确认"
            className="w-full px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-white text-sm
                       placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-rose-500/50
                       focus:border-rose-500/50 transition-all"
          />
        </div>

        <div className="flex justify-end gap-3 px-6 py-4 border-t border-white/10">
          <button
            type="button"
            onClick={handleClose}
            disabled={busy}
            className="px-4 py-2 text-sm rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-all disabled:opacity-50"
          >
            取消
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={!canDelete || busy}
            className="px-4 py-2 text-sm rounded-lg bg-rose-600 hover:bg-rose-500 text-white font-medium transition-all disabled:opacity-50 flex items-center gap-2"
          >
            {busy && <Loader2 size={14} className="animate-spin" />}
            删除
          </button>
        </div>
    </dialog>
  );
}

// ─── Similarity Distribution Panel ────────────────────────────────────────────

function SimilarityDistributionPanel() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'similarityDistribution'],
    queryFn: getSimilarityDistribution,
  });
  const { data: statusData } = useQuery({
    queryKey: ['admin', 'embeddingsStatus'],
    queryFn: getEmbeddingsStatus,
  });

  const [localThreshold, setLocalThreshold] = useState(null);
  const [saving, setSaving] = useState(false);

  const dist = data || { buckets: [], total: 0, threshold: 0.3, count_above: 0 };
  const threshold = localThreshold ?? dist.threshold;

  // Reset local threshold when the server threshold changes (render-time adjustment)
  const prevServerThresholdRef = useRef(dist.threshold);
  if (dist.threshold !== prevServerThresholdRef.current) {
    prevServerThresholdRef.current = dist.threshold;
    setLocalThreshold(null);
  }

  const maxCount = Math.max(...dist.buckets.map((b) => b.count), 1);

  const countAbove = dist.buckets.reduce((sum, b) => {
    if (b.min >= threshold) return sum + b.count;
    if (b.min < threshold && b.max > threshold) {
      return sum + Math.round(b.count * (b.max - threshold) / (b.max - b.min));
    }
    return sum;
  }, 0);
  const displayCount = localThreshold == null ? dist.count_above : countAbove;

  const handleSave = async () => {
    if (localThreshold == null) return;
    setSaving(true);
    try {
      await updateThreshold(localThreshold);
      queryClient.invalidateQueries({ queryKey: ['admin', 'similarityDistribution'] });
    } finally {
      setSaving(false);
    }
  };

  const status = statusData || {
    vocab_size: 0, dim_count: 0, total_galleries: 0,
    embedded_count: 0, pending_count: 0,
    profile_liked_count: 0, profile_ready: false,
  };

  if (isLoading) {
    return (
      <div className="rounded-xl border border-white/10 bg-white/5 p-6 flex justify-center">
        <Loader2 size={20} className="animate-spin text-gray-500" />
      </div>
    );
  }

  return (
    <div className="rounded-xl border border-white/10 bg-white/5 backdrop-blur-sm p-5 shadow-sm space-y-4">
      {/* Embeddings status row */}
      <div className="flex items-center gap-4 text-xs text-gray-400 flex-wrap">
        <span>词表: <span className="text-white font-mono">{status.vocab_size.toLocaleString()}</span> / dim {status.dim_count.toLocaleString()}</span>
        <span>已嵌入: <span className="text-white font-mono">{status.embedded_count.toLocaleString()}</span> / {status.total_galleries.toLocaleString()}</span>
        {status.pending_count > 0 && (
          <span className="text-amber-400">待处理: {status.pending_count.toLocaleString()}</span>
        )}
        <span>偏好画廊: <span className="text-white font-mono">{status.profile_liked_count.toLocaleString()}</span></span>
        <span className={status.profile_ready ? 'text-emerald-400' : 'text-rose-400'}>
          {status.profile_ready ? 'profile ready' : 'profile not ready'}
        </span>
      </div>

      {!dist.buckets.length ? (
        <div className="text-center text-gray-500 text-sm py-6">
          暂无相似度数据，请先运行 Favorites Sync，等待词表构建和向量化完成
        </div>
      ) : (
        <>
          {/* Stats row */}
          <div className="flex items-center justify-between flex-wrap gap-2">
            <div className="flex items-center gap-4 text-sm">
              <span className="text-gray-400">
                候选总数: <span className="text-white font-semibold">{dist.total.toLocaleString()}</span>
              </span>
              <span className="text-gray-400">
                相似度 ≥ {threshold.toFixed(2)}: <span className="text-blue-400 font-semibold">{displayCount.toLocaleString()}</span>
              </span>
            </div>
            {localThreshold != null && localThreshold !== dist.threshold && (
              <button
                type="button"
                onClick={handleSave}
                disabled={saving}
                className="px-3 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium transition-all disabled:opacity-50 flex items-center gap-1.5"
              >
                {saving && <Loader2 size={12} className="animate-spin" />}
                保存阈值 {localThreshold.toFixed(2)}
              </button>
            )}
          </div>

          {/* Histogram */}
          <div className="relative" aria-label="相似度分布直方图">
            <div className="flex items-end gap-px h-32">
              {dist.buckets.map((b, i) => {
                const pct = b.count > 0 ? Math.log(b.count + 1) / Math.log(maxCount + 1) : 0;
                const aboveThreshold = b.min >= threshold;
                const partial = b.min < threshold && b.max > threshold;
                return (
                  <div
                    key={`${b.min.toFixed(3)}-${b.max.toFixed(3)}`}
                    className="flex-1 relative group"
                    style={{ height: '100%', display: 'flex', alignItems: 'flex-end' }}
                    title={`${b.min.toFixed(3)} – ${b.max.toFixed(3)}: ${b.count}`}
                  >
                    <div
                      className={`w-full rounded-t-sm transition-colors ${aboveThreshold ? 'bg-blue-500/70' : partial ? 'bg-blue-500/40' : 'bg-white/15'
                        }`}
                      style={{ height: `${Math.max(pct * 100, 0.5)}%` }}
                    />
                    <div className="absolute bottom-full mb-2 left-1/2 -translate-x-1/2 hidden group-hover:block z-10
                                    px-2 py-1 rounded bg-zinc-800 border border-white/10 text-xs text-gray-300 whitespace-nowrap shadow-lg">
                      {b.min.toFixed(3)} – {b.max.toFixed(3)}: {b.count}
                    </div>
                  </div>
                );
              })}
            </div>
            {/* Threshold line — positioned in [bucketMin, bucketMax] range */}
            {dist.buckets.length > 1 && (() => {
              const scoreMin = dist.buckets[0].min;
              const scoreMax = dist.buckets[dist.buckets.length - 1].max;
              if (scoreMax <= scoreMin) return null;
              const pos = ((threshold - scoreMin) / (scoreMax - scoreMin)) * 100;
              if (pos < 0 || pos > 100) return null;
              return (
                <div
                  className="absolute top-0 bottom-0 w-px bg-red-500/80 pointer-events-none"
                  style={{ left: `${pos}%` }}
                >
                  <span className="absolute -top-5 left-1/2 -translate-x-1/2 text-xs text-red-400 font-mono whitespace-nowrap">
                    {threshold.toFixed(2)}
                  </span>
                </div>
              );
            })()}
          </div>

          {/* X-axis labels */}
          <div className="flex justify-between text-xs text-gray-500 font-mono -mt-1">
            <span>{dist.buckets[0]?.min.toFixed(2) ?? '0.00'}</span>
            <span>{dist.buckets[dist.buckets.length - 1]?.max.toFixed(2) ?? '1.00'}</span>
          </div>

          {/* Slider */}
          <div className="flex items-center gap-3">
            <label htmlFor="threshold-slider" className="text-xs text-gray-400 shrink-0">阈值</label>
            <input
              id="threshold-slider"
              type="range"
              min={0}
              max={1}
              step={0.01}
              value={threshold}
              onChange={(e) => setLocalThreshold(Number(e.target.value))}
              className="flex-1 h-1.5 accent-blue-500 cursor-pointer"
            />
            <input
              type="number"
              min={0}
              max={1}
              step={0.01}
              value={threshold}
              onChange={(e) => setLocalThreshold(Number(e.target.value))}
              aria-label="阈值数值"
              className="w-20 px-2 py-1 rounded-lg bg-white/5 border border-white/10 text-white text-xs text-center
                         focus:outline-none focus:ring-2 focus:ring-blue-500/40 transition-all"
            />
          </div>
        </>
      )}
    </div>
  );
}

function SyncRunsPanel({
  tasks,
  isLoading,
  pendingByTask,
  runTaskAction,
  setDeleteTarget,
}) {
  const [expandedTaskIds, setExpandedTaskIds] = useState(() => new Set());
  const toggleTask = (taskId) => {
    setExpandedTaskIds((prev) => {
      const next = new Set(prev);
      if (next.has(taskId)) next.delete(taskId);
      else next.add(taskId);
      return next;
    });
  };

  if (isLoading) {
    return (
      <div className="rounded-lg border border-white/10 bg-zinc-950/45 flex justify-center items-center py-12">
        <Loader2 size={24} className="animate-spin text-gray-500" />
      </div>
    );
  }

  if (tasks.length === 0) {
    return (
      <div className="rounded-lg border border-white/10 bg-zinc-950/45 text-center py-12 text-gray-500 text-sm">
        暂无任务，点击「新建任务」开始
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <TaskHeaderStrip tasks={tasks} />
      <div className="space-y-2">
        {tasks.map((task) => (
          <SyncTaskRunRow
            key={task.id}
            task={task}
            rowAction={pendingByTask[task.id]}
            runTaskAction={runTaskAction}
            setDeleteTarget={setDeleteTarget}
            expanded={expandedTaskIds.has(task.id)}
            onToggle={() => toggleTask(task.id)}
          />
        ))}
      </div>
    </div>
  );
}

function QueuesPanel({ stats }) {
  return (
    <div>
      <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Thumb Queue</p>
      <div className="rounded-lg border border-white/10 bg-zinc-950/45 p-4 shadow-sm">
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3 w-full">
          <QueueStage
            icon={Loader2}
            label="Waiting"
            value={stats.waiting}
            color="text-orange-400"
            infoTitle="失败重试等待中，冷却后进入 Pending"
          />
          <QueueStage icon={RefreshCw} label="Pending" value={stats.pending} color="text-yellow-400" />
          <QueueStage icon={Loader2} label="Processing" value={stats.processing} color="text-blue-400" />
          <QueueStage icon={CheckCircle2} label="Done" value={stats.done} color="text-emerald-400" />
        </div>
      </div>
    </div>
  );
}

function RecommendationsPanel() {
  return (
    <div>
      <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Recommended Score Distribution</p>
      <SimilarityDistributionPanel />
    </div>
  );
}

// ─── Main Page ────────────────────────────────────────────────────────────────

const initialUiState = { createOpen: false, deleteTarget: null };
function uiReducer(state, action) {
  switch (action.type) {
    case 'openCreate':
      return { ...state, createOpen: true };
    case 'closeCreate':
      return { ...state, createOpen: false };
    case 'setDeleteTarget':
      return { ...state, deleteTarget: action.task };
    case 'closeDelete':
      return { ...state, deleteTarget: null };
    default:
      return state;
  }
}

export default function AdminPage() {
  const queryClient = useQueryClient();
  const [ui, dispatchUi] = useReducer(uiReducer, initialUiState);
  const [errorMsg, setErrorMsg] = useState('');
  const [pendingByTask, setPendingByTask] = useState({});
  const [activeTab, setActiveTab] = useState('sync');
  const sseRetryRef = useRef(0);

  const { createOpen: openCreate, deleteTarget } = ui;
  const setOpenCreate = (v) => dispatchUi({ type: v ? 'openCreate' : 'closeCreate' });
  const setDeleteTarget = (task) => dispatchUi({ type: task ? 'setDeleteTarget' : 'closeDelete', task });
  const modalOpen = openCreate || Boolean(deleteTarget);

  const { data: tasksData, isLoading: tasksLoading, isError: tasksError } = useQuery({
    queryKey: ['admin', 'tasks'],
    queryFn: getTasks,
    refetchInterval: false,
  });

  const { data: thumbData, isError: thumbError } = useQuery({
    queryKey: ['admin', 'thumbStats'],
    queryFn: getThumbStats,
    refetchInterval: false,
  });

  const tasks = tasksData || [];
  const stats = thumbData || { pending: 0, processing: 0, done: 0, waiting: 0 };

  useEffect(() => {
    if (modalOpen) return undefined;
    let cancelled = false;
    let source = null;
    let retryTimer = null;

    const scheduleReconnect = () => {
      if (cancelled) return;
      const attempt = sseRetryRef.current;
      const delay = Math.min(1000 * (2 ** attempt), 15000);
      sseRetryRef.current = attempt + 1;
      retryTimer = window.setTimeout(connect, delay);
    };

    const connect = () => {
      if (cancelled) return;
      source = new EventSource('/api/v1/admin/events');
      source.onopen = () => {
        sseRetryRef.current = 0;
      };
      source.addEventListener('admin.task', () => {
        queryClient.invalidateQueries({ queryKey: ['admin', 'tasks'] });
      });
      source.addEventListener('ping', () => {
        // Keepalive only. Do not refetch on heartbeat.
      });
      source.onerror = () => {
        if (source) {
          source.close();
          source = null;
        }
        scheduleReconnect();
      };
    };

    connect();

    return () => {
      cancelled = true;
      if (retryTimer) window.clearTimeout(retryTimer);
      if (source) source.close();
    };
  }, [modalOpen, queryClient]);

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['admin', 'tasks'] });
    queryClient.invalidateQueries({ queryKey: ['admin', 'thumbStats'] });
  };

  const upsertTaskCache = (task) => {
    if (!task?.id) return;
    queryClient.setQueryData(['admin', 'tasks'], (prev = []) => {
      const exists = prev.some((item) => item.id === task.id);
      if (!exists) return prev;
      return prev.map((item) => (item.id === task.id ? task : item));
    });
  };

  const removeTaskCache = (taskId) => {
    queryClient.setQueryData(['admin', 'tasks'], (prev = []) => prev.filter((item) => item.id !== taskId));
  };

  const setTaskPending = (taskId, action) => {
    setPendingByTask((prev) => ({ ...prev, [taskId]: action }));
  };

  const clearTaskPending = (taskId) => {
    setPendingByTask((prev) => {
      const next = { ...prev };
      delete next[taskId];
      return next;
    });
  };

  const runTaskAction = async (taskId, action, fn, onSuccess) => {
    setErrorMsg('');
    setTaskPending(taskId, action);
    try {
      const result = await fn();
      upsertTaskCache(result);
      if (action === 'delete') {
        removeTaskCache(taskId);
      }
      refresh();
      if (onSuccess) onSuccess(result);
    } catch (err) {
      setErrorMsg(err.message || '操作失败');
    } finally {
      clearTaskPending(taskId);
    }
  };

  const handleDeleteConfirm = async () => {
    if (!deleteTarget) return;
    await runTaskAction(deleteTarget.id, 'delete', () => deleteTask(deleteTarget.id, true), () => {
      setDeleteTarget(null);
    });
  };

  return (
    <div className="space-y-6 pb-8">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-white">Admin</h1>
          <p className="text-sm text-gray-500 mt-0.5">任务调度 &amp; 缩略图队列</p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => setOpenCreate(true)}
            className="flex items-center gap-2 px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-sm font-medium transition-all"
          >
            <Plus size={16} />
            新建任务
          </button>
        </div>
      </div>

      {/* Error Banner */}
      {errorMsg && (
        <div role="alert" className="rounded-xl bg-rose-500/10 border border-rose-500/30 px-4 py-3 text-sm text-rose-400 flex items-center gap-2">
          <XCircle size={16} />
          {errorMsg}
          <button type="button" onClick={() => setErrorMsg('')} className="ml-auto p-1 hover:text-white transition-colors rounded" aria-label="关闭错误提示">
            <XCircle size={14} />
          </button>
        </div>
      )}
      {(tasksError || thumbError) && (
        <div role="alert" className="rounded-xl bg-rose-500/10 border border-rose-500/30 px-4 py-3 text-sm text-rose-400 flex items-center gap-2">
          <XCircle size={16} />
          加载数据失败，请检查后端连接
        </div>
      )}

      <div className="border-b border-white/10">
        <div className="flex items-center gap-1 overflow-x-auto">
          {ADMIN_TABS.map((tab) => {
            const active = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                type="button"
                onClick={() => setActiveTab(tab.id)}
                className={`px-3 py-2 text-sm border-b transition-colors whitespace-nowrap ${active
                  ? 'border-cyan-300 text-white'
                  : 'border-transparent text-gray-500 hover:text-gray-300'
                  }`}
                aria-pressed={active}
              >
                {tab.label}
              </button>
            );
          })}
        </div>
      </div>

      {activeTab === 'sync' && (
        <SyncRunsPanel
          tasks={tasks}
          isLoading={tasksLoading}
          pendingByTask={pendingByTask}
          runTaskAction={runTaskAction}
          setDeleteTarget={setDeleteTarget}
        />
      )}
      {activeTab === 'queues' && <QueuesPanel stats={stats} />}
      {activeTab === 'recommendations' && <RecommendationsPanel />}

      {/* Create Task Modal */}
      {openCreate && (
        <CreateTaskModal
          onClose={() => setOpenCreate(false)}
          onCreated={refresh}
          tasks={tasks}
        />
      )}

      {/* Delete Confirm Modal */}
      {deleteTarget && (
        <DeleteTaskModal
          task={deleteTarget}
          busy={pendingByTask[deleteTarget.id] === 'delete'}
          onClose={() => setDeleteTarget(null)}
          onConfirm={handleDeleteConfirm}
        />
      )}
    </div>
  );
}
