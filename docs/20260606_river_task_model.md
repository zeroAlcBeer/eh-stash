# River 接管后的任务模型

> `sync_task_defs` 是"同步定义"，`river_job` 是"一次执行"。Incremental 一轮被切成多个 slice job，由 `run_id` 串成 chain；periodic kick 只决定开新一轮 / 跳过当前轮，不亲自干活。

## 1. 两层模型

```
Task Definition (sync_task_defs)  ───►  River Job (river_job)
  稳定存在                                每次执行一条
  描述 source / strategy / scope          River 管 state / 重试 / 取消
  保存 checkpoint 和 progress
```

| 层 | 代表字段 | 职责 | 生命周期 |
|---|---|---|---|
| 定义层 | `task_kind`, `source`, `strategy`, `scope`, `checkpoint`, `progress` | 说明"要同步什么"和"同步到哪里"。包括 incremental 一轮的 `run_id` / `next_gid` / `scanned_count`。 | 长期存在，用户在 Admin 中管理。 |
| 调度层 | `schedule_kind`, `schedule_interval_sec`, `enabled`, `requested_action` | 说明是 admin 手动 enqueue，还是 periodic 自动 enqueue。`requested_action` 是用户操作未消费时的临时槽位（start / stop / retry）。 | 随定义变化。 |
| 执行层 | `river_job.kind`, `state`, `attempt`, `max_attempts`, `args`, `errors` | River 原生 job 执行状态。Admin 直接展示。 | 每次执行一条 job，完成 / 取消 / 失败后成为历史。 |

Job kind 共 4 种：

- `ehstash_full_sync`
- `ehstash_incremental_sync`（kick / 路由器）
- `ehstash_incremental_slice`（每片实干）
- `ehstash_favorites_sync`

## 2. River 原生状态如何出现在 Admin

| state | 含义 |
|---|---|
| `available` | 可被 worker 取走 |
| `scheduled` | 定时等待中 |
| `running` | 正在执行 |
| `retryable` | 失败后等待重试 |
| `completed` | 执行成功 |
| `cancelled` | 被取消 |
| `discarded` | 重试耗尽 |

旧 UI 里曾经有 `starting` / `stopping` / `queued` 这类自造中间态，现在主状态来自 `river_job.state`。如果用户操作尚未被 scheduler 消费，则额外显示 `requested_action`，它不是 job state。

## 3. 当前实际任务定义

| 定义 | task_kind | source | strategy | schedule |
|---|---|---|---|---|
| Gallery Full Sync | gallery_sync | gallery_list | full | manual |
| Gallery Incremental Sync（Mixed-incre） | gallery_sync | gallery_list | incremental | periodic |
| Favorites Source Sync | favorites_sync | favorites | full | periodic |

Mixed-incre 的 `sync_task_defs` 行：

```
task_kind = gallery_sync
source    = gallery_list
strategy  = incremental
scope     = {"categories": ["Doujinshi", "Manga", "Cosplay"]}
schedule  = periodic, every 30s (RunOnStart=true)
checkpoint = {
  run_id        : "<UnixNano>"   // chain identity for one round
  next_gid      : "<cursor>"     // EH list pagination cursor
  scanned_count : <int>          // items processed this round
  latest_gid    : <int>          // max gid seen in round
  round         : <int>          // completed rounds counter
}
progress = {"pct": <float>}      // 0..100, scanned_count / scan_window
```

## 4. Incremental 的"分片"模型

一轮 incremental 不是一个长 job，而是用 `run_id` 串起来的一串短 River job：kick 决定开不开新一轮、slice 干活、slice 自己 chain 下一 slice。每片只处理一个 EH 列表页 + 该页中所有 new / 需 refresh 的 gallery 的 detail fetch。

### Kick (`ehstash_incremental_sync`)

- periodic 每 30s 触发一次（`RunOnStart=true`，启动时也立刻触发一次）。
- 路由器：检查 `checkpoint.run_id` + `current_job_id`：
  - **chain in flight**：早返回，什么也不做。
  - **无 chain**：生成新 `run_id = strconv.FormatInt(UnixNano)`，写回 checkpoint，enqueue 第一片 slice。
- `activeUniqueInsertOpts`：按 args 在非 terminal state 内去重，所以 slice 跑期间 periodic 投同 args 的 kick 直接被 River 拒绝，不会堆 no-op kick row。
- Timeout 1 min（kick 是路由器，本应极短）。

### Slice (`ehstash_incremental_slice`)

- args = `{task_id, run_id (string)}`。cursor 从 checkpoint 的 `next_gid` 读取，不在 args 里。
- 入口检查 `checkpoint.run_id != args.run_id` → self-drop（stale chain）。
- 抓一页列表 → 解析 25 个 item → 对每个 new / needs-refresh 的 item 抓 detail page → bulk upsert → 写回 checkpoint。
- 结尾根据 result 决定：`END` / `WINDOW` / `BANNED` / `ERROR` 或继续 chain 下一片 slice（args 复用同一 `run_id`）。
- Timeout 30 min（足够 cold-start 单页 25 个 item 全 detail-fetch 一遍）。
- `SliceMaxAttempts=2`：失败重试一次，节制 retry 成本。

