package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/db"
)

const (
	scorerBatchSize     = 100
	scorerBatchInterval = 2 * time.Second
)

// RunRecommendedScorer incrementally computes recommended_cache.
// Only runs when triggered by resetCh (signal-driven, no polling).
func RunRecommendedScorer(ctx context.Context, database *db.DB, resetCh <-chan struct{}) {
	slog.Info("[SCORE] recommended scorer started")

	var cursor *int64

	for {
		// Wait for reset signal when idle
		if cursor == nil {
			select {
			case <-resetCh:
				slog.Info("[SCORE] reset: restarting from latest gid")
				cursor = nil
			case <-ctx.Done():
				slog.Info("[SCORE] scorer stopped")
				return
			}
		}

		// Check for reset signal (non-blocking)
		select {
		case <-resetCh:
			cursor = nil
			slog.Info("[SCORE] reset: restarting from latest gid")
		default:
		}

		gids, nextCursor, err := database.ScoreRecommendedBatch(ctx, cursor, scorerBatchSize)
		if err != nil {
			slog.Error("[SCORE] error", "error", err)
			sleep(ctx, 10*time.Second)
			continue
		}

		if len(gids) == 0 {
			if cursor != nil {
				slog.Info("[SCORE] full scan complete, idling until next reset signal")
				cursor = nil
			}
			continue // back to top — wait for resetCh
		}

		cursor = nextCursor

		select {
		case <-time.After(scorerBatchInterval):
		case <-ctx.Done():
			return
		}
	}
}
