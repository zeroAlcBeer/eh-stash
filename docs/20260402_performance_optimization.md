# 性能优化记录

## 背景

项目部署在树莓派 4 (ARM64) 上，PostgreSQL 容器 CPU 占用长期在 25%-70% 之间震荡，偶尔冲到 100%。数据库约 12 万条画廊记录。

初始状态：`docker stats` 显示 PostgreSQL 容器 CPU 70.96%，内存 156MB。

## 第一阶段：Python 层优化

### 代码分析定位热点

通过审查 Python scraper 代码，识别出以下问题：

| 组件 | 问题 | 频率 |
|------|------|------|
| Recommended Scorer | 以 0.1 秒间隔持续批量扫描全部画廊，空闲时每 30 秒超时后重新开始全量扫描 | 持续 |
| TF-IDF 重建 | 收藏同步每抓一页就全量 TRUNCATE + 重算 preference_tags | 每页 |
| API 关联子查询 | 每行结果执行 `(SELECT COUNT(*) FROM gallery_group_members WHERE group_id = ...)` | 每请求×每行 |
| Scheduler 轮询 | 每 3 秒查一次 sync_tasks 表 | 持续 |
| Thumb Worker | 空闲时每 5 秒执行 `UPDATE ... FOR UPDATE SKIP LOCKED` 查询 | 持续 |

### 修复措施

1. **Scorer 改为信号驱动**：batch interval 从 0.1s 提到 2s；空闲时不再超时重扫，改为纯等待 `_scorer_reset` 信号
2. **TF-IDF 只在收藏同步末尾重算一次**：删除每页的 `rebuild_preference_tags()` 调用
3. **API 关联子查询改为预聚合**：`(SELECT COUNT(*) ...)` 改为 `LEFT JOIN (SELECT group_id, COUNT(*) ... GROUP BY group_id)` 或 window function
4. **PostgreSQL 资源限制**：docker-compose.pi.yaml 添加 `cpus: '1'` + PG 参数调优（shared_buffers=64MB, work_mem=4MB 等）

### 效果

CPU 从 70% 降到 25% 震荡，偶尔冲 100%。

## 第二阶段：Scraper + API 迁移至 Go

### 迁移动机

Python 版存在架构层面的低效：

- **同步 DB 阻塞异步循环**：`psycopg2` 是同步驱动，在 `async def` 中直接调用会阻塞整个 asyncio 事件循环，其他协程全部等待
- **资源开销**：Python 进程 ~60-80MB RSS + 193MB Docker 镜像，在树莓派 1-4GB RAM 环境下占比过高
- **GIL 限制**：asyncio 本身的调度开销在 ARM CPU 上不可忽略
- **Go 的优势**：goroutine 原生非阻塞并发，`pgx` 异步连接池，编译为静态二进制 ~15MB RSS / 21.6MB 镜像

### 迁移范围

scraper-go/ 完整重写：config、parser (goquery)、db (pgx)、client (utls Chrome TLS 指纹)、ratelimit、scheduler、task (full/incremental/favorites)、worker (thumb/scorer/grouper)。

关键验证：utls Chrome TLS 指纹通过 ExHentai bot 检测，集成测试确认列表页 25 items/page 解析正确。

### 架构改进

| 维度 | Python | Go |
|------|--------|----|
| 调度器轮询 | 3 秒 | 10 秒 |
| Thumb Worker 空闲 | 5 秒轮询 DB | channel 信号驱动 |
| Scorer 空闲 | 30 秒轮询 | channel 纯等待 |
| DB 调用 | psycopg2 同步阻塞事件循环 | pgx 原生非阻塞 |
| 连接池 | max=10×2 | max=5 |
| 并发模型 | asyncio + 全局变量 Event | goroutine + channel |
| 容器内存 | ~60-80MB | ~15MB |
| 镜像大小 | 193MB | 21.6MB |

## 第三阶段：SQL 查询优化

迁移到 Go 后，CPU 在 25% 和 0 之间震荡，偶尔冲 100%。通过 `pg_stat_statements` 精确定位到剩余热点。

### 诊断过程

#### 启用查询统计

docker-compose.pi.yaml 中 PostgreSQL 启动参数添加：

```yaml
command: >
  postgres
  ...
  -c shared_preload_libraries=pg_stat_statements
  -c pg_stat_statements.track=all
```

重启后执行：

```sql
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
```

查询 top 10：

```sql
SELECT calls, round(mean_exec_time)::int AS avg_ms,
       round(total_exec_time/1000)::int AS total_sec,
       LEFT(query, 120) AS query
FROM pg_stat_statements
ORDER BY total_exec_time DESC LIMIT 10;
```

#### 优化前数据

| 查询 | 调用次数 | 平均耗时 | 累计 | 问题 |
|------|---------|---------|------|------|
| Grouper `REGEXP_REPLACE` 增量分组 | 108 | 3071ms | 332s | 每页 upsert 触发一次，每次全表正则扫描 |
| Scorer `INSERT tags @>` 推荐打分 | 1207 | 136ms | 165s | 收藏没变也全量扫 12 万画廊 |
| Scorer `DELETE tags @>` 推荐清理 | 1207 | 129ms | 156s | 同上 |

总计 653 秒 DB CPU 时间。

### 优化方案

#### 优化 1：Grouper — base_title 物化列

**问题根因**：增量分组 SQL 对 12 万行执行 3 次 `REGEXP_REPLACE` 来计算标准化标题，每次 3 秒。

**方案**：
- `eh_galleries` 新增 `base_title` 列，在画廊写入时由应用层计算好
- 添加 B-tree 索引
- 分组 SQL 直接按索引查找，不再运行时算正则

