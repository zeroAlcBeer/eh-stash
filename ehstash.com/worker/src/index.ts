import { Hono } from 'hono';
import { cors } from 'hono/cors';
import { secureHeaders } from 'hono/secure-headers';
import postgres from 'postgres';
import tagWhitelist from './tags.json';

type RateLimiter = {
  limit: (input: { key: string }) => Promise<{ success: boolean }>;
};

type Bindings = {
  DB: Hyperdrive;
  THUMBS: R2Bucket;
  RATE_LIMITER: RateLimiter;
};

type SqlParameter = postgres.ParameterOrJSON<number>;

const app = new Hono<{ Bindings: Bindings }>();

// ─── Constants ──────────────────────────────────────────────────────────────

// Categories the public site is willing to expose. Asian Porn / Non-H /
// Western / Game CG / Artist CG are dropped entirely. Cosplay is gated
// behind the user's "三次元" toggle (allow_cosplay=1 from frontend).
const BASE_CATEGORIES = ['Manga', 'Doujinshi', 'Image Set', 'Misc'];
const COSPLAY_CATEGORY = 'Cosplay';

// Hard runtime blacklist. Must stay in sync with schema/blacklist.sql;
// that SQL physically deletes; this worker filter is the runtime fallback
// for anything that slips through (e.g. new sync data).
type BlacklistRule = Array<{ ns: string; val: string }>;
const TAG_BLACKLIST: BlacklistRule[] = [
  [{ ns: 'male', val: 'yaoi' }],
  [{ ns: 'female', val: 'amputee' }],
  [{ ns: 'female', val: 'futanari' }],
  [{ ns: 'other', val: 'full color' }],
  // Composite: both must be present to trigger
  [
    { ns: 'female', val: 'pregnant' },
    { ns: 'female', val: 'dark nipples' },
  ],
];

const VALID_SORTS = new Set([
  'gid_desc', 'gid_asc', 'fav_count', 'rating', 'posted_at', 'comment_count',
]);

// Per-request tag cap. Anything beyond this is silently dropped — keeps
// the cache-key space bounded (O(N²) at worst with N ≈ a few thousand
// known tags) and stops `&tag=...&tag=...&tag=...` style abuse from
// generating uncacheable combinatorial queries.
const TAG_LIMIT = 2;

// Whitelist of legal "ns:val" tag pairs, materialized from the DB by
// scripts/export-tags.sh. Empty set ⇒ bootstrap mode: validation is
// skipped (the gate is off until tags.json is populated). When non-empty,
// any request whose tag= param misses returns an empty page without
// touching Hyperdrive/Neon.
const TAG_SET: Set<string> = new Set(tagWhitelist.tags as string[]);

// Edge cache TTLs (seconds). Non-empty pages cache 5 min because data
// only changes during batch syncs. Empty pages — and whitelist rejects —
// cache 30 min so repeated "guess a tag" probing collapses to one cache
// write per distinct probe.
const OK_TTL = 300;
const EMPTY_TTL = 1800;

// ─── Middleware ─────────────────────────────────────────────────────────────

app.use('*', secureHeaders());

app.use(
  '*',
  cors({
    origin: (origin) => {
      if (!origin) return null;
      try {
        const host = new URL(origin).hostname;
        if (host === 'ehstash.com' || host === 'www.ehstash.com') return origin;
        if (host.endsWith('.pages.dev')) return origin;
        if (host === 'localhost' || host === '127.0.0.1') return origin;
      } catch { /* malformed Origin */ }
      return null;
    },
    allowMethods: ['GET'],
    maxAge: 86400,
  }),
);

// Origin/Referer gate for the data API. Browsers fetching from our own
// frontend always send a valid Origin or Referer; curl/scripts that omit
// both get a 403. Trivial to spoof, but it's another layer above Bot
// Fight Mode and the rate limiter.
function isAllowedHost(host: string): boolean {
  if (host === 'ehstash.com' || host === 'www.ehstash.com') return true;
  if (host.endsWith('.pages.dev')) return true;
  if (host === 'localhost' || host === '127.0.0.1') return true;
  return false;
}

