# EH-Stash 架构方案

（这是项目初期的 Prompt 架构已经有变动）

## 概述

新建独立项目 `eh-stash`，四个 Docker 服务（postgres + scraper + api + frontend），技术栈与 konakore 一致（Python FastAPI + React + PostgreSQL）。

Scraper 以 **endless-loop 单线程 round-robin** 模式抓取 exhentai.org 的 Manga / Doujinshi / Cosplay 三个分类的全量历史画廊元数据，全局严格维持 1 req/s；API 提供分页/过滤/统计接口；Frontend 提供卡片式浏览页，点击跳转 EX 原站。

**不做本地文件下载，不做自动登录，Cookie 手动维护写入 `.env`。**

---

## 服务架构

```
docker-compose.yaml
├── postgres        （持久化存储，PostgreSQL 16)
├── scraper         (Python, endless-loop 单线程 round-robin 抓取器）
├── api             (Python FastAPI, REST 接口）
└── frontend        (React + Vite, 浏览 WebUI)
```

---

## 项目文件结构

```
eh-stash/
├── docker-compose.yaml
├── .env.example
├── migrations/
│   └── 001_schema.sql
├── scraper/
│   ├── Dockerfile
│   ├── requirements.txt
│   ├── main.py        # endless loop 入口
│   ├── config.py
│   ├── db.py
│   ├── parser.py      # parse_gallery_list() + parse_detail()
│   └── loop.py        # 核心 round-robin 循环（单文件，无需拆分）
├── api/
│   ├── Dockerfile
│   ├── requirements.txt
│   ├── main.py
│   ├── db.py
│   ├── models.py
│   └── routers/
│       ├── galleries.py
│       └── stats.py
└── frontend/
    ├── Dockerfile
    ├── package.json
    ├── vite.config.js
    └── src/
        ├── main.jsx
        ├── App.jsx
        ├── api/
        │   └── index.js
        ├── pages/
        │   └── GalleryPage.jsx
        └── components/
            ├── GalleryCard.jsx
            ├── FilterPanel.jsx
            └── TagBadge.jsx
```

---

## 数据库 Schema

### `eh_galleries`（核心表）

```sql
CREATE TABLE eh_galleries (
    gid             BIGINT PRIMARY KEY,
    token           TEXT NOT NULL,
    category        TEXT,                        -- 'Manga' | 'Doujinshi' | 'Cosplay'
    title           TEXT,
    title_jpn       TEXT,
    uploader        TEXT,
    posted_at       TIMESTAMPTZ,
    language        TEXT,
    pages           INT,
    rating          NUMERIC(3, 2),
    fav_count       INT DEFAULT 0,
    thumb           TEXT,                        -- 封面 CDN URL
    tags            JSONB,                       -- {"female": ["schoolgirl"], "language": ["chinese"]}
    last_synced_at  TIMESTAMPTZ,                 -- NULL = 从未抓过 detail；非 NULL = 最后同步时间
    is_active       BOOLEAN DEFAULT TRUE         -- 软删除（画廊被删时标记 false）
);

-- 索引
CREATE INDEX idx_eh_galleries_category      ON eh_galleries (category);
CREATE INDEX idx_eh_galleries_fav_count     ON eh_galleries (fav_count DESC);
CREATE INDEX idx_eh_galleries_rating        ON eh_galleries (rating DESC);
CREATE INDEX idx_eh_galleries_posted_at     ON eh_galleries (posted_at DESC);
CREATE INDEX idx_eh_galleries_language      ON eh_galleries (language);
CREATE INDEX idx_eh_galleries_tags          ON eh_galleries USING GIN (tags);  -- 支持 @> 查询
```

### `schedule_state`（抓取进度，支持断点续传）

```sql
CREATE TABLE schedule_state (
    job_name    TEXT PRIMARY KEY,
    state       JSONB,
    last_run_at TIMESTAMPTZ
);

-- 预置任务行（3 个分类各一行，list + detail 进度统一存储）
INSERT INTO schedule_state (job_name, state) VALUES
    ('scraper-manga',     '{"current_page": 0, "pages_done": 0, "galleries_inserted": 0}'),
    ('scraper-doujinshi', '{"current_page": 0, "pages_done": 0, "galleries_inserted": 0}'),
    ('scraper-cosplay',   '{"current_page": 0, "pages_done": 0, "galleries_inserted": 0}');
```

---

## Scraper 服务

### 文件说明

