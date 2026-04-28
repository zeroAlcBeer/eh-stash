package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	cfg.MaxConns = 5
	cfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to db: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() {
	d.pool.Close()
}

// GalleryRow represents a row to upsert into eh_galleries.
type GalleryRow struct {
	GID          int64
	Token        string
	Category     string
	Title        string
	TitleJPN     string
	BaseTitle    string // normalized title for grouping
	Uploader     string
	PostedAt     *time.Time
	Language     string
	Pages        int
	Rating       *float64
	FavCount     int
	CommentCount int
	Thumb        string
	Tags         map[string][]string
	IsActive     bool
}

type SyncTask struct {
	ID            int
	Name          string
	Type          string
	Category      string
	DesiredStatus string
	Status        string
	Config        map[string]any
	State         map[string]any
	ProgressPct   float64
	LastRunAt     *time.Time
	ErrorMessage  *string
}

type ThumbQueueItem struct {
	ID         int
	GID        int64
	ThumbURL   string
	RetryCount int
}

func (d *DB) UpsertGalleriesBulk(ctx context.Context, rows []GalleryRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Build bulk values
	var values []string
	var args []any
	idx := 1
	for _, r := range rows {
		tagsJSON, _ := json.Marshal(r.Tags)
		values = append(values, fmt.Sprintf(
			"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d::jsonb,NOW(),$%d)",
			idx, idx+1, idx+2, idx+3, idx+4, idx+5, idx+6, idx+7,
			idx+8, idx+9, idx+10, idx+11, idx+12, idx+13, idx+14, idx+15,
		))
		args = append(args,
			r.GID, r.Token, r.Category, r.Title, r.TitleJPN, r.BaseTitle, r.Uploader,
			r.PostedAt, r.Language, r.Pages, r.Rating, r.FavCount,
			r.CommentCount, r.Thumb, string(tagsJSON), r.IsActive,
		)
		idx += 16
	}

	sql := `INSERT INTO eh_galleries (
		gid, token, category, title, title_jpn, base_title, uploader, posted_at, language,
		pages, rating, fav_count, comment_count, thumb, tags, last_synced_at, is_active
	) VALUES ` + strings.Join(values, ",") + `
	ON CONFLICT (gid) DO UPDATE SET
		token = EXCLUDED.token, category = EXCLUDED.category,
		title = EXCLUDED.title, title_jpn = EXCLUDED.title_jpn,
		base_title = EXCLUDED.base_title,
		uploader = EXCLUDED.uploader, posted_at = EXCLUDED.posted_at,
		language = EXCLUDED.language, pages = EXCLUDED.pages,
		rating = EXCLUDED.rating, fav_count = EXCLUDED.fav_count,
		comment_count = EXCLUDED.comment_count, thumb = EXCLUDED.thumb,
		tags = EXCLUDED.tags, last_synced_at = NOW(), is_active = EXCLUDED.is_active`

	_, err = tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("upsert galleries: %w", err)
	}

	// Upsert thumb_queue for rows with thumb URL
	var thumbValues []string
	var thumbArgs []any
	tidx := 1
	for _, r := range rows {
		if r.Thumb == "" {
			continue
		}
		thumbValues = append(thumbValues, fmt.Sprintf("($%d,$%d)", tidx, tidx+1))
		thumbArgs = append(thumbArgs, r.GID, r.Thumb)
		tidx += 2
	}

	if len(thumbValues) > 0 {
		thumbSQL := `INSERT INTO thumb_queue (gid, thumb_url) VALUES ` +
			strings.Join(thumbValues, ",") + `
			ON CONFLICT (gid) DO UPDATE SET
				thumb_url = EXCLUDED.thumb_url,
				status = 'pending',
				retry_count = 0,
				processed_at = NULL
			WHERE thumb_queue.thumb_url != EXCLUDED.thumb_url
			   OR thumb_queue.status = 'failed'`
		_, err = tx.Exec(ctx, thumbSQL, thumbArgs...)
		if err != nil {
			return 0, fmt.Errorf("upsert thumb_queue: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (d *DB) ListSyncTasks(ctx context.Context) ([]SyncTask, error) {
	rows, err := d.pool.Query(ctx, "SELECT id, name, type, category, desired_status, status, config, state, progress_pct, last_run_at, error_message FROM sync_tasks ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []SyncTask
	for rows.Next() {
		var t SyncTask
		var cfgJSON, stateJSON []byte
		err := rows.Scan(&t.ID, &t.Name, &t.Type, &t.Category, &t.DesiredStatus, &t.Status,
			&cfgJSON, &stateJSON, &t.ProgressPct, &t.LastRunAt, &t.ErrorMessage)
		if err != nil {
			return nil, err
		}
		t.Config = make(map[string]any)
		t.State = make(map[string]any)
		_ = json.Unmarshal(cfgJSON, &t.Config)
		_ = json.Unmarshal(stateJSON, &t.State)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (d *DB) GetTaskRuntime(ctx context.Context, taskID int) (*SyncTask, error) {
	var t SyncTask
	var cfgJSON, stateJSON []byte
	err := d.pool.QueryRow(ctx,
		`SELECT id, name, type, category, desired_status, status, config, state, progress_pct
		 FROM sync_tasks WHERE id = $1`, taskID,
	).Scan(&t.ID, &t.Name, &t.Type, &t.Category, &t.DesiredStatus, &t.Status,
		&cfgJSON, &stateJSON, &t.ProgressPct)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Config = make(map[string]any)
	t.State = make(map[string]any)
	_ = json.Unmarshal(cfgJSON, &t.Config)
	_ = json.Unmarshal(stateJSON, &t.State)
	return &t, nil
}

func (d *DB) UpdateTaskRuntime(ctx context.Context, taskID int, opts ...UpdateOption) error {
	u := &updateOpts{}
	for _, o := range opts {
		o(u)
	}

	sets := []string{"updated_at = NOW()"}
	args := []any{}
	idx := 1

	if u.state != nil {
		stateJSON, _ := json.Marshal(u.state)
		sets = append(sets, fmt.Sprintf("state = $%d", idx))
		args = append(args, string(stateJSON))
		idx++
	}
	if u.progressPct != nil {
		sets = append(sets, fmt.Sprintf("progress_pct = $%d", idx))
		args = append(args, *u.progressPct)
		idx++
	}
	if u.status != nil {
		sets = append(sets, fmt.Sprintf("status = $%d", idx))
		args = append(args, *u.status)
		idx++
	}
	if u.errorMessage != nil {
		sets = append(sets, fmt.Sprintf("error_message = $%d", idx))
		args = append(args, *u.errorMessage)
		idx++
	}
	if u.touchRunTime {
		sets = append(sets, "last_run_at = NOW()")
	}

	if len(sets) == 1 {
		return nil // only updated_at
	}

	args = append(args, taskID)
	sql := fmt.Sprintf("UPDATE sync_tasks SET %s WHERE id = $%d", strings.Join(sets, ", "), idx)
	_, err := d.pool.Exec(ctx, sql, args...)
	return err
}

type updateOpts struct {
	state        map[string]any
	progressPct  *float64
	status       *string
	errorMessage *string
	touchRunTime bool
}

type UpdateOption func(*updateOpts)

func WithState(s map[string]any) UpdateOption {
	return func(o *updateOpts) { o.state = s }
}

func WithProgress(p float64) UpdateOption {
	return func(o *updateOpts) { o.progressPct = &p }
}

func WithStatus(s string) UpdateOption {
	return func(o *updateOpts) { o.status = &s }
}

func WithError(msg string) UpdateOption {
	return func(o *updateOpts) { o.errorMessage = &msg }
}

func WithTouchRunTime() UpdateOption {
	return func(o *updateOpts) { o.touchRunTime = true }
}

func (d *DB) SetTaskDesiredStatus(ctx context.Context, taskID int, desired string) error {
	_, err := d.pool.Exec(ctx,
		"UPDATE sync_tasks SET desired_status = $1, updated_at = NOW() WHERE id = $2",
		desired, taskID)
	return err
}

func (d *DB) ClaimNextThumbQueueItem(ctx context.Context) (*ThumbQueueItem, error) {
	var item ThumbQueueItem
	err := d.pool.QueryRow(ctx, `
		UPDATE thumb_queue SET status = 'processing'
		WHERE id = (
			SELECT id FROM thumb_queue
			WHERE status = 'pending'
			  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
			ORDER BY created_at LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, gid, thumb_url, retry_count
	`).Scan(&item.ID, &item.GID, &item.ThumbURL, &item.RetryCount)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (d *DB) MarkThumbDone(ctx context.Context, itemID int) error {
	_, err := d.pool.Exec(ctx,
		"UPDATE thumb_queue SET status = 'done', processed_at = NOW() WHERE id = $1", itemID)
	return err
}

func (d *DB) MarkThumbFailed(ctx context.Context, itemID int, maxRetries int) (int, string, error) {
	var retryCount int
	var status string
	err := d.pool.QueryRow(ctx, `
		UPDATE thumb_queue
		SET retry_count = retry_count + 1,
			status = CASE WHEN retry_count + 1 >= $1 THEN 'failed' ELSE 'pending' END,
			processed_at = CASE WHEN retry_count + 1 >= $1 THEN NOW() ELSE NULL END,
			next_retry_at = CASE WHEN retry_count + 1 >= $1 THEN NULL
				ELSE NOW() + (LEAST(POWER(2, retry_count + 1), 8) || ' minutes')::interval
			END
		WHERE id = $2
		RETURNING retry_count, status
	`, maxRetries, itemID).Scan(&retryCount, &status)
	return retryCount, status, err
}

func (d *DB) MarkThumbPermanentFailed(ctx context.Context, itemID int) error {
	_, err := d.pool.Exec(ctx,
		"UPDATE thumb_queue SET status = 'failed', processed_at = NOW() WHERE id = $1", itemID)
	return err
}

func (d *DB) MarkGalleryInactive(ctx context.Context, gid int64) error {
	_, err := d.pool.Exec(ctx,
		"UPDATE eh_galleries SET is_active = FALSE WHERE gid = $1", gid)
	return err
}

func (d *DB) ResetStaleThumbProcessing(ctx context.Context) (int, error) {
	tag, err := d.pool.Exec(ctx,
		"UPDATE thumb_queue SET status = 'pending' WHERE status = 'processing'")
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (d *DB) CountGalleriesByCategory(ctx context.Context, category string) (int, error) {
	var count int
	err := d.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM eh_galleries WHERE LOWER(category) = LOWER($1)", category,
	).Scan(&count)
	return count, err
}

// UpsertFavorites inserts favorites, only for gids that exist in eh_galleries.
func (d *DB) UpsertFavorites(ctx context.Context, favorites []FavoriteRow) (int, error) {
	if len(favorites) == 0 {
		return 0, nil
	}

	gids := make([]int64, len(favorites))
	tsArr := make([]*string, len(favorites))
	for i, f := range favorites {
		gids[i] = f.GID
		tsArr[i] = f.FavoritedAt
	}

	tag, err := d.pool.Exec(ctx, `
		INSERT INTO user_favorites (gid, favorited_at)
		SELECT v.gid, COALESCE((ts.val)::timestamptz, NOW())
		FROM unnest($1::bigint[]) AS v(gid)
		JOIN eh_galleries g ON g.gid = v.gid
		LEFT JOIN (
			SELECT * FROM unnest($1::bigint[], $2::text[]) AS t(gid, val)
		) ts ON ts.gid = v.gid
		ON CONFLICT (gid) DO UPDATE SET favorited_at = EXCLUDED.favorited_at
	`, gids, tsArr)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

type FavoriteRow struct {
	GID         int64
	FavoritedAt *string
}

// UpsertFavoritesCountNew inserts favorites and returns the count of genuinely new rows.
func (d *DB) UpsertFavoritesCountNew(ctx context.Context, favorites []FavoriteRow) (int, error) {
	if len(favorites) == 0 {
		return 0, nil
	}

	gids := make([]int64, len(favorites))
	tsArr := make([]*string, len(favorites))
	for i, f := range favorites {
		gids[i] = f.GID
		tsArr[i] = f.FavoritedAt
	}

	// Count existing favorites before upsert
	var existingCount int
	err := d.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM user_favorites WHERE gid = ANY($1)", gids,
	).Scan(&existingCount)
	if err != nil {
		return 0, err
	}

	// Upsert
	tag, err := d.pool.Exec(ctx, `
		INSERT INTO user_favorites (gid, favorited_at)
		SELECT v.gid, COALESCE((ts.val)::timestamptz, NOW())
		FROM unnest($1::bigint[]) AS v(gid)
		JOIN eh_galleries g ON g.gid = v.gid
		LEFT JOIN (
			SELECT * FROM unnest($1::bigint[], $2::text[]) AS t(gid, val)
		) ts ON ts.gid = v.gid
		ON CONFLICT (gid) DO UPDATE SET favorited_at = EXCLUDED.favorited_at
	`, gids, tsArr)
	if err != nil {
		return 0, err
	}

	// New = total affected - previously existing
	totalAffected := int(tag.RowsAffected())
	newCount := totalAffected - existingCount
	if newCount < 0 {
		newCount = 0
	}
	return newCount, nil
}

func (d *DB) CleanupStaleFavorites(ctx context.Context, allGIDs []int64) (int, error) {
	if len(allGIDs) == 0 {
		tag, err := d.pool.Exec(ctx,
			"DELETE FROM user_favorites WHERE gid IN (SELECT gid FROM eh_galleries WHERE is_active = TRUE)")
		if err != nil {
			return 0, err
		}
		return int(tag.RowsAffected()), nil
	}

	tag, err := d.pool.Exec(ctx, `
		DELETE FROM user_favorites
		WHERE gid NOT IN (SELECT unnest($1::bigint[]))
		  AND gid IN (SELECT gid FROM eh_galleries WHERE is_active = TRUE)
	`, allGIDs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (d *DB) GetNonExistingGIDs(ctx context.Context, gids []int64) ([]int64, error) {
	if len(gids) == 0 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `
		SELECT v.gid FROM unnest($1::bigint[]) AS v(gid)
		LEFT JOIN eh_galleries g ON g.gid = v.gid
		WHERE g.gid IS NULL
	`, gids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []int64
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		result = append(result, gid)
	}
	return result, rows.Err()
}

func (d *DB) RebuildPreferenceTags(ctx context.Context) (int, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, "TRUNCATE preference_tags")
	if err != nil {
		return 0, err
	}

	tag, err := tx.Exec(ctx, `
		WITH fav_tf AS (
			SELECT ns, tag_value, COUNT(*)::REAL AS tf
			FROM eh_galleries g
			JOIN user_favorites f ON g.gid = f.gid,
				 jsonb_each(g.tags) AS t(ns, vals),
				 jsonb_array_elements_text(vals) AS tag_value
			WHERE ns IN ('artist', 'group', 'character', 'parody')
			GROUP BY ns, tag_value
		),
		doc_freq AS (
			SELECT ns, tag_value, COUNT(DISTINCT g.gid)::REAL AS df
			FROM eh_galleries g,
				 jsonb_each(g.tags) AS t(ns, vals),
				 jsonb_array_elements_text(vals) AS tag_value
			WHERE g.is_active = TRUE
			  AND ns IN ('artist', 'group', 'character', 'parody')
			GROUP BY ns, tag_value
		),
		total AS (
			SELECT COUNT(*)::REAL AS n FROM eh_galleries WHERE is_active = TRUE
		)
		INSERT INTO preference_tags (namespace, tag, weight, count)
		SELECT f.ns, f.tag_value,
			   (1.0 + LN(f.tf)) * LN(total.n / GREATEST(d.df, 1)),
			   f.tf
		FROM fav_tf f
		JOIN doc_freq d ON d.ns = f.ns AND d.tag_value = f.tag_value
		CROSS JOIN total
	`)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (d *DB) ScoreRecommendedBatch(ctx context.Context, cursorGID *int64, batchSize int) ([]int64, *int64, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	// Check preference_tags has data
	var exists bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM preference_tags LIMIT 1)").Scan(&exists)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, nil
	}

	// Fetch batch of GIDs
	var gidRows pgx.Rows
	if cursorGID == nil {
		gidRows, err = tx.Query(ctx,
			"SELECT gid FROM eh_galleries WHERE is_active = TRUE ORDER BY gid DESC LIMIT $1",
			batchSize)
	} else {
		gidRows, err = tx.Query(ctx,
			"SELECT gid FROM eh_galleries WHERE is_active = TRUE AND gid < $1 ORDER BY gid DESC LIMIT $2",
			*cursorGID, batchSize)
	}
	if err != nil {
		return nil, nil, err
	}

	var gids []int64
	for gidRows.Next() {
		var gid int64
		if err := gidRows.Scan(&gid); err != nil {
			gidRows.Close()
			return nil, nil, err
		}
		gids = append(gids, gid)
	}
	gidRows.Close()

	if len(gids) == 0 {
		return nil, nil, nil
	}

	// Score and upsert
	_, err = tx.Exec(ctx, `
		INSERT INTO recommended_cache (gid, rec_score)
		SELECT g.gid, SUM(p.weight)
		FROM preference_tags p
		JOIN eh_galleries g ON g.tags @> jsonb_build_object(p.namespace, jsonb_build_array(p.tag))
		WHERE g.gid = ANY($1)
		GROUP BY g.gid
		HAVING SUM(p.weight) >= 20
		ON CONFLICT (gid) DO UPDATE SET rec_score = EXCLUDED.rec_score
	`, gids)
	if err != nil {
		return nil, nil, err
	}

	// Cleanup below threshold
	_, err = tx.Exec(ctx, `
		DELETE FROM recommended_cache
		WHERE gid = ANY($1)
		  AND gid NOT IN (
			  SELECT g.gid
			  FROM preference_tags p
			  JOIN eh_galleries g ON g.tags @> jsonb_build_object(p.namespace, jsonb_build_array(p.tag))
			  WHERE g.gid = ANY($1)
			  GROUP BY g.gid
			  HAVING SUM(p.weight) >= 20
		  )
	`, gids)
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	var nextCursor *int64
	if len(gids) == batchSize {
		last := gids[len(gids)-1]
		nextCursor = &last
	}
	return gids, nextCursor, nil
}

// PreferenceTag represents a row from preference_tags.
type PreferenceTag struct {
	Namespace string
	Tag       string
	Weight    float64
}

// ListPreferenceTags returns all preference tags.
func (d *DB) ListPreferenceTags(ctx context.Context) ([]PreferenceTag, error) {
	rows, err := d.pool.Query(ctx, "SELECT namespace, tag, weight FROM preference_tags")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []PreferenceTag
	for rows.Next() {
		var t PreferenceTag
		if err := rows.Scan(&t.Namespace, &t.Tag, &t.Weight); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// FindGIDsByTag returns all active gallery GIDs that contain the given tag.
// Uses the GIN index on tags column.
func (d *DB) FindGIDsByTag(ctx context.Context, namespace, tag string) ([]int64, error) {
	jsonPattern := fmt.Sprintf(`{"%s": ["%s"]}`, namespace, tag)
	rows, err := d.pool.Query(ctx,
		"SELECT gid FROM eh_galleries WHERE is_active = TRUE AND tags @> $1::jsonb",
		jsonPattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var gids []int64
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return gids, rows.Err()
}

// ReplaceRecommendedCache replaces the entire recommended_cache with new scores.
func (d *DB) ReplaceRecommendedCache(ctx context.Context, scores map[int64]float64, threshold float64) (int, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, "TRUNCATE recommended_cache")
	if err != nil {
		return 0, err
	}

	if len(scores) == 0 {
		return 0, tx.Commit(ctx)
	}

	// Batch insert
	var values []string
	var args []any
	idx := 1
	for gid, score := range scores {
		if score < threshold {
			continue
		}
		values = append(values, fmt.Sprintf("($%d,$%d)", idx, idx+1))
		args = append(args, gid, score)
		idx += 2

		// Flush in batches of 1000
		if len(values) >= 1000 {
			_, err = tx.Exec(ctx,
				"INSERT INTO recommended_cache (gid, rec_score) VALUES "+strings.Join(values, ","),
				args...)
			if err != nil {
				return 0, err
			}
			values = values[:0]
			args = args[:0]
			idx = 1
		}
	}

	inserted := 0
	if len(values) > 0 {
		tag, err := tx.Exec(ctx,
			"INSERT INTO recommended_cache (gid, rec_score) VALUES "+strings.Join(values, ","),
			args...)
		if err != nil {
			return 0, err
		}
		inserted = int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (d *DB) GalleryGroupFullRebuild(ctx context.Context) (int, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, "TRUNCATE gallery_group_members")
	if err != nil {
		return 0, err
	}

	tag, err := tx.Exec(ctx, `
		WITH multi AS (
			SELECT base_title FROM eh_galleries
			WHERE base_title IS NOT NULL AND base_title != ''
			GROUP BY base_title HAVING COUNT(*) > 1
		),
		grouped AS (
			SELECT MIN(g.gid) OVER (PARTITION BY g.base_title) AS group_id, g.gid
			FROM eh_galleries g JOIN multi m ON g.base_title = m.base_title
		)
		INSERT INTO gallery_group_members (group_id, gid)
		SELECT group_id, gid FROM grouped
		ON CONFLICT (gid) DO UPDATE SET group_id = EXCLUDED.group_id
	`)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (d *DB) GalleryGroupIncremental(ctx context.Context) (int, error) {
	tag, err := d.pool.Exec(ctx, `
		WITH new_galleries AS (
			SELECT gid, base_title
			FROM eh_galleries
			WHERE base_title IS NOT NULL AND base_title != ''
			  AND gid NOT IN (SELECT gid FROM gallery_group_members)
		),
		matching AS (
			SELECT g.gid, g.base_title
			FROM eh_galleries g
			WHERE g.base_title IS NOT NULL AND g.base_title != ''
			  AND g.base_title IN (SELECT base_title FROM new_galleries)
		),
		multi AS (
			SELECT base_title FROM matching GROUP BY base_title HAVING COUNT(*) > 1
		),
		grouped AS (
			SELECT MIN(m.gid) OVER (PARTITION BY m.base_title) AS group_id, m.gid
			FROM matching m JOIN multi mu ON m.base_title = mu.base_title
		)
		INSERT INTO gallery_group_members (group_id, gid)
		SELECT group_id, gid FROM grouped
		ON CONFLICT (gid) DO UPDATE SET group_id = EXCLUDED.group_id
	`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (d *DB) GalleryGroupIsEmpty(ctx context.Context) (bool, error) {
	var exists bool
	err := d.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM gallery_group_members LIMIT 1)").Scan(&exists)
	return !exists, err
}

// GetGalleryByGID returns a gallery's basic info for incremental comparison.
func (d *DB) GetGalleryByGID(ctx context.Context, gid int64) (map[string]any, error) {
	var tagsJSON []byte
	var rating *float64
	err := d.pool.QueryRow(ctx,
		"SELECT tags, rating FROM eh_galleries WHERE gid = $1", gid,
	).Scan(&tagsJSON, &rating)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"rating": rating,
	}
	var tags map[string][]string
	_ = json.Unmarshal(tagsJSON, &tags)
	result["tags"] = tags
	return result, nil
}