app.use('/v1/*', async (c, next) => {
  for (const header of ['Origin', 'Referer']) {
    const raw = c.req.header(header);
    if (!raw) continue;
    try {
      if (isAllowedHost(new URL(raw).hostname)) return next();
    } catch { /* malformed header */ }
  }
  return c.json({ error: 'forbidden' }, 403);
});

// Per-IP rate limiter on the data API.
app.use('/v1/*', async (c, next) => {
  const ip = c.req.header('CF-Connecting-IP') || c.req.header('X-Real-IP') || 'unknown';
  const { success } = await c.env.RATE_LIMITER.limit({ key: ip });
  if (!success) {
    return c.json({ error: 'rate limited' }, 429, { 'Retry-After': '60' });
  }
  return next();
});

// ─── DB ─────────────────────────────────────────────────────────────────────

function makeSql(env: Bindings) {
  return postgres(env.DB.connectionString, {
    max: 5,
    fetch_types: false,
    prepare: false,
    idle_timeout: 20,
    types: {
      // OID 1700 NUMERIC → number (default is string).
      num: {
        to: 1700,
        from: [1700],
        serialize: String,
        parse: parseFloat,
      },
      // OID 20 BIGINT → number. gid values fit comfortably in JS safe-int.
      bi: {
        to: 20,
        from: [20],
        serialize: String,
        parse: Number,
      },
    },
  });
}

// ─── Tag normalization & whitelist ──────────────────────────────────────────

type Tag = { ns: string; val: string };

// Shared parser so the request validator and the SQL builder can't drift.
// ns is case-folded (matches existing buildWhere behavior); val is taken
// verbatim because JSONB containment is byte-exact against stored values.
function normalizeTag(raw: string): Tag | null {
  const t = raw.replace('：', ':').trim();
  const i = t.indexOf(':');
  if (i < 0) return null;
  const ns = t.slice(0, i).trim().toLowerCase();
  const val = t.slice(i + 1).trim();
  if (!ns || !val) return null;
  return { ns, val };
}

// Returns the validated, capped, deduped tag list — or null if any tag
// is unknown (caller short-circuits to an empty page).
function validateTags(raw: string[]): Tag[] | null {
  const capped = raw.slice(0, TAG_LIMIT);
  const out: Tag[] = [];
  const seen = new Set<string>();
  for (const r of capped) {
    const t = normalizeTag(r);
    if (!t) continue;
    const key = `${t.ns}:${t.val}`;
    if (seen.has(key)) continue;
    seen.add(key);
    if (TAG_SET.size > 0 && !TAG_SET.has(key)) return null;
    out.push(t);
  }
  return out;
}

// ─── Cache helpers ──────────────────────────────────────────────────────────

// Build a stable cache key from request shape (not raw URL). Equivalent
// requests — different param order, default values written explicitly,
// case-different category — collapse to the same key.
function galleriesCacheKey(p: {
  category: string;
  language: string;
  minRating: number;
  minFav: number;
  tags: Tag[];
  allowCosplay: boolean;
  sort: string;
  page: number;
  pageSize: number;
}): Request {
  const u = new URL('https://cache.ehstash.local/v1/galleries');
  const sp = u.searchParams;
  if (p.category) sp.set('cat', p.category.toLowerCase());
  if (p.language) sp.set('lang', p.language.toLowerCase());
  if (p.minRating > 0) sp.set('mr', String(p.minRating));
  if (p.minFav > 0) sp.set('mf', String(p.minFav));
  if (p.allowCosplay) sp.set('ac', '1');
  sp.set('sort', p.sort);
  sp.set('p', String(p.page));
  sp.set('ps', String(p.pageSize));
  const tagsKey = p.tags.map(({ ns, val }) => `${ns}:${val}`).sort().join('|');
  if (tagsKey) sp.set('tags', tagsKey);
  return new Request(u.toString(), { method: 'GET' });
}

