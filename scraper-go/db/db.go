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
		tags = EXCLUDED.tags, last_synced_at = NOW(),
		is_active = eh_galleries.is_active AND EXCLUDED.is_active`

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

	// Maintain invariant: every gallery row has a matching recommended_cache row
	// so the embeddings worker can pick it up via the partial index without
	// needing a LEFT JOIN.
	var rcValues []string
	var rcArgs []any
	rcIdx := 1
	for _, r := range rows {
		if !r.IsActive {
			continue
		}
		rcValues = append(rcValues, fmt.Sprintf("($%d)", rcIdx))
		rcArgs = append(rcArgs, r.GID)
		rcIdx++
	}
	if len(rcValues) > 0 {
		_, err = tx.Exec(ctx,
			`INSERT INTO recommended_cache (gid) VALUES `+strings.Join(rcValues, ",")+
				` ON CONFLICT (gid) DO NOTHING`,
			rcArgs...)
		if err != nil {
			return 0, fmt.Errorf("upsert recommended_cache: %w", err)
		}
	}

	// Outbox for pi-sync. PK on gid coalesces repeated upserts; pi-sync's
	// optimistic delete uses enqueued_at to detect concurrent updates.
	gids := make([]int64, len(rows))
	for i, r := range rows {
		gids[i] = r.GID
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO sync_outbox (gid, enqueued_at)
		 SELECT unnest($1::bigint[]), NOW()
		 ON CONFLICT (gid) DO UPDATE SET enqueued_at = NOW()`,
		gids)
	if err != nil {
		return 0, fmt.Errorf("upsert sync_outbox: %w", err)
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

func (d *DB) ResetInterruptedStoppingTasks(ctx context.Context) (int64, error) {
	tag, err := d.pool.Exec(ctx, `
		UPDATE sync_tasks
		SET status = 'stopped', updated_at = NOW()
		WHERE status = 'running'
		  AND desired_status = 'stopped'
	`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
