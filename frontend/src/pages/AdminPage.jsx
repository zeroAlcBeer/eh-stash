import React, { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Clock,
  Loader2,
  CheckCircle2,
  XCircle,
  Play,
  Square,
  Trash2,
  Plus,
  RefreshCw,
  ChevronDown,
} from 'lucide-react';
import {
  createTask,
  deleteTask,
  getTasks,
  getThumbStats,
  startTask,
  stopTask,
} from '../api/admin';

// ─── Constants ───────────────────────────────────────────────────────────────

const CATEGORY_OPTIONS = [
  'Misc', 'Doujinshi', 'Manga', 'Artist CG', 'Game CG',
  'Image Set', 'Cosplay', 'Asian Porn', 'Non-H', 'Western',
];

const DEFAULT_FULL = { start_gid: '' };
const DEFAULT_INCREMENTAL = {
  detail_quota: 25,
  gid_window: 10000,
  rating_diff_threshold: 0.5,
};

function getDefaultConfig(type) {
  return type === 'full' ? { ...DEFAULT_FULL } : { ...DEFAULT_INCREMENTAL };
}

function buildPayload(form) {
  const base = { name: form.name.trim(), type: form.type, category: form.category };
  if (form.type === 'full') {
    return { ...base, config: { start_gid: form.config.start_gid === '' ? null : Number(form.config.start_gid) } };
  }
  return {
    ...base,
    config: {
      detail_quota: Number(form.config.detail_quota),
      gid_window: Number(form.config.gid_window),
      rating_diff_threshold: Number(form.config.rating_diff_threshold),
    },
  };
}

// ─── Sub-components ──────────────────────────────────────────────────────────

const STATUS_CONFIG = {
  running: { dot: 'bg-blue-400', text: 'text-blue-400', ring: 'ring-blue-500/30', bg: 'bg-blue-500/10' },
  stopped: { dot: 'bg-gray-400', text: 'text-gray-400', ring: 'ring-gray-500/30', bg: 'bg-gray-500/10' },
  completed: { dot: 'bg-emerald-400', text: 'text-emerald-400', ring: 'ring-emerald-500/30', bg: 'bg-emerald-500/10' },
  error: { dot: 'bg-rose-400', text: 'text-rose-400', ring: 'ring-rose-500/30', bg: 'bg-rose-500/10' },
};

function StatusBadge({ status }) {
  const cfg = STATUS_CONFIG[status] || STATUS_CONFIG.stopped;
  return (
    <span className={`inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium ring-1 ${cfg.bg} ${cfg.text} ${cfg.ring}`}>
      <span className={`w-1.5 h-1.5 rounded-full ${cfg.dot}`} />
      {status}
    </span>
  );
}