function jsonResponse(body: unknown, sMaxAge: number): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: {
      'Content-Type': 'application/json; charset=utf-8',
      // s-maxage drives the Workers Cache + CF edge; max-age keeps a
      // short browser cache so back/forward feels instant. CORS headers
      // are added on the unwind by Hono's middleware and are not stored.
      'Cache-Control': `public, s-maxage=${sMaxAge}, max-age=60`,
    },
  });
}

function emptyPage(page: number, pageSize: number, ttl: number): Response {
  return jsonResponse(
    { items: [], total: 0, page, size: pageSize, pages: 0 },
    ttl,
  );
}

// ─── SQL builders ───────────────────────────────────────────────────────────

function buildOrderBy(sort: string): string {
  switch (sort) {
    case 'gid_asc':       return 'g.gid ASC';
    case 'fav_count':     return 'g.fav_count DESC NULLS LAST, g.gid DESC';
    case 'rating':        return 'g.rating DESC NULLS LAST, g.gid DESC';
    case 'posted_at':     return 'g.posted_at DESC NULLS LAST, g.gid DESC';
    case 'comment_count': return 'g.comment_count DESC NULLS LAST, g.gid DESC';
    case 'gid_desc':
    default:              return 'g.gid DESC';
  }
}

// Returns a single SQL fragment that excludes any blacklisted gallery,
// plus the params it binds (appended to the caller's positional list).
function buildBlacklistClause(startIdx: number): { sql: string; params: string[] } {
  if (TAG_BLACKLIST.length === 0) return { sql: 'TRUE', params: [] };
  const params: string[] = [];
  const ruleClauses: string[] = [];
  for (const rule of TAG_BLACKLIST) {
    const tagClauses = rule.map(({ ns, val }) => {
      const nsIdx = startIdx + params.length;
      params.push(ns);
      const valIdx = startIdx + params.length;
      params.push(val);
      return `g.tags @> jsonb_build_object($${nsIdx}::text, jsonb_build_array($${valIdx}::text))`;
    });
    ruleClauses.push(`(${tagClauses.join(' AND ')})`);
  }
  return { sql: `NOT (${ruleClauses.join(' OR ')})`, params };
}

function buildCategoryClause(allowCosplay: boolean, startIdx: number): { sql: string; params: string[] } {
  const allowed = allowCosplay ? [...BASE_CATEGORIES, COSPLAY_CATEGORY] : BASE_CATEGORIES;
  const placeholders = allowed.map((_, i) => `$${startIdx + i}`);
  return { sql: `g.category IN (${placeholders.join(', ')})`, params: allowed };
}

function buildWhere(input: {
  category: string;
  language: string;
  minRating: number;
  minFav: number;
  tags: Tag[];
  allowCosplay: boolean;
}): { sql: string; values: SqlParameter[] } {
  const values: SqlParameter[] = [];
  const conds: string[] = ['g.is_active = TRUE'];
  const bind = (v: SqlParameter) => {
    values.push(v);
    return `$${values.length}`;
  };

  const cat = buildCategoryClause(input.allowCosplay, values.length + 1);
  values.push(...cat.params);
  conds.push(cat.sql);

  const bl = buildBlacklistClause(values.length + 1);
  values.push(...bl.params);
  if (bl.sql !== 'TRUE') conds.push(bl.sql);

  if (input.category) conds.push(`g.category ILIKE ${bind(input.category)}`);
  if (input.language) conds.push(`g.language ILIKE ${bind(input.language)}`);
  if (input.minRating > 0) conds.push(`g.rating >= ${bind(input.minRating)}`);
  if (input.minFav > 0) conds.push(`g.fav_count >= ${bind(input.minFav)}`);

  for (const { ns, val } of input.tags) {
    conds.push(
      `g.tags @> jsonb_build_object(${bind(ns)}::text, jsonb_build_array(${bind(val)}::text))`,
    );
  }

  return { sql: conds.join(' AND '), values };
}

