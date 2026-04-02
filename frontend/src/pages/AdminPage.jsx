import React, { useEffect, useState, useCallback } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Loader2,
  CheckCircle2,
  XCircle,
  Play,
  Square,
  Trash2,
  Plus,
  RefreshCw,
  ChevronDown,
  AlertTriangle,
  Info,
} from 'lucide-react';
import {
  createTask,
  deleteTask,
  getTasks,
  getThumbStats,
  getScoreDistribution,
  updateThreshold,
  startTask,
  stopTask,
} from '../api/admin';
import { useCountUp } from '../hooks/useCountUp';
import { useFocusTrap } from '../hooks/useFocusTrap';

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
  return (
    (task.status === 'stopped' && task.desired_status === 'running')
    || (task.status === 'running' && task.desired_status === 'stopped')
  );
}

function getDisplayStatus(task) {
  if (task.status === 'stopped' && task.desired_status === 'running') return 'starting';
  if (task.status === 'running' && task.desired_status === 'stopped') return 'stopping';
  if (task.type === 'favorites' && task.status === 'completed' && task.desired_status === 'running') return 'scheduled';
  return task.status;
}

function formatTaskCategory(task) {
  if (task.type === 'favorites') return 'Favorites';
  if (task.type !== 'incremental') return task.category;
  const categories = Array.isArray(task.config?.categories) ? task.config.categories : [];
  if (!categories.length) return `${MIXED_CATEGORY}(0)`;
  return `${MIXED_CATEGORY}(${categories.length}): ${categories.join(', ')}`;
}

// ─── Sub-components ──────────────────────────────────────────────────────────

const STATUS_CONFIG = {
  starting: { text: 'text-cyan-300', ring: 'ring-cyan-500/30', bg: 'bg-cyan-500/10' },
  running: { text: 'text-blue-400', ring: 'ring-blue-500/30', bg: 'bg-blue-500/10' },
  stopping: { text: 'text-amber-300', ring: 'ring-amber-500/30', bg: 'bg-amber-500/10' },
  stopped: { text: 'text-gray-400', ring: 'ring-gray-500/30', bg: 'bg-gray-500/10' },
  completed: { text: 'text-emerald-400', ring: 'ring-emerald-500/30', bg: 'bg-emerald-500/10' },
  scheduled: { text: 'text-cyan-400', ring: 'ring-cyan-500/30', bg: 'bg-cyan-500/10' },
  error: { text: 'text-rose-400', ring: 'ring-rose-500/30', bg: 'bg-rose-500/10' },
};

