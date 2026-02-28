# Sync Tasks 架构方案

> 将 scraper 的硬编码配置改为后台可管理的同步任务系统。

## 1. 目标

| # | 目标 | 现状 |
|---|------|------|
| 1 | 同步任务通过 Admin UI 配置，非硬编码 | `loop.py` 写死 `CATEGORIES = ['Manga', 'Doujinshi', 'Cosplay']` |
| 2 | 初始化后无任何同步任务 | 启动即跑 6 个 job |
| 3 | 手动创建任务，支持全量 / 增量，暴露可调参数 | 参数全在 `.env`，启动时读一次 |
| 4 | 查看任务列表和同步进度（GID 百分比） | `schedule_state.state` 只存游标 |
| 5 | 手动启动、停止、删除 | 无控制通道 |
| 6 | 缩略图改为队列驱动（insert/upsert 入队），取代磁盘扫描 diff | `run_thumb_loop` 每轮 `iterdir()` 全量 diff |

## 2. 架构决策

### 2.1 语言：Python

瓶颈是目标站点 rate limit（1s/请求），非计算密度。Go 重写无可观测收益，增加维护成本。

### 2.2 协调机制：PostgreSQL（零新依赖）

- Worker 轮询 `sync_tasks` 表获取任务定义和控制信号
- 缩略图使用 `thumb_queue` 表做 FIFO 队列
- 不引入 Redis / Celery / RabbitMQ
- `SELECT ... FOR UPDATE SKIP LOCKED` 提供原子任务获取

### 2.3 部署拓扑：不变

```
postgres ← scraper(worker)
         ← api(FastAPI) ← frontend(React)
```

docker-compose 无变更。scraper 容器职责从"硬编码循环"变为"DB 驱动调度器"。

## 3. DB Schema 变更

新建 migration 文件 `migrations/002_sync_tasks.sql`：

```sql
-- 同步任务表（替代 schedule_state 的角色）
CREATE TABLE IF NOT EXISTS sync_tasks (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    type            TEXT NOT NULL CHECK (type IN ('full', 'incremental')),
    category        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'stopped'
                    CHECK (status IN ('stopped', 'running', 'completed', 'error')),

    -- 用户可调参数（Admin UI 编辑）
    config          JSONB NOT NULL DEFAULT '{}',

    -- Worker 内部运行状态（Worker 读写，API 只读）
    state           JSONB NOT NULL DEFAULT '{}',

    -- 进度百分比 0.0 ~ 100.0
    progress_pct    REAL DEFAULT 0,

    -- 控制信号：API 写，Worker 读
    desired_status  TEXT NOT NULL DEFAULT 'stopped'
                    CHECK (desired_status IN ('running', 'stopped')),

    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    last_run_at     TIMESTAMPTZ,
    error_message   TEXT
);

-- 缩略图队列表（替代磁盘扫描 diff）
CREATE TABLE IF NOT EXISTS thumb_queue (
    id              SERIAL PRIMARY KEY,
    gid             BIGINT NOT NULL UNIQUE,
    thumb_url       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    retry_count     INT DEFAULT 0,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    processed_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_thumb_queue_pending
    ON thumb_queue (created_at) WHERE status = 'pending';
```

> `schedule_state` 表保留不删，但不再被新代码使用。未来可用迁移脚本搬历史数据。

### 3.1 config 字段规格

**full 类型**：

```jsonc
{
  "rate_interval": 1.0,       // 请求间隔秒数
  "inline_set": "dm_e",       // ExHentai 列表展示模式
  "start_gid": null            // 可选，起始 GID（从此 GID 向旧方向遍历）
}
```

**incremental 类型**：

```jsonc
{
  "rate_interval": 1.0,
  "inline_set": "dm_e",
  "categories": ["Doujinshi", "Manga", "Cosplay"], // 混合抓取分类（按站点活跃度自然分配）

  "scan_window": 10000,        // 每轮扫描的 item 条数
  "rating_diff_threshold": 0.5 // 粗粒度评分变化阈值
}
```

### 3.2 state 字段规格

**full 类型**：

```jsonc
{
  "next_gid": null,            // 当前游标（null = 从最新页开始）
  "round": 0,                  // 已完成轮次
  "done": false,               // 全量遍历是否结束
  "anchor_gid": null           // 首页取到的最大 GID，用于计算进度
}
```