function GradientProgressBar({ progress, dbCount, totalCount }) {
  const clampedPct = Math.max(0, Math.min(100, progress));
  const displayPct = progress < 1 ? progress.toFixed(3) : progress.toFixed(1);

  const numerator = dbCount != null ? Number(dbCount).toLocaleString() : '—';
  const denominator = totalCount != null ? Number(totalCount).toLocaleString() : '—';

  return (
    <div className="min-w-[180px]">
      <div className="flex items-center justify-between mb-1">
        <span className="text-xs text-gray-400 font-mono">
          {numerator} / {denominator}
        </span>
        <span className="text-xs font-semibold text-white ml-2">{displayPct}%</span>
      </div>
      <div className="h-1.5 w-full rounded-full bg-white/10 overflow-hidden">
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

function StatCard({ icon: Icon, label, value, color }) {
  return (
    <div className="relative overflow-hidden rounded-xl border border-white/10 bg-white/5 backdrop-blur-sm p-5 shadow-sm">
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium text-gray-400 uppercase tracking-wider">{label}</p>
          <p className={`mt-1.5 text-3xl font-bold ${color}`}>{value}</p>
        </div>
        <div className={`p-2.5 rounded-lg bg-white/5`}>
          <Icon size={18} className={color} />
        </div>
      </div>
    </div>
  );
}

function InputField({ label, type = 'text', value, onChange, placeholder, step }) {
  return (
    <div>
      <label className="block text-xs font-medium text-gray-400 mb-1.5">{label}</label>
      <input
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

function SelectField({ label, value, onChange, options }) {
  return (
    <div>
      <label className="block text-xs font-medium text-gray-400 mb-1.5">{label}</label>
      <div className="relative">
        <select
          value={value}
          onChange={onChange}
          className="w-full appearance-none px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-white text-sm
                     focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500/50
                     transition-all cursor-pointer"
        >
          {options.map((opt) => (
            <option key={opt.value ?? opt} value={opt.value ?? opt} className="bg-[#1e1e1e]">
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

function CreateTaskModal({ open, onClose, onCreated }) {
  const [busy, setBusy] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');
  const [form, setForm] = useState({
    name: '', type: 'full', category: 'Cosplay', config: getDefaultConfig('full'),
  });

  const handleTypeChange = (nextType) => {
    setForm((prev) => ({ ...prev, type: nextType, config: getDefaultConfig(nextType) }));
  };

  const handleSubmit = async () => {
    setErrorMsg('');
    if (!form.name.trim()) { setErrorMsg('名称不能为空'); return; }
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
        onClick={onClose}
      />
      {/* Modal */}
      <div className="relative w-full max-w-md mx-4 rounded-2xl border border-white/10 bg-[#1a1a1a] shadow-2xl">
        <div className="flex items-center justify-between px-6 py-4 border-b border-white/10">
          <h2 className="text-base font-semibold text-white">新建同步任务</h2>
          <button
            onClick={onClose}
            className="text-gray-400 hover:text-white transition-colors p-1 rounded-lg hover:bg-white/10"
          >
            <XCircle size={18} />
          </button>
        </div>

        <div className="px-6 py-5 space-y-4">
          <InputField
            label="任务名称"
            value={form.name}
            onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
            placeholder="e.g. cosplay-full-01"
          />
          <SelectField
            label="类型"
            value={form.type}
            onChange={(e) => handleTypeChange(e.target.value)}
            options={[{ label: 'Full Scan', value: 'full' }, { label: 'Incremental', value: 'incremental' }]}
          />
          <SelectField
            label="分类 (Category)"
            value={form.category}
            onChange={(e) => setForm((p) => ({ ...p, category: e.target.value }))}
            options={CATEGORY_OPTIONS}
          />

          {/* Config fields */}
          <div className="rounded-xl border border-white/10 bg-white/3 p-4 space-y-3">
            <p className="text-xs font-medium text-gray-500 uppercase tracking-wider">Config</p>
            {form.type === 'full' ? (
              <InputField
                label="start_gid (可选)"
                type="number"
                value={form.config.start_gid}
                onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, start_gid: e.target.value } }))}
                placeholder="留空从最新开始"
              />
            ) : (
              <>
                <InputField
                  label="detail_quota"
                  type="number"
                  value={form.config.detail_quota}
                  onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, detail_quota: e.target.value } }))}
                />
                <InputField
                  label="gid_window"
                  type="number"
                  value={form.config.gid_window}
                  onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, gid_window: e.target.value } }))}
                />
                <InputField
                  label="rating_diff_threshold"
                  type="number"
                  step="0.1"
                  value={form.config.rating_diff_threshold}
                  onChange={(e) => setForm((p) => ({ ...p, config: { ...p.config, rating_diff_threshold: e.target.value } }))}
                />
              </>
            )}
          </div>

          {errorMsg && (
            <div className="rounded-lg bg-rose-500/10 border border-rose-500/30 px-3 py-2 text-sm text-rose-400">
              {errorMsg}
            </div>
          )}
        </div>

        <div className="flex justify-end gap-3 px-6 py-4 border-t border-white/10">
          <button
            onClick={onClose}
            disabled={busy}
            className="px-4 py-2 text-sm rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-all disabled:opacity-50"
          >
            取消
          </button>
          <button
            onClick={handleSubmit}
            disabled={busy}
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

// ─── Main Page ────────────────────────────────────────────────────────────────

