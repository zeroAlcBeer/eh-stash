package db

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	// EmbeddingDim is the sparsevec column dimension. Must match
	// migrations/007_cosine_recommendation.sql.
	EmbeddingDim = 65536

	// embeddingMinCount is the minimum number of active galleries that must
	// reference a (namespace, tag) pair for it to enter the vocabulary.
	embeddingMinCount = 3
)

// typeWeights maps eh tag namespaces to relative weights used in tag_embedding.
// Values are sqrt-scaled relative to intuitive importance, because cosine
// similarity squares each contribution in the dot product.
//
// Namespaces absent from this map fall back to 1.0. Add new ones explicitly.
var typeWeights = map[string]float64{
	"artist":    1.7,
	"group":     1.7,
	"parody":    1.4,
	"character": 1.2,
	"cosplayer": 1.2,
	"female":    0.6,
	"male":      0.6,
	"mixed":     0.6,
	"location":  0.4,
	"other":     0.4,
	"language":  0.2,
}

// excludedNamespaces are skipped entirely during vocabulary construction.
var excludedNamespaces = map[string]bool{
	"reclass": true,
	"temp":    true,
}

func typeWeightFor(namespace string) float64 {
	if w, ok := typeWeights[namespace]; ok {
		return w
	}
	return 1.0
}

// ─── sparsevec text format helpers ──────────────────────────────────────────

type sparseEntry struct {
	Index int // 1-based, matching pgvector convention
	Value float64
}

func formatSparseVec(entries []sparseEntry, dim int) string {
	if len(entries) == 0 {
		return fmt.Sprintf("{}/%d", dim)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Index < entries[j].Index })
	var b strings.Builder
	b.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(e.Index))
		b.WriteByte(':')
		b.WriteString(strconv.FormatFloat(e.Value, 'g', -1, 64))
	}
	b.WriteByte('}')
	b.WriteByte('/')
	b.WriteString(strconv.Itoa(dim))
	return b.String()
}

func normalizeSparse(entries []sparseEntry) []sparseEntry {
	var sum float64
	for _, e := range entries {
		sum += e.Value * e.Value
	}
	if sum == 0 {
		return entries
	}
	norm := math.Sqrt(sum)
	out := make([]sparseEntry, len(entries))
	for i, e := range entries {
		out[i] = sparseEntry{Index: e.Index, Value: e.Value / norm}
	}
	return out
}

// ─── Vocabulary ──────────────────────────────────────────────────────────────

type vocabEntry struct {
	Dim        int
	Idf        float64
	TypeWeight float64
}

type vocabulary struct {
	byKey    map[string]vocabEntry // key = namespace + "\x00" + tag
	dimCount int
}

func vocabKey(namespace, tag string) string {
	return namespace + "\x00" + tag
}