**incremental 类型**：

```jsonc
{
  "next_gid": null,
  "round": 0,
  "latest_gid": null,
  "scanned_count": 0
}
```

## 4. Worker 重构（scraper 容器）

### 4.1 核心循环

```
Worker 启动
  ├─ 读取环境变量（仅 site-level：cookies, headers, base_url）
  ├─ validate_access()
  ├─ spawn thumb_worker 协程
  └─ 主调度循环 (每 3s 一轮):
       ├─ SELECT * FROM sync_tasks
       ├─ 对比 desired_status vs 内存中的 asyncio.Task 映射:
       │    ├─ desired='running' 且无活跃 task → 创建 asyncio.Task
       │    ├─ desired='stopped' 且有活跃 task → task.cancel()
       │    └─ DB 中行被 DELETE → cancel + 从映射中移除
       └─ 对每个活跃 task，更新 status / progress_pct / last_run_at
```

### 4.2 任务执行逻辑

每个 task 的执行体 = 现有 `_run_scraper_job` / `_run_callback_job` 的逻辑，但：

1. **参数来源改为 DB**：从 `sync_tasks.config` 读取，非 env
2. **响应停止信号**：每处理一页后重查 `desired_status`
3. **更新进度**：每页完成后更新 `progress_pct`
4. **缩略图入队**：insert/upsert 成功后向 `thumb_queue` 写入

### 4.3 Config 热更新规则

用户可在任务运行中通过 Admin API 修改 config，按「自然边界」生效：

| 参数 | 生效时机 | 说明 |
|------|---------|------|
| `rate_interval` | 下一次 `asyncio.sleep` | 逐请求参数，每页边界重载 |
| `inline_set` | 下一次列表页请求 | 逐请求参数 |
| `rating_diff_threshold` | 下一次比较判断 | 逐请求参数 |

| `scan_window` | 下一个 cycle | 逐轮参数，当前 cycle 按旧值跑完 |
| `start_gid` | 不可热更新 | 只在创建时生效，运行中改无语义 |

**实现方式**：每页开始时执行一次 `SELECT config, desired_status FROM sync_tasks WHERE id = %s`，解构到局部变量。逐轮参数在 turn/cycle 开始时绑定一次。

### 4.4 进度计算

| 任务类型 | 算法 |
|---------|------|
| full | 首页请求后记录 `anchor_gid`（最大 GID）。`progress = (anchor_gid - current_cursor) / anchor_gid × 100`。到末页或无内容时 100%。 |
| incremental | `scanned_count / scan_window × 100`。轮次完成后进度重置为 0（循环周期）。 |

### 4.5 任务创建时的初始 state

```python
def init_state(task_type: str, config: dict) -> dict:
    if task_type == 'full':
        return {
            "next_gid": config.get("start_gid"),  # None = 从最新页
            "round": 0,
            "done": False,
            "anchor_gid": None,
        }
    else:  # incremental
        return {
            "next_gid": None,
            "round": 0,
            "latest_gid": None,
            "scanned_count": 0,
        }
```

## 5. 缩略图队列

### 5.1 入队

在 `upsert_galleries_bulk` 成功后，对每条有 `thumb` 字段的记录执行：

```sql
INSERT INTO thumb_queue (gid, thumb_url)
VALUES (%s, %s)
ON CONFLICT (gid) DO UPDATE SET
    thumb_url = EXCLUDED.thumb_url,
    status = 'pending',
    retry_count = 0
WHERE thumb_queue.thumb_url != EXCLUDED.thumb_url
   OR thumb_queue.status = 'failed';
```

### 5.2 消费（thumb_worker 协程）

```sql
-- 原子取任务
UPDATE thumb_queue SET status = 'processing'
WHERE id = (
    SELECT id FROM thumb_queue
    WHERE status = 'pending'
    ORDER BY created_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING id, gid, thumb_url;
```

- 下载成功 → `status = 'done'`, `processed_at = NOW()`
- 下载失败 → `retry_count += 1`
- `retry_count >= 3` → `status = 'failed'`
- 队列为空时 `sleep 5s`

### 5.3 thumb_worker 的 rate limit

使用 `config.THUMB_RATE_INTERVAL`（env），与同步任务的 rate 独立。

## 6. API 新增路由