**标准化规则**（Go 侧 `NormalizeBaseTitle`）：
1. 优先使用 `title_jpn`，无则 fallback 到 `title`
2. 去除标记：`[中国翻訳]` `[中国語]` `[DL版]` `[無修正]` `(C\d+)`
3. 去除所有空白字符

**迁移**：`migrations/006_base_title.sql`

```sql
ALTER TABLE eh_galleries ADD COLUMN IF NOT EXISTS base_title TEXT;

UPDATE eh_galleries
SET base_title = REGEXP_REPLACE(
    REGEXP_REPLACE(
        COALESCE(NULLIF(title_jpn, ''), title),
        '\s*\[中国翻訳\]|\s*\[中国語\]|\s*\[DL版\]|\s*\[無修正\]|\s*\(C\d+\)', '', 'g'
    ),
    '\s+', '', 'g'
)
WHERE COALESCE(NULLIF(title_jpn, ''), title) IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_eh_galleries_base_title
    ON eh_galleries (base_title) WHERE base_title IS NOT NULL AND base_title != '';
```

#### 优化 2：Scorer — 全轮按需触发

**问题根因**：收藏同步每轮结束都无条件重算 TF-IDF + 触发全量推荐评分，即使收藏没有任何变化。

**方案**：
- 每页记录是否有新增收藏（布尔标记）
- 全轮结束时：有新增 OR cleanup 删除了旧收藏 → 触发 rebuild + scorer
- 无变化 → 跳过，scorer 零开销

#### 优化 3：Scorer — 反向查询算法

**问题根因**：原算法正向遍历 12 万画廊，每批 100 个做 `tags @> jsonb_build_object(...)` 匹配。即使有 GIN 索引，1200 批 × 270ms = 5 分钟。

**新算法**（`scorer_reverse.go`）：
1. 读取偏好标签表（~200 行）
2. 对每个偏好标签，用 GIN 索引查 `SELECT gid FROM eh_galleries WHERE tags @> '{"artist":["xxx"]}'`
3. Go 内存中聚合每个画廊的得分
4. 批量写入 `recommended_cache`

200 次索引查询 vs 1200 次批量扫描，预期从 5 分钟降到 10 秒以内。

原算法保留在 `scorer.go`，可随时回退。

### 优化结果

#### 优化后数据

| 查询 | 调用次数 | 平均耗时 | 累计 |
|------|---------|---------|------|
| Grouper 增量分组（base_title 索引） | 8 | 633ms | 5s |
| 其余查询 | - | <56ms | <2s |
| Scorer | 0（未触发） | - | 0s |

#### 全程对比

| 指标 | 初始 (Python) | 第一阶段后 | 第二阶段后 (Go) | 第三阶段后 |
|------|--------------|-----------|----------------|-----------|
| CPU 常态 | 70% | 25% 震荡 | 25% 震荡 | **5%** |
| CPU 峰值 | 持续高位 | 偶尔 100% | 偶尔 100% | **偶尔 15%** |
| Grouper 单次 | 3071ms | 3071ms | 3071ms | **633ms** |
| Scorer 无变更开销 | 持续运行 | 信号驱动但无条件触发 | 同左 | **0s（跳过）** |
| 查询总耗时 (观测期) | — | — | 653s | **5s** |
| scraper 内存 | ~60-80MB | ~60-80MB | ~15MB | ~15MB |
| scraper 镜像 | 193MB | 193MB | 21.6MB | 21.6MB |

## 附录

### 资源限制配置

docker-compose.pi.yaml 中 PostgreSQL 容器：

```yaml
deploy:
  resources:
    limits:
      cpus: '1'
command: >
  postgres
  -c shared_buffers=64MB
  -c work_mem=4MB
  -c maintenance_work_mem=32MB
  -c effective_cache_size=128MB
  -c max_connections=20
  -c shared_preload_libraries=pg_stat_statements
  -c pg_stat_statements.track=all
```

### 排查常用命令

```bash
# 当前活跃查询
docker exec ehstash-postgres-1 psql -U postgres -d eh_stash -c "
SELECT pid, now() - query_start AS duration, state, LEFT(query, 150) AS query
FROM pg_stat_activity
WHERE state != 'idle' AND query NOT LIKE '%pg_stat%'
ORDER BY duration DESC;"

# 累计最慢查询 top 10
docker exec ehstash-postgres-1 psql -U postgres -d eh_stash -c "
SELECT calls, round(mean_exec_time)::int AS avg_ms,
       round(total_exec_time/1000)::int AS total_sec,
       LEFT(query, 120) AS query
FROM pg_stat_statements
ORDER BY total_exec_time DESC LIMIT 10;"

# 全表扫描 vs 索引命中率
docker exec ehstash-postgres-1 psql -U postgres -d eh_stash -c "
SELECT relname, seq_scan, idx_scan, n_live_tup,
       CASE WHEN seq_scan + idx_scan > 0
            THEN round(100.0 * idx_scan / (seq_scan + idx_scan))
            ELSE 0 END AS idx_hit_pct
FROM pg_stat_user_tables
ORDER BY seq_scan DESC;"

# 连接数
docker exec ehstash-postgres-1 psql -U postgres -d eh_stash -c "
SELECT count(*) AS total,
       count(*) FILTER (WHERE state = 'idle') AS idle,
       count(*) FILTER (WHERE state = 'active') AS active
FROM pg_stat_activity
WHERE backend_type = 'client backend';"

# 重置统计（观测新一轮数据前执行）
docker exec ehstash-postgres-1 psql -U postgres -d eh_stash -c "
SELECT pg_stat_statements_reset();"
```