func (d *DB) loadVocabulary(ctx context.Context) (*vocabulary, error) {
	v := &vocabulary{byKey: make(map[string]vocabEntry)}

	rows, err := d.pool.Query(ctx,
		`SELECT dim, namespace, tag, idf, type_weight
		 FROM tag_vocabulary WHERE is_active = TRUE`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var dim int
		var ns, tag string
		var idf, tw float64
		if err := rows.Scan(&dim, &ns, &tag, &idf, &tw); err != nil {
			rows.Close()
			return nil, err
		}
		v.byKey[vocabKey(ns, tag)] = vocabEntry{Dim: dim, Idf: idf, TypeWeight: tw}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = d.pool.QueryRow(ctx,
		`SELECT dim_count FROM tag_vocabulary_meta WHERE id = 1`).Scan(&v.dimCount)
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return v, nil
}

// VocabularyIsEmpty reports whether tag_vocabulary has any rows.
func (d *DB) VocabularyIsEmpty(ctx context.Context) (bool, error) {
	var exists bool
	err := d.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tag_vocabulary LIMIT 1)`).Scan(&exists)
	return !exists, err
}

// ClearAllEmbeddings sets tag_embedding to NULL on every recommended_cache row.
// Used after a vocabulary rebuild that may have changed dim assignments. Also
// clears the dependent similarity column since it would be stale.
func (d *DB) ClearAllEmbeddings(ctx context.Context) error {
	_, err := d.pool.Exec(ctx,
		`UPDATE recommended_cache SET tag_embedding = NULL, similarity = NULL`)
	return err
}

// RebuildVocabulary scans active galleries, builds a new vocabulary with
// monotonic dim allocation. Existing tags get their idf/type_weight refreshed;
// tags that no longer qualify are marked is_active = FALSE but keep their dim
// slot so existing embeddings remain valid.
//
// Returns (newDimsAdded, deactivated).
func (d *DB) RebuildVocabulary(ctx context.Context) (int, int, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)

	var totalGalleries int64
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM eh_galleries WHERE is_active = TRUE`).Scan(&totalGalleries); err != nil {
		return 0, 0, err
	}
	if totalGalleries == 0 {
		return 0, 0, fmt.Errorf("vocabulary rebuild aborted: no active galleries")
	}

	var dimCount int
	if err := tx.QueryRow(ctx,
		`SELECT dim_count FROM tag_vocabulary_meta WHERE id = 1`).Scan(&dimCount); err != nil {
		return 0, 0, err
	}

	// Load existing vocab entries by (namespace, tag).
	existing := make(map[string]struct{})
	exRows, err := tx.Query(ctx, `SELECT namespace, tag FROM tag_vocabulary`)
	if err != nil {
		return 0, 0, err
	}
	for exRows.Next() {
		var ns, tag string
		if err := exRows.Scan(&ns, &tag); err != nil {
			exRows.Close()
			return 0, 0, err
		}
		existing[vocabKey(ns, tag)] = struct{}{}
	}
	exRows.Close()

	// Compute df for every (namespace, tag) over active galleries.
	type qualEntry struct {
		Namespace string
		Tag       string
		DF        int64
	}
	var qualifying []qualEntry
	qRows, err := tx.Query(ctx, `
		SELECT t.ns, tag_value, COUNT(DISTINCT g.gid)::BIGINT AS df
		FROM eh_galleries g,
		     jsonb_each(g.tags) AS t(ns, vals),
		     jsonb_array_elements_text(vals) AS tag_value
		WHERE g.is_active = TRUE
		GROUP BY t.ns, tag_value
		HAVING COUNT(DISTINCT g.gid) >= $1
		ORDER BY t.ns, tag_value
	`, embeddingMinCount)
	if err != nil {
		return 0, 0, err
	}
	for qRows.Next() {
		var q qualEntry
		if err := qRows.Scan(&q.Namespace, &q.Tag, &q.DF); err != nil {
			qRows.Close()
			return 0, 0, err
		}
		if excludedNamespaces[q.Namespace] {
			continue
		}
		qualifying = append(qualifying, q)
	}
	qRows.Close()
	if err := qRows.Err(); err != nil {
		return 0, 0, err
	}

	// Pass 1: insert new entries with monotonic dims.
	nextDim := dimCount
	var newCount int
	for _, q := range qualifying {
		if _, ok := existing[vocabKey(q.Namespace, q.Tag)]; ok {
			continue
		}
		if nextDim >= EmbeddingDim {
			return 0, 0, fmt.Errorf("vocabulary exceeds sparsevec dim cap %d", EmbeddingDim)
		}
		idf := math.Log(float64(totalGalleries) / math.Max(float64(q.DF), 1))
		_, err := tx.Exec(ctx,
			`INSERT INTO tag_vocabulary (dim, namespace, tag, idf, type_weight, is_active, updated_at)
			 VALUES ($1, $2, $3, $4, $5, TRUE, NOW())`,
			nextDim, q.Namespace, q.Tag, idf, typeWeightFor(q.Namespace))
		if err != nil {
			return 0, 0, err
		}
		nextDim++
		newCount++
	}

	// Pass 2: refresh existing qualifying entries.
	for _, q := range qualifying {
		if _, ok := existing[vocabKey(q.Namespace, q.Tag)]; !ok {
			continue
		}
		idf := math.Log(float64(totalGalleries) / math.Max(float64(q.DF), 1))
		_, err := tx.Exec(ctx,
			`UPDATE tag_vocabulary
			 SET idf = $1, type_weight = $2, is_active = TRUE, updated_at = NOW()
			 WHERE namespace = $3 AND tag = $4`,
			idf, typeWeightFor(q.Namespace), q.Namespace, q.Tag)
		if err != nil {
			return 0, 0, err
		}
	}

	// Pass 3: deactivate vocab entries that no longer qualify.
	// Rebuild qualifying set as a temp table for efficient anti-join.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE _qual (namespace TEXT, tag TEXT) ON COMMIT DROP`); err != nil {
		return 0, 0, err
	}
	if len(qualifying) > 0 {
		var values []string
		var args []any
		idx := 1
		for _, q := range qualifying {
			values = append(values, fmt.Sprintf("($%d,$%d)", idx, idx+1))
			args = append(args, q.Namespace, q.Tag)
			idx += 2
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO _qual (namespace, tag) VALUES `+strings.Join(values, ","), args...); err != nil {
			return 0, 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tag_vocabulary tv
		SET is_active = FALSE, updated_at = NOW()
		WHERE is_active = TRUE
		  AND NOT EXISTS (SELECT 1 FROM _qual q WHERE q.namespace = tv.namespace AND q.tag = tv.tag)
	`); err != nil {
		return 0, 0, err
	}

	var deactivated int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM tag_vocabulary WHERE is_active = FALSE`).Scan(&deactivated); err != nil {
		return 0, 0, err
	}
	var activeCount int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM tag_vocabulary WHERE is_active = TRUE`).Scan(&activeCount); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tag_vocabulary_meta
		SET dim_count = $1, active_count = $2, total_galleries = $3, updated_at = NOW()
		WHERE id = 1
	`, nextDim, activeCount, totalGalleries); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return newCount, deactivated, nil
}

// EmbedGalleriesBatch picks up to `limit` active galleries that have NULL
// tag_embedding in recommended_cache, computes their sparse embeddings, and
// writes them back in a single bulk UPDATE. Returns the gids that were
// embedded so the caller can recompute their similarity.
func (d *DB) EmbedGalleriesBatch(ctx context.Context, limit int) ([]int64, error) {
	vocab, err := d.loadVocabulary(ctx)
	if err != nil {
		return nil, err
	}
	if len(vocab.byKey) == 0 {
		return nil, nil
	}

	// Pending detection uses the partial index
	// idx_recommended_cache_embedding_pending so this is O(pending), not O(N).
	rows, err := d.pool.Query(ctx, `
		SELECT rc.gid, g.tags
		FROM recommended_cache rc
		JOIN eh_galleries g ON g.gid = rc.gid
		WHERE rc.tag_embedding IS NULL AND g.is_active = TRUE
		ORDER BY rc.gid DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}

	type galleryTags struct {
		GID  int64
		Tags map[string][]string
	}
	var pending []galleryTags
	for rows.Next() {
		var gid int64
		var tagsJSON []byte
		if err := rows.Scan(&gid, &tagsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		gt := galleryTags{GID: gid}
		_ = json.Unmarshal(tagsJSON, &gt.Tags)
		pending = append(pending, gt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	gids := make([]int64, len(pending))
	vecs := make([]string, len(pending))
	for i, gt := range pending {
		entries := make([]sparseEntry, 0, 16)
		for ns, vals := range gt.Tags {
			for _, tag := range vals {
				e, ok := vocab.byKey[vocabKey(ns, tag)]
				if !ok {
					continue
				}
				entries = append(entries, sparseEntry{
					Index: e.Dim + 1,
					Value: e.Idf * e.TypeWeight,
				})
			}
		}
		entries = normalizeSparse(entries)
		gids[i] = gt.GID
		vecs[i] = formatSparseVec(entries, EmbeddingDim)
	}

	_, err = d.pool.Exec(ctx, `
		UPDATE recommended_cache rc
		SET tag_embedding = v.vec::sparsevec, updated_at = NOW()
		FROM (SELECT * FROM unnest($1::bigint[], $2::text[]) AS x(gid, vec)) AS v
		WHERE rc.gid = v.gid
	`, gids, vecs)
	if err != nil {
		return nil, err
	}
	return gids, nil
}

// RecomputeAllScores updates recommended_cache.similarity for every row,
// using the current user_profile vector. Used after a profile rebuild.
//
// If the profile is empty (no favorites), all similarities are cleared.
func (d *DB) RecomputeAllScores(ctx context.Context) error {
	var profileReady bool
	err := d.pool.QueryRow(ctx,
		`SELECT embedding IS NOT NULL FROM user_profile WHERE id = 1`,
	).Scan(&profileReady)
	if err != nil {
		return err
	}

	if !profileReady {
		_, err := d.pool.Exec(ctx,
			`UPDATE recommended_cache SET similarity = NULL WHERE similarity IS NOT NULL`)
		return err
	}

	// NULLIF guards against zero-vector embeddings (cosine yields NaN).
	if _, err := d.pool.Exec(ctx, `
		UPDATE recommended_cache rc
		SET similarity = NULLIF(1 - (rc.tag_embedding <=> up.embedding), 'NaN'::float8),
		    updated_at = NOW()
		FROM user_profile up
		WHERE up.id = 1 AND rc.tag_embedding IS NOT NULL
	`); err != nil {
		return err
	}
	// Rows without an embedding cannot have a similarity.
	_, err = d.pool.Exec(ctx,
		`UPDATE recommended_cache SET similarity = NULL
		 WHERE tag_embedding IS NULL AND similarity IS NOT NULL`)
	return err
}

// RecomputeScoresForGIDs updates similarity only for the given gids.
// Called by the embeddings worker after each batch so newly-embedded galleries
// immediately become rankable.
func (d *DB) RecomputeScoresForGIDs(ctx context.Context, gids []int64) error {
	if len(gids) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		UPDATE recommended_cache rc
		SET similarity = NULLIF(1 - (rc.tag_embedding <=> up.embedding), 'NaN'::float8),
		    updated_at = NOW()
		FROM user_profile up
		WHERE up.id = 1
		  AND up.embedding IS NOT NULL
		  AND rc.gid = ANY($1)
		  AND rc.tag_embedding IS NOT NULL
	`, gids)
	return err
}

// RebuildUserProfile recomputes user_profile directly from user_favorites,
// eh_galleries.tags, and tag_vocabulary using a TF-IDF style formula:
//
//	profile[X] = idf(X) * type_weight(X) * (1 + ln(tf_favorites(X)))
//
// then L2-normalized. The (1 + ln(tf)) sub-linear damping prevents common
// tags (e.g. female:schoolgirl_uniform present in many favorites) from
// accumulating disproportionate weight and dominating the recommendation,
// which would otherwise drown out rarer but more discriminating signals
// (specific artist/character tags).
//
// This is independent of tag_embedding — profile is usable immediately after
// favorites sync, without waiting for any embedding worker to catch up.
func (d *DB) RebuildUserProfile(ctx context.Context) error {
	// Total favorited galleries that are still active (this is what we report
	// as liked_count to the UI).
	var likedCount int
	if err := d.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM user_favorites f
		JOIN eh_galleries g ON g.gid = f.gid
		WHERE g.is_active = TRUE
	`).Scan(&likedCount); err != nil {
		return err
	}

	if likedCount == 0 {
		_, err := d.pool.Exec(ctx,
			`UPDATE user_profile SET embedding = NULL, liked_count = 0, updated_at = NOW() WHERE id = 1`)
		return err
	}

	// Per-dim: how many favorited galleries contain this (namespace, tag)?
	// Join to tag_vocabulary brings in idf + type_weight + dim.
	rows, err := d.pool.Query(ctx, `
		SELECT v.dim, v.idf, v.type_weight, tf.cnt
		FROM (
			SELECT t.ns, tag_value, COUNT(*)::INT AS cnt
			FROM user_favorites f
			JOIN eh_galleries g ON g.gid = f.gid,
			     jsonb_each(g.tags) AS t(ns, vals),
			     jsonb_array_elements_text(vals) AS tag_value
			WHERE g.is_active = TRUE
			GROUP BY t.ns, tag_value
		) tf
		JOIN tag_vocabulary v
		  ON v.namespace = tf.ns AND v.tag = tf.tag_value AND v.is_active = TRUE
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	entries := make([]sparseEntry, 0, 256)
	for rows.Next() {
		var dim int
		var idf, tw float64
		var tf int
		if err := rows.Scan(&dim, &idf, &tw, &tf); err != nil {
			return err
		}
		value := idf * tw * (1.0 + math.Log(float64(tf)))
		entries = append(entries, sparseEntry{Index: dim + 1, Value: value})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(entries) == 0 {
		// User has favorites but none of their tags qualify for the vocab.
		_, err := d.pool.Exec(ctx,
			`UPDATE user_profile SET embedding = NULL, liked_count = $1, updated_at = NOW() WHERE id = 1`,
			likedCount)
		return err
	}

	entries = normalizeSparse(entries)
	lit := formatSparseVec(entries, EmbeddingDim)

	_, err = d.pool.Exec(ctx, `
		UPDATE user_profile
		SET embedding = $1::sparsevec, liked_count = $2, updated_at = NOW()
		WHERE id = 1
	`, lit, likedCount)
	return err
}