新文件 `api/routers/admin.py`，挂载前缀 `/v1/admin`。

### 6.1 任务管理

```
POST   /v1/admin/tasks              创建任务
GET    /v1/admin/tasks              任务列表
GET    /v1/admin/tasks/{id}         任务详情
PATCH  /v1/admin/tasks/{id}         修改 config / name
POST   /v1/admin/tasks/{id}/start   设置 desired_status = 'running'
POST   /v1/admin/tasks/{id}/stop    设置 desired_status = 'stopped'
DELETE /v1/admin/tasks/{id}         删除任务（worker 会 cancel 对应 task）
```

### 6.2 缩略图队列

```
GET    /v1/admin/thumb-queue/stats  队列统计 {pending, processing, done, failed}
```

### 6.3 请求/响应示例

**POST /v1/admin/tasks**

```json
// Request
{
  "name": "manga-full-sync",
  "type": "full",
  "category": "Manga",
  "config": {
    "rate_interval": 1.0,
    "inline_set": "dm_e",
    "start_gid": 3000000
  }
}

// Request (incremental mixed)
{
  "name": "mixed-incremental",
  "type": "incremental",
  "category": "Mixed",
  "config": {
    "categories": ["Doujinshi", "Manga", "Cosplay"],
    "scan_window": 10000,
    "rating_diff_threshold": 0.5
  }
}

// Response 201
{
  "id": 1,
  "name": "manga-full-sync",
  "type": "full",
  "category": "Manga",
  "status": "stopped",
  "desired_status": "stopped",
  "config": {"rate_interval": 1.0, "inline_set": "dm_e", "start_gid": 3000000},
  "state": {"next_gid": 3000000, "round": 0, "done": false, "anchor_gid": null},
  "progress_pct": 0,
  "created_at": "...",
  "updated_at": "...",
  "last_run_at": null,
  "error_message": null
}
```

**GET /v1/admin/tasks**

```json
// Response 200
[
  {
    "id": 1,
    "name": "manga-full-sync",
    "type": "full",
    "category": "Manga",
    "status": "running",
    "progress_pct": 34.5,
    "desired_status": "running",
    "config": {...},
    "state": {...},
    "created_at": "...",
    "updated_at": "...",
    "last_run_at": "...",
    "error_message": null
  }
]
```

**PATCH /v1/admin/tasks/{id}**

```json
// Request（部分更新）
{
  "config": {"rate_interval": 2.0}
}

// Response 200: 完整 task 对象
```

> PATCH config 为合并语义（shallow merge），非替换。

**POST /v1/admin/tasks/{id}/start**

```json
// Response 200
{"id": 1, "desired_status": "running"}
```

**POST /v1/admin/tasks/{id}/stop**

```json
// Response 200
{"id": 1, "desired_status": "stopped"}
```

**GET /v1/admin/thumb-queue/stats**

```json
// Response 200
{
  "pending": 1234,
  "processing": 1,
  "done": 56789,
  "failed": 12
}
```

### 6.4 Pydantic Models

```python
# api/models.py 新增

class SyncTaskCreate(BaseModel):
    name: str
    type: Literal['full', 'incremental']
    category: str
    config: dict = {}

class SyncTaskUpdate(BaseModel):
    name: Optional[str] = None
    config: Optional[dict] = None

class SyncTask(BaseModel):
    id: int
    name: str
    type: str
    category: str
    status: str
    desired_status: str
    config: dict
    state: dict
    progress_pct: float
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None
    last_run_at: Optional[datetime] = None
    error_message: Optional[str] = None

class ThumbQueueStats(BaseModel):
    pending: int
    processing: int
    done: int
    failed: int
```

## 7. 前端 Admin 页面

### 7.1 路由

`App.jsx` 加顶部导航 "Admin" 入口。使用 React Router：

- `/` → `GalleryPage`（现有）
- `/admin` → `AdminPage`（新增）

### 7.2 AdminPage 组件结构

```
AdminPage
  ├─ ThumbQueueCard          缩略图队列状态卡片
  │    └─ pending / processing / done / failed 四个数字
  ├─ TaskTable               任务列表表格
  │    └─ 列: name | type | category | status | progress(进度条) | 操作
  │         操作: ▶ Start | ⏹ Stop | 🗑 Delete
  └─ CreateTaskDialog        新建任务对话框
       ├─ name (text)
       ├─ type (select: full / incremental)
       ├─ category (text)
       └─ config 参数表单（根据 type 动态展示对应字段）
```