| 文件 | 职责 |
|------|------|
| `main.py` | 进程入口，启动 endless loop，处理信号退出 |
| `config.py` | 读取 `.env`（`EX_COOKIES`, `EX_BASE_URL`, `RATE_INTERVAL`） |
| `db.py` | psycopg2 连接池 |
| `parser.py` | `parse_gallery_list()` + `parse_detail()`（改写自 `eh_demo.py`） |
| `loop.py` | 核心 endless-loop round-robin 逻辑（单文件） |

### 抓取策略：Endless-Loop Round-Robin

**设计原则**：单线程，list 抓取与 detail 抓取不分离，全局严格 1 req/s，无独立调度器。每轮循环开始时处于 active zone，由数据变化信号动态切换至 inactive zone。

**伪代码**：

```python
CATEGORIES = ['Manga', 'Doujinshi', 'Cosplay']
INACTIVE_THRESHOLD = 3   # 页内连续无变化即进入 inactive zone

while True:
    active = True                                 # 每轮重置为 active

    for category in CATEGORIES:
        page = load_state(category).current_page
        gids = fetch_list_page(category, page)    # 1 req，始终执行
        sleep(RATE_INTERVAL)

        if not gids:
            save_state(category, current_page=0)  # 到达末页，从头重新循环
            active = True                          # 重置 active 状态
            continue

        consecutive_no_change = 0                 # 每页重置计数

        for gid, token in gids:
            if not exists(gid):
                # 新 gid：必抓 detail，入库
                detail = fetch_detail(gid, token)
                insert_gallery(gid, token, detail)
                sleep(RATE_INTERVAL)
                consecutive_no_change = 0         # 有新内容，重置计数

            elif active:
                # 已存在 + active zone：抓 detail 检测变化
                detail = fetch_detail(gid, token)
                sleep(RATE_INTERVAL)
                if changed(detail, stored[gid]):
                    update_gallery(gid, detail)
                    consecutive_no_change = 0
                else:
                    consecutive_no_change += 1
                    if consecutive_no_change >= INACTIVE_THRESHOLD:
                        active = False             # 切入 inactive zone，break 当前页
                        break

            else:
                pass                              # inactive zone：跳过，0 req

        save_state(category, current_page=page + 1)
```

**active / inactive zone 转换逻辑**：

- `active = True`：每轮 while 开始时重置，每次分类到达末页（页码归零）时重置
- → `active = False`：页内连续 3 个已存在 gid 均无变化，切入 inactive zone
- inactive zone 后：当页剩余 gid 全跳过，后续页的已存在 gid 也全跳过；只有新 gid（`last_synced_at IS NULL`）仍会抓 detail

**两种 gid 身份**：

| 遇到的 gid | active zone | inactive zone |
|-----------|-------------|---------------|
| 不存在（新画廊） | 抓 detail + INSERT | 抓 detail + INSERT（始终执行）|
| 已存在 | 抓 detail，检测变化 | 跳过，0 req |

**执行时序示例**（每行间隔恰好 1s）：
```
# 历史首圈（大量新 gid）
t=0    [LIST]   Manga     page=0  → 25 gids
t=1    [DETAIL] Manga     gid=111111  +INSERT  fav=234
t=2    [DETAIL] Manga     gid=111112  +INSERT  fav=89
...
t=25   [DETAIL] Manga     gid=111135  +INSERT  → page=0 完成

# 历史全部入库后（循环进入同步模式）
t=0    [LIST]   Manga     page=0  → 25 gids  (active=True)
t=1    [DETAIL] Manga     gid=111111  changed=True  fav 234→251  (no_change=0)
t=2    [DETAIL] Manga     gid=111112  changed=False             (no_change=1)
t=3    [DETAIL] Manga     gid=111113  changed=False             (no_change=2)
t=4    [DETAIL] Manga     gid=111114  changed=False             (no_change=3 → inactive!)
       [SKIP]   Manga     gid=111115..111135  (inactive zone)
t=5    [LIST]   Doujinshi page=0  → 25 gids  (active 不重置，仍 False)
       [SKIP]   Doujinshi gid=200001..200025
t=6    [LIST]   Cosplay   page=0  → 25 gids
       ...
# 下一轮 while 开始：active = True 重置
```

**加速效应**：历史全部入库且内容稳定后，inactive zone 命中率越高，循环越快，每页只需 1s（list） + 最多 3 个 detail 请求，每轮 3 分类全扫时间大幅缩短。内容活跃时（有新 fav / 新 gid）自动退化为更多 detail 请求，无需人工干预。

**退避策略**：