// ─── Routes ─────────────────────────────────────────────────────────────────

app.get('/', (c) => c.json({ name: 'ehstash-api', ok: true }));

app.get('/v1/galleries', async (c) => {
  const url = new URL(c.req.url);
  const category = url.searchParams.get('category') || '';
  const language = url.searchParams.get('language') || '';
  const minRating = Number(url.searchParams.get('min_rating')) || 0;
  const minFav = Number(url.searchParams.get('min_fav')) || 0;
  const rawTags = url.searchParams.getAll('tag');
  const allowCosplay = url.searchParams.get('allow_cosplay') === '1';
  const sortRaw = url.searchParams.get('sort') || 'gid_desc';
  const sort = VALID_SORTS.has(sortRaw) ? sortRaw : 'gid_desc';
  const page = Math.max(1, Number(url.searchParams.get('page')) || 1);
  const pageSize = Math.min(100, Math.max(1, Number(url.searchParams.get('page_size')) || 24));
  const offset = (page - 1) * pageSize;

  // L2: whitelist + cap. Unknown tag ⇒ negative-cached empty page.
  const tags = validateTags(rawTags);
  if (tags === null) {
    return emptyPage(page, pageSize, EMPTY_TTL);
  }

  // L1: edge cache lookup. Hit returns a fresh Response so the CORS
  // middleware on the unwind can attach the right ACAO header.
  const cacheKey = galleriesCacheKey({
    category, language, minRating, minFav,
    tags, allowCosplay, sort, page, pageSize,
  });
  const cached = await caches.default.match(cacheKey);
  if (cached) {
    return new Response(cached.body, {
      status: cached.status,
      headers: new Headers(cached.headers),
    });
  }

  const sql = makeSql(c.env);
  try {
    const { sql: whereSql, values } = buildWhere({
      category, language, minRating, minFav, tags, allowCosplay,
    });
    const orderBy = buildOrderBy(sort);

    const countQuery = `
      SELECT COUNT(*)::int AS total
      FROM eh_galleries g
      WHERE ${whereSql}
    `;
    const countRows = await sql.unsafe<{ total: number }[]>(countQuery, values);
    const total = countRows[0]?.total ?? 0;

    const itemQuery = `
      SELECT g.gid, g.token, g.category, g.title, g.title_jpn, g.uploader,
             g.posted_at, g.language, g.pages, g.rating, g.fav_count,
             g.thumb, g.comment_count, g.tags,
             ggm.group_id,
             COALESCE(gc.cnt, 0)::int AS group_count
      FROM eh_galleries g
      LEFT JOIN gallery_group_members ggm ON ggm.gid = g.gid
      LEFT JOIN (
        SELECT ggmc.group_id, COUNT(*) AS cnt
        FROM gallery_group_members ggmc
        JOIN eh_galleries gc2 ON gc2.gid = ggmc.gid
        WHERE gc2.is_active = TRUE
        GROUP BY ggmc.group_id
      ) gc ON gc.group_id = ggm.group_id
      WHERE ${whereSql}
      ORDER BY ${orderBy}
      LIMIT $${values.length + 1} OFFSET $${values.length + 2}
    `;
    const items = await sql.unsafe(itemQuery, [...values, pageSize, offset]);

    const body = {
      items,
      total,
      page,
      size: pageSize,
      pages: total ? Math.ceil(total / pageSize) : 0,
    };
    const ttl = total === 0 ? EMPTY_TTL : OK_TTL;
    const response = jsonResponse(body, ttl);
    c.executionCtx.waitUntil(caches.default.put(cacheKey, response.clone()));
    return response;
  } finally {
    c.executionCtx.waitUntil(sql.end({ timeout: 5 }));
  }
});