function StatusBadge({ status }) {
  const cfg = STATUS_CONFIG[status] || STATUS_CONFIG.stopped;
  const spinning = status === 'starting' || status === 'stopping';

  return (
    <span className={`inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium ring-1 ${cfg.bg} ${cfg.text} ${cfg.ring}`}>
      {spinning ? (
        <Loader2 size={11} className="animate-spin" />
      ) : (
        <span className={`w-1.5 h-1.5 rounded-full ${status === 'error' ? 'bg-rose-400' : status === 'completed' ? 'bg-emerald-400' : status === 'running' ? 'bg-blue-400' : 'bg-gray-400'}`} aria-hidden="true" />
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
      <div className="h-1.5 w-full rounded-full bg-white/10 overflow-hidden" role="progressbar" aria-valuenow={clampedPct} aria-valuemin={0} aria-valuemax={100}>
        <div
          className="h-full rounded-full transition-all duration-700 ease-out"
          style={{
            width: `${clampedPct}%`,
            background: 'linear-gradient(90deg, #3b82f6 0%, #10b981 100%)',
          }}
        />
      </div>
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

function CreateTaskModal({ open, onClose, onCreated, tasks }) {
  const [busy, setBusy] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');
  const [form, setForm] = useState({
    name: '', type: 'full', category: 'Cosplay', config: getDefaultConfig('full'),
  });
  const hasIncrementalTask = (tasks || []).some((task) => task.type === 'incremental');
  const hasFavoritesTask = (tasks || []).some((task) => task.type === 'favorites');

  const handleClose = useCallback(() => {
    if (!busy) onClose();
  }, [busy, onClose]);
  const dialogRef = useFocusTrap(open, handleClose);

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
      setErrorMsg('仅允许创建一个 incremental 任务');
      return;
    }
    if (form.type === 'favorites' && hasFavoritesTask) {
      setErrorMsg('仅允许创建一个 favorites 任务');
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

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={handleClose}
      />
      {/* Modal */}
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="新建同步任务"
        className="relative w-full max-w-md mx-4 rounded-2xl border border-white/10 bg-zinc-900 shadow-2xl"
      >
        <div className="flex items-center justify-between px-6 py-4 border-b border-white/10">
          <h2 className="text-base font-semibold text-white">新建同步任务</h2>
          <button
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
              { label: 'Full Scan', value: 'full' },
              { label: 'Incremental', value: 'incremental' },
              { label: 'Favorites Sync', value: 'favorites' },
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
              <label className="block text-xs font-medium text-gray-400 mb-1.5">分类 (categories)</label>
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
              <p className="mt-1.5 text-xs text-gray-500">Incremental 使用 Mixed 模式，按上传活跃度抓取。</p>
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
              已存在 incremental 任务。系统仅允许一个 incremental 任务。
            </div>
          )}
          {form.type === 'favorites' && hasFavoritesTask && (
            <div role="alert" className="rounded-lg bg-amber-500/10 border border-amber-500/30 px-3 py-2 text-sm text-amber-300">
              已存在 favorites 任务。系统仅允许一个 favorites 任务。
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
            onClick={handleClose}
            disabled={busy}
            className="px-4 py-2 text-sm rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-all disabled:opacity-50"
          >
            取消
          </button>
          <button
            onClick={handleSubmit}
            disabled={busy || (form.type === 'incremental' && hasIncrementalTask) || (form.type === 'favorites' && hasFavoritesTask)}
            className="px-4 py-2 text-sm rounded-lg bg-blue-600 hover:bg-blue-500 text-white font-medium transition-all disabled:opacity-50 flex items-center gap-2"
          >
            {busy && <Loader2 size={14} className="animate-spin" />}
            创建任务
          </button>
        </div>
      </div>
    </div>
  );
}

function DeleteTaskModal({ open, task, busy, onClose, onConfirm }) {
  const [value, setValue] = useState('');

  const handleClose = useCallback(() => {
    if (!busy) onClose();
  }, [busy, onClose]);
  const dialogRef = useFocusTrap(open && Boolean(task), handleClose);

  useEffect(() => {
    setValue('');
  }, [open, task?.id]);

  if (!open || !task) return null;

  const canDelete = value.trim() === task.name;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" onClick={handleClose} />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="确认删除任务"
        className="relative w-full max-w-md mx-4 rounded-2xl border border-rose-500/30 bg-zinc-900 shadow-2xl"
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
            onClick={handleClose}
            disabled={busy}
            className="px-4 py-2 text-sm rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-all disabled:opacity-50"
          >
            取消
          </button>
          <button
            onClick={onConfirm}
            disabled={!canDelete || busy}
            className="px-4 py-2 text-sm rounded-lg bg-rose-600 hover:bg-rose-500 text-white font-medium transition-all disabled:opacity-50 flex items-center gap-2"
          >
            {busy && <Loader2 size={14} className="animate-spin" />}
            删除
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── Score Distribution Panel ─────────────────────────────────────────────────

function ScoreDistributionPanel() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'scoreDistribution'],
    queryFn: getScoreDistribution,
  });

  const [localThreshold, setLocalThreshold] = useState(null);
  const [saving, setSaving] = useState(false);

  const dist = data || { buckets: [], total: 0, threshold: 20, count_above: 0 };
  const threshold = localThreshold ?? dist.threshold;

  // Reset local when server data arrives
  useEffect(() => {
    if (data) setLocalThreshold(null);
  }, [data?.threshold]);

  const maxCount = Math.max(...dist.buckets.map((b) => b.count), 1);

  // Compute count_above for the local threshold
  const countAbove = dist.buckets.reduce((sum, b) => {
    if (b.max > threshold) return sum + b.count;
    if (b.min <= threshold && b.max > threshold) return sum + Math.round(b.count * (b.max - threshold) / (b.max - b.min));
    return sum;
  }, 0);
  // Use server count_above when unchanged, local estimate when dragging
  const displayCount = localThreshold == null ? dist.count_above : countAbove;

  const handleSave = async () => {
    if (localThreshold == null) return;
    setSaving(true);
    try {
      await updateThreshold(localThreshold);
      queryClient.invalidateQueries({ queryKey: ['admin', 'scoreDistribution'] });
    } finally {
      setSaving(false);
    }
  };

  if (isLoading) {
    return (
      <div className="rounded-xl border border-white/10 bg-white/5 p-6 flex justify-center">
        <Loader2 size={20} className="animate-spin text-gray-500" />
      </div>
    );
  }

  if (!dist.buckets.length) {
    return (
      <div className="rounded-xl border border-white/10 bg-white/5 p-6 text-center text-gray-500 text-sm">
        暂无推荐数据，请先运行 Favorites Sync 并等待评分完成
      </div>
    );
  }

  const scoreMin = dist.buckets[0]?.min ?? 0;
  const scoreMax = dist.buckets[dist.buckets.length - 1]?.max ?? 100;

  return (
    <div className="rounded-xl border border-white/10 bg-white/5 backdrop-blur-sm p-5 shadow-sm space-y-4">
      {/* Stats row */}
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div className="flex items-center gap-4 text-sm">
          <span className="text-gray-400">
            推荐总数: <span className="text-white font-semibold">{dist.total.toLocaleString()}</span>
          </span>
          <span className="text-gray-400">
            阈值 ≥ {threshold}: <span className="text-blue-400 font-semibold">{displayCount.toLocaleString()}</span>
          </span>
        </div>
        {localThreshold != null && localThreshold !== dist.threshold && (
          <button
            onClick={handleSave}
            disabled={saving}
            className="px-3 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium transition-all disabled:opacity-50 flex items-center gap-1.5"
          >
            {saving && <Loader2 size={12} className="animate-spin" />}
            保存阈值 {localThreshold}
          </button>
        )}
      </div>

      {/* Histogram */}
      <div className="relative" aria-label="评分分布直方图">
        <div className="flex items-end gap-px h-32">
          {dist.buckets.map((b, i) => {
            const pct = b.count > 0 ? Math.log(b.count + 1) / Math.log(maxCount + 1) : 0;
            const aboveThreshold = b.min >= threshold;
            const partial = b.min < threshold && b.max > threshold;
            return (
              <div
                key={i}
                className="flex-1 relative group"
                style={{ height: '100%', display: 'flex', alignItems: 'flex-end' }}
                title={`${b.min.toFixed(1)} – ${b.max.toFixed(1)}: ${b.count}`}
              >
                <div
                  className={`w-full rounded-t-sm transition-colors ${aboveThreshold ? 'bg-blue-500/70' : partial ? 'bg-blue-500/40' : 'bg-white/15'
                    }`}
                  style={{ height: `${Math.max(pct * 100, 0.5)}%` }}
                />
                {/* Tooltip */}
                <div className="absolute bottom-full mb-2 left-1/2 -translate-x-1/2 hidden group-hover:block z-10
                                px-2 py-1 rounded bg-zinc-800 border border-white/10 text-xs text-gray-300 whitespace-nowrap shadow-lg">
                  {b.min.toFixed(1)} – {b.max.toFixed(1)}: {b.count}
                </div>
              </div>
            );
          })}
        </div>
        {/* Threshold line */}
        {scoreMax > scoreMin && (
          <div
            className="absolute top-0 bottom-0 w-px bg-red-500/80 pointer-events-none"
            style={{ left: `${((threshold - scoreMin) / (scoreMax - scoreMin)) * 100}%` }}
          >
            <span className="absolute -top-5 left-1/2 -translate-x-1/2 text-xs text-red-400 font-mono whitespace-nowrap">
              {threshold}
            </span>
          </div>
        )}
      </div>

      {/* X-axis labels */}
      <div className="flex justify-between text-xs text-gray-500 font-mono -mt-1">
        <span>{scoreMin.toFixed(0)}</span>
        <span>{scoreMax.toFixed(0)}</span>
      </div>

      {/* Slider */}
      <div className="flex items-center gap-3">
        <label htmlFor="threshold-slider" className="text-xs text-gray-400 shrink-0">阈值</label>
        <input
          id="threshold-slider"
          type="range"
          min={scoreMin}
          max={scoreMax}
          step={0.5}
          value={threshold}
          onChange={(e) => setLocalThreshold(Number(e.target.value))}
          className="flex-1 h-1.5 accent-blue-500 cursor-pointer"
        />
        <input
          type="number"
          min={0}
          step={0.5}
          value={threshold}
          onChange={(e) => setLocalThreshold(Number(e.target.value))}
          aria-label="阈值数值"
          className="w-20 px-2 py-1 rounded-lg bg-white/5 border border-white/10 text-white text-xs text-center
                     focus:outline-none focus:ring-2 focus:ring-blue-500/40 transition-all"
        />
      </div>
    </div>
  );
}