export default function AdminPage() {
  const queryClient = useQueryClient();
  const [openCreate, setOpenCreate] = useState(false);
  const [busy, setBusy] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');

  const tasksQuery = useQuery({
    queryKey: ['admin', 'tasks'],
    queryFn: getTasks,
    refetchInterval: 5000,
  });

  const thumbQuery = useQuery({
    queryKey: ['admin', 'thumbStats'],
    queryFn: getThumbStats,
    refetchInterval: 5000,
  });

  const tasks = tasksQuery.data || [];
  const stats = thumbQuery.data || { pending: 0, processing: 0, done: 0, waiting: 0 };

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['admin', 'tasks'] });
    queryClient.invalidateQueries({ queryKey: ['admin', 'thumbStats'] });
  };

  const runAction = async (fn) => {
    setErrorMsg('');
    setBusy(true);
    try {
      await fn();
      refresh();
    } catch (err) {
      setErrorMsg(err.message || '操作失败');
    } finally {
      setBusy(false);
    }
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
            onClick={refresh}
            className="p-2 rounded-lg text-gray-400 hover:text-white hover:bg-white/10 transition-all"
            title="刷新"
          >
            <RefreshCw size={16} />
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
        <div className="rounded-xl bg-rose-500/10 border border-rose-500/30 px-4 py-3 text-sm text-rose-400 flex items-center gap-2">
          <XCircle size={16} />
          {errorMsg}
        </div>
      )}
      {(tasksQuery.isError || thumbQuery.isError) && (
        <div className="rounded-xl bg-rose-500/10 border border-rose-500/30 px-4 py-3 text-sm text-rose-400 flex items-center gap-2">
          <XCircle size={16} />
          加载数据失败，请检查后端连接
        </div>
      )}

      {/* Bento Stats Grid */}
      <div>
        <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Thumb Queue</p>
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <StatCard icon={Clock} label="Pending" value={stats.pending} color="text-yellow-400" />
          <StatCard icon={Loader2} label="Processing" value={stats.processing} color="text-blue-400" />
          <StatCard icon={CheckCircle2} label="Done" value={stats.done} color="text-emerald-400" />
          <StatCard icon={XCircle} label="Waiting" value={stats.waiting} color="text-orange-400" />
        </div>
      </div>

      {/* Sync Tasks Table */}
      <div>
        <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-3">Sync Tasks</p>
        <div className="rounded-xl border border-white/10 bg-white/5 backdrop-blur-sm overflow-x-auto shadow-sm">
          {tasksQuery.isLoading ? (
            <div className="flex justify-center items-center py-12">
              <Loader2 size={24} className="animate-spin text-gray-500" />
            </div>
          ) : tasks.length === 0 ? (
            <div className="text-center py-12 text-gray-500 text-sm">
              暂无任务，点击「新建任务」开始
            </div>
          ) : (
            <table className="w-full min-w-[640px] text-sm">
              <thead>
                <tr className="border-b border-white/10">
                  <th className="text-left px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wider">名称</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wider">类型</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wider">分类</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wider">状态</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wider w-56">进度</th>
                  <th className="text-right px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wider">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/5">
                {tasks.map((task) => {
                  const progress = Number(task.progress_pct || 0);
                  const dbCount = task.state?.db_count ?? null;
                  const totalCount = task.state?.total_count ?? null;
                  const isRunning = task.status === 'running';
                  const isStopped = task.status === 'stopped';
                  const isCompleted = task.status === 'completed';

                  return (
                    <tr
                      key={task.id}
                      className="group hover:bg-white/[0.04] transition-colors duration-100"
                    >
                      <td className="px-4 py-3.5 font-medium text-white">
                        {task.name}
                        {task.error_message && (
                          <p className="text-xs text-rose-400 mt-0.5 truncate max-w-[200px]"
                            title={task.error_message}>
                            {task.error_message}
                          </p>
                        )}
                      </td>
                      <td className="px-4 py-3.5">
                        <span className="text-xs font-mono text-gray-400 bg-white/5 px-2 py-0.5 rounded">
                          {task.type}
                        </span>
                      </td>
                      <td className="px-4 py-3.5 text-gray-300">{task.category}</td>
                      <td className="px-4 py-3.5">
                        <StatusBadge status={task.status} />
                      </td>
                      <td className="px-4 py-3.5">
                        <GradientProgressBar
                          progress={progress}
                          dbCount={dbCount}
                          totalCount={totalCount}
                        />
                      </td>
                      <td className="px-4 py-3.5">
                        <div className="flex items-center justify-end gap-1.5">
                          {/* Start */}
                          <button
                            title="Start"
                            disabled={busy || isRunning || isCompleted}
                            onClick={() => runAction(() => startTask(task.id))}
                            className="p-1.5 rounded-lg text-emerald-400 hover:bg-emerald-500/20 transition-all
                                       disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                          >
                            <Play size={15} />
                          </button>
                          {/* Stop */}
                          <button
                            title="Stop"
                            disabled={busy || isStopped || isCompleted}
                            onClick={() => runAction(() => stopTask(task.id))}
                            className="p-1.5 rounded-lg text-yellow-400 hover:bg-yellow-500/20 transition-all
                                       disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                          >
                            <Square size={15} />
                          </button>
                          {/* Delete */}
                          <button
                            title="Delete"
                            disabled={busy}
                            onClick={() => runAction(() => deleteTask(task.id))}
                            className="p-1.5 rounded-lg text-rose-400 hover:bg-rose-500/20 transition-all
                                       disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent"
                          >
                            <Trash2 size={15} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {/* Create Task Modal */}
      <CreateTaskModal
        open={openCreate}
        onClose={() => setOpenCreate(false)}
        onCreated={refresh}
      />
    </div>
  );
}