app.get('/v1/galleries/group/:groupId', async (c) => {
  const sql = makeSql(c.env);
  try {
    const groupId = Number(c.req.param('groupId'));
    if (!Number.isFinite(groupId)) {
      return c.json({ error: 'invalid group id' }, 400);
    }
    const url = new URL(c.req.url);
    const allowCosplay = url.searchParams.get('allow_cosplay') === '1';

    const params: SqlParameter[] = [groupId];
    const cat = buildCategoryClause(allowCosplay, params.length + 1);
    params.push(...cat.params);
    const bl = buildBlacklistClause(params.length + 1);
    params.push(...bl.params);
    const blClause = bl.sql === 'TRUE' ? '' : `AND ${bl.sql}`;

    const query = `
      SELECT g.gid, g.token, g.category, g.title, g.title_jpn, g.uploader,
             g.posted_at, g.language, g.pages, g.rating, g.fav_count,
             g.thumb, g.comment_count, g.tags,
             ggm.group_id,
             COUNT(*) OVER (PARTITION BY ggm.group_id)::int AS group_count
      FROM gallery_group_members ggm
      JOIN eh_galleries g ON g.gid = ggm.gid
      WHERE ggm.group_id = $1
        AND g.is_active = TRUE
        AND ${cat.sql}
        ${blClause}
      ORDER BY g.posted_at ASC NULLS LAST, g.gid ASC
    `;
    const rows = await sql.unsafe(query, params);
    if (rows.length === 0) return c.json({ error: 'group not found' }, 404);
    return c.json(rows);
  } finally {
    c.executionCtx.waitUntil(sql.end({ timeout: 5 }));
  }
});

app.get('/v1/galleries/:gid', async (c) => {
  const sql = makeSql(c.env);
  try {
    const gid = Number(c.req.param('gid'));
    if (!Number.isFinite(gid)) {
      return c.json({ error: 'invalid gid' }, 400);
    }
    const url = new URL(c.req.url);
    const allowCosplay = url.searchParams.get('allow_cosplay') === '1';

    const params: SqlParameter[] = [gid];
    const cat = buildCategoryClause(allowCosplay, params.length + 1);
    params.push(...cat.params);
    const bl = buildBlacklistClause(params.length + 1);
    params.push(...bl.params);
    const blClause = bl.sql === 'TRUE' ? '' : `AND ${bl.sql}`;

    const query = `
      SELECT g.gid, g.token, g.category, g.title, g.title_jpn, g.uploader,
             g.posted_at, g.language, g.pages, g.rating, g.fav_count,
             g.thumb, g.comment_count, g.tags, g.last_synced_at,
             g.file_size, g.file_size_bytes, g.rating_count, g.visible,
             g.parent_gid, g.torrent_count, g.is_expunged,
             ggm.group_id,
             COALESCE(gc.cnt, 0)::int AS group_count
      FROM eh_galleries g
      LEFT JOIN gallery_group_members ggm ON ggm.gid = g.gid
      LEFT JOIN (
        SELECT ggmc.group_id, COUNT(*) AS cnt
        FROM gallery_group_members ggmc
        JOIN eh_galleries gc2 ON gc2.gid = ggmc.gid
        WHERE gc2.is_active = TRUE
        GROUP BY ggmc.group_id
      ) gc ON gc.group_id = ggm.group_id
      WHERE g.gid = $1
        AND g.is_active = TRUE
        AND ${cat.sql}
        ${blClause}
      LIMIT 1
    `;
    const rows = await sql.unsafe(query, params);
    if (rows.length === 0) return c.json({ error: 'not found' }, 404);
    return c.json(rows[0]);
  } finally {
    c.executionCtx.waitUntil(sql.end({ timeout: 5 }));
  }
});

// Optional thumb passthrough — frontend normally hits R2 directly.
app.get('/v1/thumbs/:gid', async (c) => {
  const gid = c.req.param('gid');
  const obj = await c.env.THUMBS.get(gid);
  if (!obj) return c.notFound();
  return new Response(obj.body, {
    headers: {
      'Content-Type': obj.httpMetadata?.contentType || 'image/jpeg',
      'Cache-Control': 'public, max-age=604800, immutable',
      ETag: obj.httpEtag,
    },
  });
});

export default app;