// ─── Main Page ────────────────────────────────────────────────────────────────

export default function AdminPage() {
  const queryClient = useQueryClient();
  const [openCreate, setOpenCreate] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');
  const [pendingByTask, setPendingByTask] = useState({});
  const [deleteTarget, setDeleteTarget] = useState(null);
  const [autoRefresh, setAutoRefresh] = useState(true);

  const modalOpen = openCreate || Boolean(deleteTarget);
  const shouldPoll = autoRefresh && !modalOpen;

  const tasksQuery = useQuery({
    queryKey: ['admin', 'tasks'],
    queryFn: getTasks,
    refetchInterval: shouldPoll ? 5000 : false,
  });

  const thumbQuery = useQuery({
    queryKey: ['admin', 'thumbStats'],
    queryFn: getThumbStats,
    refetchInterval: shouldPoll ? 5000 : false,
  });

  const isFetching = tasksQuery.isFetching || thumbQuery.isFetching;

  const tasks = tasksQuery.data || [];
  const stats = thumbQuery.data || { pending: 0, processing: 0, done: 0, waiting: 0 };

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
            onClick={() => setAutoRefresh((v) => !v)}
            className={`relative p-2.5 rounded-lg transition-all ${autoRefresh
              ? 'text-emerald-400 hover:bg-emerald-500/10'
              : 'text-gray-400 hover:text-white hover:bg-white/10'
              }`}
            aria-label={autoRefresh ? '暂停自动刷新' : '开启自动刷新'}
            aria-pressed={autoRefresh}
          >
            {autoRefresh && (
              <svg
                className="absolute inset-0 w-full h-full pointer-events-none"
                viewBox="0 0 32 32"
                fill="none"
                aria-hidden="true"
              >
                <circle
                  cx="16" cy="16" r="13"
                  strokeWidth="1.5"
                  transform="rotate(-90 16 16)"
                  style={{
                    strokeDasharray: '81.68',
                    animation: 'refresh-ring 5s linear infinite, refresh-ring-pulse 5s linear infinite',
                  }}
                />
              </svg>
            )}
            <RefreshCw size={16} className={isFetching ? 'animate-spin' : ''} />
          </button>
          <button
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
          <button onClick={() => setErrorMsg('')} className="ml-auto p-1 hover:text-white transition-colors rounded" aria-label="关闭错误提示">
            <XCircle size={14} />
          </button>
        </div>
      )}
      {(tasksQuery.isError || thumbQuery.isError) && (
        <div role="alert" className="rounded-xl bg-rose-500/10 border border-rose-500/30 px-4 py-3 text-sm text-rose-400 flex items-center gap-2">
          <XCircle size={16} />
          加载数据失败，请检查后端连接
        </div>
      )}

      {/* Queue Flow */}
      <div>
        <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Thumb Queue</p>
        <div className="rounded-xl border border-white/10 bg-white/5 backdrop-blur-sm p-4 shadow-sm">
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

      {/* Recommended Score Distribution */}
      <div>
        <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Recommended Score Distribution</p>
        <ScoreDistributionPanel />
      </div>

      {/* Sync Tasks */}
      <div>
        <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Sync Tasks</p>
        {tasksQuery.isLoading ? (
          <div className="rounded-xl border border-white/10 bg-white/5 flex justify-center items-center py-12">
            <Loader2 size={24} className="animate-spin text-gray-500" />
          </div>
        ) : tasks.length === 0 ? (
          <div className="rounded-xl border border-white/10 bg-white/5 text-center py-12 text-gray-500 text-sm">
            暂无任务，点击「新建任务」开始
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            {tasks.map((task) => {
              const progress = Number(task.progress_pct || 0);
              const isIncremental = task.type === 'incremental';
              const isFavorites = task.type === 'favorites';
              const dbCount = isIncremental
                ? (task.state?.scanned_count ?? null)
                : isFavorites
                  ? null
                  : (task.state?.db_count ?? null);
              const totalCount = isIncremental
                ? (task.config?.scan_window ?? null)
                : isFavorites
                  ? null
                  : (task.state?.total_count ?? null);
              const transition = isTransitioning(task);
              const displayStatus = getDisplayStatus(task);
              const rowAction = pendingByTask[task.id];
              const rowBusy = Boolean(rowAction);

              const canStart = !rowBusy && !transition && task.status !== 'running'
                && (task.status !== 'completed' || task.type === 'favorites');
              const canStop = !rowBusy && !transition && task.status === 'running';
              const canDelete = !rowBusy && !transition && task.status !== 'running'
                && (task.desired_status !== 'running' || (task.type === 'favorites' && task.status === 'completed'));

              return (
                <div
                  key={task.id}
                  className="rounded-xl border border-white/10 bg-white/5 backdrop-blur-sm p-4 hover:border-white/20 transition-colors"
                >
                  {/* Row 1: name + status + actions */}
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="font-medium text-white text-sm">{task.name}</span>
                        <span className="text-xs font-mono text-gray-400 bg-white/5 px-2 py-0.5 rounded">{task.type}</span>
                        <StatusBadge status={displayStatus} />
                      </div>
                      <p className="text-xs text-gray-500 mt-1">{formatTaskCategory(task)}</p>
                      {task.error_message && (
                        <p className="text-xs text-rose-400 mt-1 truncate" title={task.error_message}>
                          {task.error_message}
                        </p>
                      )}
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <button
                        title="启动"
                        aria-label={`启动任务 ${task.name}`}
                        disabled={!canStart}
                        onClick={() => runTaskAction(task.id, 'start', () => startTask(task.id))}
                        className="p-2 rounded-lg text-emerald-400 hover:bg-emerald-500/20 transition-all
                                   disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                      >
                        {rowAction === 'start' ? <Loader2 size={15} className="animate-spin" /> : <Play size={15} />}
                      </button>
                      <button
                        title="停止"
                        aria-label={`停止任务 ${task.name}`}
                        disabled={!canStop}
                        onClick={() => runTaskAction(task.id, 'stop', () => stopTask(task.id))}
                        className="p-2 rounded-lg text-yellow-400 hover:bg-yellow-500/20 transition-all
                                   disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                      >
                        {rowAction === 'stop' ? <Loader2 size={15} className="animate-spin" /> : <Square size={15} />}
                      </button>
                      <button
                        title="删除"
                        aria-label={`删除任务 ${task.name}`}
                        disabled={!canDelete}
                        onClick={() => setDeleteTarget(task)}
                        className="p-2 rounded-lg text-rose-400 hover:bg-rose-500/20 transition-all
                                   disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                      >
                        {rowAction === 'delete' ? <Loader2 size={15} className="animate-spin" /> : <Trash2 size={15} />}
                      </button>
                    </div>
                  </div>
                  {/* Row 2: progress */}
                  <div className="mt-3">
                    <GradientProgressBar
                      progress={progress}
                      dbCount={dbCount}
                      totalCount={totalCount}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* Create Task Modal */}
      <CreateTaskModal
        open={openCreate}
        onClose={() => setOpenCreate(false)}
        onCreated={refresh}
        tasks={tasks}
      />

      {/* Delete Confirm Modal */}
      <DeleteTaskModal
        open={Boolean(deleteTarget)}
        task={deleteTarget}
        busy={deleteTarget ? pendingByTask[deleteTarget.id] === 'delete' : false}
        onClose={() => setDeleteTarget(null)}
        onConfirm={handleDeleteConfirm}
      />
    </div>
  );
}
