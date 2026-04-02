package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/db"
)

const recommendThreshold = 20.0

// RunRecommendedScorerReverse is the reverse-lookup scorer algorithm.
// Instead of scanning all galleries and checking tags, it iterates over
// preference tags and uses the GIN index to find matching galleries.
// Scores are aggregated in memory, then bulk-written to recommended_cache.
//
// Signal-driven: only runs when resetCh fires.
func RunRecommendedScorerReverse(ctx context.Context, database *db.DB, resetCh <-chan struct{}) {
	slog.Info("[SCORE] reverse scorer started")

	for {
		select {
		case <-resetCh:
			slog.Info("[SCORE] reset signal received, starting reverse scoring")
		case <-ctx.Done():
			slog.Info("[SCORE] reverse scorer stopped")
			return
		}

		// Drain any extra signals
		for {
			select {
			case <-resetCh:
			default:
				goto run
			}
		}

	run:
		start := time.Now()

		tags, err := database.ListPreferenceTags(ctx)
		if err != nil {
			slog.Error("[SCORE] list preference tags failed", "error", err)
			sleep(ctx, 10*time.Second)
			continue
		}

		if len(tags) == 0 {
			slog.Info("[SCORE] no preference tags, skipping")
			continue
		}

		// For each preference tag, find matching galleries via GIN index
		scores := make(map[int64]float64)
		for _, tag := range tags {
			select {
			case <-ctx.Done():
				return
			default:
			}

			gids, err := database.FindGIDsByTag(ctx, tag.Namespace, tag.Tag)
			if err != nil {
				slog.Error("[SCORE] find GIDs by tag failed",
					"namespace", tag.Namespace, "tag", tag.Tag, "error", err)
				continue
			}

			for _, gid := range gids {
				scores[gid] += tag.Weight
			}
		}

		// Write to recommended_cache
		count, err := database.ReplaceRecommendedCache(ctx, scores, recommendThreshold)
		if err != nil {
			slog.Error("[SCORE] replace recommended cache failed", "error", err)
			continue
		}

		slog.Info("[SCORE] reverse scoring complete",
			"tags", len(tags),
			"candidates", len(scores),
			"recommended", count,
			"duration", time.Since(start).Round(time.Millisecond))
	}
}