### 退出原因

| 退出 | 条件 | 后续动作 |
|---|---|---|
| `END` | 列表页解析后无 `next_cursor`，或拿到空 items。 | 本轮完成。重置 `next_gid` / `scanned_count` / `latest_gid`，`round += 1`，`run_id = null`。下次 periodic 开新一轮。 |
| `WINDOW` | `scanned_count >= scan_window`（默认 10000）。 | 同 `END`：本轮结束、等下次 periodic。 |
| `BANNED` | 列表或 detail fetch 返回 banned。 | 暂停本轮（`run_id = null`），下次 periodic 触发后开新一轮。`next_gid` 保留以便手动续。 |
| `ERROR` | 列表 fetch / parse 失败。 | 同 `BANNED`。 |
| (continue) | 有 next_cursor 且未到 window。 | 写 `checkpoint.next_gid`，enqueue 下一片 slice（同 `run_id`）。 |
| ctx cancel | scraper 重启 / 用户 stop / River timeout。 | 清 `run_id`，`MarkTaskDefFinished(... "cancelled")`。下次 periodic 开新一轮。 |

## 5. 实际执行流

```
periodic kick (每 30s)
       │
       ▼
┌──────────────────────┐
│ ehstash_incremental_ │  chain in flight ──► skip (no-op)
│ sync (kick)          │  否则 ──► 生成 run_id，enqueue 第一片 slice
└──────────────────────┘
       │
       ▼
┌──────────────────────┐
│ ehstash_incremental_ │  读 checkpoint.next_gid
│ slice                │  抓列表页 + 每个 new item 的 detail
│                      │  bulk upsert，写回 checkpoint
└──────────────────────┘
       │
       ▼
未到 window 且有 next cursor → enqueue 下一片 slice（同 run_id）
否则 → 重置状态，本轮结束
```

伪码：

```go
// schema
IncrementalSyncArgs  { TaskID int }                      // kick, also river:"unique" by args
IncrementalSliceArgs { TaskID int; RunID string }        // run_id is string to avoid float64 precision loss

// kick worker
def := GetTaskDef(taskID)
if checkpoint.run_id != "" && current_job_id != nil:
    return                                               // chain in flight, no-op
newRunID := UnixNano()
checkpoint.run_id        = newRunID
checkpoint.next_gid      = nil
checkpoint.scanned_count = 0
checkpoint.latest_gid    = nil
UpdateTaskDefCheckpoint(...)
Insert(IncrementalSliceArgs{taskID, newRunID})

// slice worker
def := GetTaskDef(taskID)
if checkpoint.run_id != args.run_id:
    return                                               // stale chain, self-drop
result := RunIncrementalSlice(ctx, def)                  // 1 list page + per-new-item detail fetches
switch result.ExitReason:
  case "END", "WINDOW":
    reset round state; round += 1; MarkTaskDefFinished
  case "BANNED", "ERROR":
    checkpoint.run_id = nil; MarkTaskDefFinished
  default:
    checkpoint.next_gid = result.NextCursor
    UpdateTaskDefCheckpoint(...)
    Insert(IncrementalSliceArgs{taskID, args.run_id})    // chain next slice
```

## 6. Timeout / attempts / 调优参数

| 常量 | 值 | 位置 | 含义 |
|---|---|---|---|
| `ManagerPollInterval` | 5 s | scheduler manager loop | 查 `requested_action` + 清理 terminal job 的 `current_job_id`。 |
| `IncrementalInterval` | 30 s | periodic kick 默认间隔 | 可被 `sync_task_defs.schedule_interval_sec` 覆盖。 |
| `SyncJobTimeout` | 30 min | full / favorites worker | 整轮长任务的 River timeout。 |
| `SliceJobTimeout` | 30 min | incremental slice worker | 单片预算。`RATE_INTERVAL=10s` 时 cold-start 单页 25 个 item 全 detail-fetch 约 4-5 min，留 6x 余量。 |
| `KickJobTimeout` | 1 min | kick worker | kick 只是查 def 决定要不要开新轮，不该超过几百 ms。 |
| `SliceMaxAttempts` | 2 | slice `InsertOpts` | 失败重试一次即放弃。kicks / full / favorites 用 River 默认的 3 次。 |

## 7. 为什么这适合 River

- **River 擅长管理短、可重试的 job**：River 的核心价值是 job lifecycle（排队 / 运行 / 失败 / 重试 / 取消 / discard）。slice 单位越小，River 状态越准确，retry 成本越低。
- **Admin 直接展示真实状态**：UI 显示当前 River job 的原生 `state` / `attempt` / `errors`；定义层显示 `checkpoint` + `progress`。不需要发明中间态。

## 8. 观测

scheduler 决策点和 slice 内 per-item 循环都有结构化 log：

- `[SCHED]` — periodic 注册 / requested action / enqueue / cancel / retry
- `[INCR ]` — kick entry / skip / new round / slice entry / list fetch / item decision + latency / page summary / chain
- `[FULL ]` — full sync entry / done / ctx cancel
- `[FAV  ]` — favorites entry / disabled / finished

如果哪里看不到进度，第一步先看 log 而不是猜。