| 状态码 | 含义 | 处理方式 |
|--------|------|---------|
| 429 | 速率限流 | 指数退避原地重试：`60s → 120s → 240s → 480s → 900s`（上限 15 分钟），不推进页码 |
| 509 | EX 带宽配额耗尽（EX 专属） | 固定等待 **3600s**，不做指数增长，等配额重置 |
| 403 | Cloudflare block | 固定等待 **7200s**，连续 3 次 403 → 写入 `schedule_state` 中 `"blocked": true`，需人工介入 |
| 5xx | 服务器错误 | 退避 `30s → 60s → 120s`，最多 3 次重试，仍失败则跳过当前 gid |
| 404 | 画廊被删 | 跳过该 gid，若已入库则标记 `is_active=false` |
| 网络超时 | 连接问题 | 等待 30s 重试 1 次，再超时则跳过 |

```python
def fetch_with_backoff(url, retries=0):
    try:
        resp = client.get(url, timeout=30)
    except TimeoutError:
        if retries < 1:
            sleep(30); return fetch_with_backoff(url, retries + 1)
        return None  # skip

    if resp.status_code == 200:
        return resp
    elif resp.status_code == 429:
        wait = min(60 * (2 ** retries), 900)
        sleep(wait); return fetch_with_backoff(url, retries + 1)
    elif resp.status_code == 509:
        sleep(3600); return fetch_with_backoff(url, 0)
    elif resp.status_code == 403:
        sleep(7200)
        if retries >= 2: mark_blocked(); return None
        return fetch_with_backoff(url, retries + 1)
    elif resp.status_code == 404:
        return None  # gallery deleted
    elif resp.status_code >= 500:
        if retries >= 3: return None  # skip
        sleep(30 * (2 ** retries)); return fetch_with_backoff(url, retries + 1)
```

**断点续传**：进程重启后从 `schedule_state.current_page` 继续，`ON CONFLICT DO NOTHING` 保证幂等。

### 限流风险分析

| 指标 | 数值 |
|------|------|
| 全局请求速率 | 1 req/s = 3,600 req/h = 86,400 req/day |
| 请求模式 | 完全均匀，无突发，无并发 |
| EX 社区报告安全线 | ~1–2 req/s 持续请求无封禁记录 |
| curl_cffi 加持 | Chrome TLS 指纹 + 登录 Cookie，EX 视为正常用户 |

**全量历史时间估算**（假设每分类约 5,000 页 × 25 条/页，共 375,000 条）：
- 每页耗时：(1 + 25) req × 1s = **26s**
- 单分类全量：5,000 × 26s ≈ **36 小时**
- 3 分类串行 round-robin：约 **4.5 天**
- 历史入库后同步模式：active zone 深度由实际变化量决定，最优情况（内容稳定）每页仅 1s + 最多 3 detail req；最差情况（大量更新）接近首圈速率

**结论**：1 req/s 均匀请求在 EX 已知限制线以内，无需进一步限速或随机 jitter。

### EX 访问配置

```
# .env
EX_COOKIES=ipb_member_id=YOUR_ID;ipb_pass_hash=YOUR_HASH;sk=YOUR_SK
EX_BASE_URL=https://exhentai.org
RATE_INTERVAL=1.0
INACTIVE_THRESHOLD=3        # 页内连续无变化次数阈值，超过则切入 inactive zone
DATABASE_URL=postgresql://user:pass@postgres:5432/eh_stash
```

EX Cookie 通常长期有效（数月），失效后手动更新 `.env` 重启 scraper 容器即可。

---

## API 服务

### 端点

| 方法 | 路径 | 核心参数 | 说明 |
|------|------|----------|------|
| GET | `/v1/galleries` | `category`, `language`, `min_rating`, `min_fav`, `tag`（`namespace:value`）, `sort`, `page`, `page_size` | 分页列表，服务端过滤 |
| GET | `/v1/galleries/{gid}` | — | 单条详情 |
| GET | `/v1/stats` | — | 三分类数量、`detail_fetched` 进度、最新同步时间 |

### 排序选项（`sort` 参数）

- `fav_count`（默认，降序）
- `rating`（降序）
- `posted_at`（降序，最新优先）

### Tag 过滤

通过 `?tag=female:schoolgirl` 参数查询，后端执行：
```sql
WHERE tags @> '{"female": ["schoolgirl"]}'::jsonb
```

---

## Frontend 服务

### 页面与组件