### 7.3 API 调用

新建 `frontend/src/api/admin.js`：

```javascript
const BASE = '/api/v1/admin';

export const getTasks = () => fetch(`${BASE}/tasks`).then(r => r.json());
export const createTask = (data) => fetch(`${BASE}/tasks`, { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data) }).then(r => r.json());
export const updateTask = (id, data) => fetch(`${BASE}/tasks/${id}`, { method: 'PATCH', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data) }).then(r => r.json());
export const startTask = (id) => fetch(`${BASE}/tasks/${id}/start`, { method: 'POST' }).then(r => r.json());
export const stopTask = (id) => fetch(`${BASE}/tasks/${id}/stop`, { method: 'POST' }).then(r => r.json());
export const deleteTask = (id) => fetch(`${BASE}/tasks/${id}`, { method: 'DELETE' });
export const getThumbStats = () => fetch(`${BASE}/thumb-queue/stats`).then(r => r.json());
```

### 7.4 轮询

任务列表页 5s 轮询 `GET /v1/admin/tasks` 刷新 status 和 progress。

## 8. 文件变更清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `migrations/002_sync_tasks.sql` | **新增** | `sync_tasks` + `thumb_queue` 表 |
| `scraper/config.py` | **修改** | 移除任务级参数（CALLBACK_*），只保留 site-level（cookies, headers, base_url, THUMB_RATE_INTERVAL） |
| `scraper/loop.py` | **重构** | 硬编码循环 → DB 驱动调度器；`run_thumb_loop` → 队列消费 |
| `scraper/db.py` | **修改** | 新增 sync_tasks 读写 + thumb_queue 入队/消费函数 |
| `api/routers/admin.py` | **新增** | Admin CRUD + 启停 API |
| `api/models.py` | **修改** | 新增 SyncTask / ThumbQueueStats models |
| `api/main.py` | **修改** | 挂载 admin router |
| `frontend/src/pages/AdminPage.jsx` | **新增** | Admin 管理页面 |
| `frontend/src/api/admin.js` | **新增** | Admin API 调用 |
| `frontend/src/App.jsx` | **修改** | 添加路由和 Admin 导航入口 |
| `frontend/package.json` | **修改** | 添加 react-router-dom 依赖 |

### 不变的文件

- `scraper/parser.py` — 解析逻辑不变
- `scraper/logic.py` — 决策函数不变
- `api/routers/galleries.py` — 现有 gallery API 不变
- `api/routers/stats.py` — 现有 stats API 不变
- `docker-compose.yaml` — 容器拓扑不变
- `migrations/001_schema.sql` — 不修改

## 9. 实施阶段

| 阶段 | 内容 | 验证方式 |
|------|------|---------|
| **P1** | `migrations/002_sync_tasks.sql` | `\d sync_tasks` + `\d thumb_queue` |
| **P2** | `api/routers/admin.py` + models + 挂载 | curl 各端点 |
| **P3** | `scraper/loop.py` 重构 + `scraper/db.py` 新函数 + `scraper/config.py` 精简 | 创建任务 → start → 观察日志 + DB 状态 |
| **P4** | 前端 `AdminPage` + 路由 + API 调用 | 浏览器操作 |
| **P5**（可选） | 迁移 `schedule_state` 历史数据到 `sync_tasks` | SQL 脚本 |

每阶段可独立验证和回滚。

## 10. 边界与约束

1. **full 任务**按单分类创建；同一分类可创建多个 full 任务（需自行避免重复抓取）。
2. **incremental 任务全局仅允许一个**（不区分运行状态）。创建第二个 incremental 会返回 `409`。
3. **full 任务完成后** `status = 'completed'`，不自动重启。如需重跑，手动 start 或新建任务。
4. **incremental 任务**是永续循环，每轮重置进度。`status` 始终为 `running`，除非手动 stop 或异常 error。
5. **删除任务**是硬删除（`DELETE FROM sync_tasks`）。如需审计日志，未来再加 soft delete。
6. **thumb_queue** 中 `status = 'done'` 的行可定期清理（如 `DELETE WHERE status = 'done' AND processed_at < NOW() - INTERVAL '7 days'`），但不是 MVP 必须。