| 文件 | 功能 |
|------|------|
| `GalleryPage.jsx` | 主浏览页：筛选面板 + 卡片网格 + 分页 |
| `GalleryCard.jsx` | 封面 + 标题 + category badge + rating + fav_count，点击新标签打开 EX |
| `FilterPanel.jsx` | 分类多选 / 语言下拉 / 最低评分滑块 / 最低 fav 数 / tag 搜索 |
| `TagBadge.jsx` | 按 namespace 着色（female/male/parody/character/artist…） |

### 关键交互

- **点击卡片** → 新标签打开 `https://exhentai.org/g/{gid}/{token}/`
- **封面图** → 直接引用 EX CDN URL，浏览器需已登录 EX 才能加载；加载失败显示占位符（可接受的低频不完美）
- **默认排序** → `fav_count DESC`

### 技术栈

- React 18 + Vite
- TanStack Query（数据获取与缓存）
- MUI 或 Tailwind（UI 组件）
- 端口：5173（开发）/ Nginx（生产）

---

## docker-compose.yaml 要点

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: eh_stash
      POSTGRES_USER: ${DB_USER}
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./migrations:/docker-entrypoint-initdb.d

  scraper:
    build: ./scraper
    depends_on: [postgres]
    env_file: .env
    restart: unless-stopped

  api:
    build: ./api
    depends_on: [postgres]
    ports:
      - "3000:3000"
    env_file: .env
    restart: unless-stopped

  frontend:
    build: ./frontend
    depends_on: [api]
    ports:
      - "5173:80"
    restart: unless-stopped

volumes:
  postgres_data:
```

---

## 实施步骤

1. **初始化骨架** — 创建项目目录，编写 `docker-compose.yaml`、`.env.example`、`migrations/001_schema.sql`
2. **Scraper** — 实现 `parser.py`（从 `eh_demo.py` 提取）→ `loop.py`（endless round-robin 逻辑）→ `main.py`，单独运行验证一个分类的前 2 页抓取
3. **API** — DB 连接池 → Pydantic models → `galleries.py` router → `stats.py` router
4. **Frontend** — `GalleryCard` → `FilterPanel` → `GalleryPage`，接入 API
5. **联调** — `docker compose up`，验证一个分类前几页正常入库并在 WebUI 展示
6. **全量抓取** — 确认断点续传正常后放开运行（预计 3 分类共约 4.5 天，期间可随时 `docker compose restart scraper` 续跑）

---

## Verification Checklist

```bash
# 实时观察 round-robin 循环输出（每 1 秒一行）
docker compose logs -f scraper
# 首圈期望：连续 [DETAIL] +INSERT，每页 26 行
# 同步模式期望：[DETAIL] changed=True/False，连续 3 个 False 后出现大量 [SKIP]
# 示例（同步模式）：
# [LIST]   Manga     page=3 → 25 gids  (active=True)
# [DETAIL] Manga     gid=111111  changed=True   fav 234→251
# [DETAIL] Manga     gid=111112  changed=False  (no_change=1)
# [DETAIL] Manga     gid=111113  changed=False  (no_change=2)
# [DETAIL] Manga     gid=111114  changed=False  (no_change=3 → inactive)
# [SKIP]   Manga     gid=111115..111135

# 确认进度统计
curl http://localhost:3000/v1/stats

# 确认列表 API 返回正确排序
curl "http://localhost:3000/v1/galleries?category=Manga&sort=fav_count&page=1"

# 确认 tag 过滤
curl "http://localhost:3000/v1/galleries?tag=language:chinese"

# 浏览器打开 WebUI
open http://localhost:5173
```

---

## 风险提示

| 风险 | 说明 | 对策 |
|------|------|------|
| EX 封禁（429） | 速率过快触发限流 | 1 req/s 全局硬编码，单线程均匀间隔，指数退避重试 |
| EX 带宽配额（509） | EX 每日带宽配额耗尽 | 固定等待 1 小时，自动恢复，无需人工介入 |
| Cloudflare block（403） | TLS 指纹或 Cookie 异常 | 等待 2 小时，连续 3 次则标记 blocked 停止，需人工检查 Cookie |
| Cookie 失效 | EX Cookie 有效期数月 | 失效后更新 `.env`，重启 scraper 容器即可 |
| 封面图加载 | 浏览器需已登录 EX 才能加载封面 CDN | 加载失败显示占位符，视为可接受的低频不完美 |
| 画廊被删 | EX 画廊被删后跳转 404 | `is_active=false` 软删除标记，WebUI 可过滤不展示 |
