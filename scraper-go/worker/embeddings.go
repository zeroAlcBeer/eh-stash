package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/db"
)

const (
	embeddingBatchSize     = 500
	embeddingBatchInterval = 100 * time.Millisecond
	embeddingIdleBase      = 30 * time.Second
	embeddingIdleMax       = time.Hour
)

// RunEmbeddings is the continuous embeddings worker.
//
// Responsibilities:
//   - Bootstrap: if tag_vocabulary is empty, build it from current eh_galleries
//     and embed every active gallery.
//   - Incremental: poll for active galleries with NULL tag_embedding and embed
//     them in batches.
//   - On profileCh signal: recompute the user_profile vector (called after
//     a favorites sync detects changes).
//
// Idle behavior: exponential backoff up to 1h between empty polls.
func RunEmbeddings(
	ctx context.Context,
	database *db.DB,
	profileCh <-chan struct{},
) {
	slog.Info("[EMBED] embeddings worker started")

	// Bootstrap: if vocabulary is empty, build it.
	if err := bootstrapIfNeeded(ctx, database); err != nil {
		slog.Error("[EMBED] bootstrap failed", "error", err)
		// Continue anyway — vocab build may succeed later.
	}

	idle := 0
	for {
		select {
		case <-ctx.Done():
			slog.Info("[EMBED] embeddings worker stopped")
			return
		case <-profileCh:
			handleProfileUpdate(ctx, database)
			continue
		default:
		}

		gids, err := database.EmbedGalleriesBatch(ctx, embeddingBatchSize)
		if err != nil {
			slog.Error("[EMBED] embed batch failed", "error", err)
			sleep(ctx, 10*time.Second)
			continue
		}

		if len(gids) > 0 {
			idle = 0
			if err := database.RecomputeScoresForGIDs(ctx, gids); err != nil {
				slog.Error("[EMBED] recompute scores failed", "error", err, "batch", len(gids))
			}
			slog.Info("[EMBED] batch processed", "count", len(gids))
			sleep(ctx, embeddingBatchInterval)
			continue
		}

		// Idle backoff: 30s → 1m → 2m → ... → 1h
		idle++
		wait := embeddingIdleBase * time.Duration(1<<minInt(idle-1, 7))
		if wait > embeddingIdleMax {
			wait = embeddingIdleMax
		}
		slog.Info("[EMBED] idle", "wait", wait)

		select {
		case <-ctx.Done():
			return
		case <-profileCh:
			handleProfileUpdate(ctx, database)
			idle = 0
		case <-time.After(wait):
		}
	}
}

// handleProfileUpdate runs the full profile + all-scores recompute. Called on
// every ProfileUpdate signal from the favorites task.
func handleProfileUpdate(ctx context.Context, database *db.DB) {
	slog.Info("[EMBED] profile update signal received")
	if err := database.RebuildUserProfile(ctx); err != nil {
		slog.Error("[EMBED] rebuild user_profile failed", "error", err)
		return
	}
	slog.Info("[EMBED] user_profile rebuilt, recomputing all scores")
	if err := database.RecomputeAllScores(ctx); err != nil {
		slog.Error("[EMBED] recompute all scores failed", "error", err)
		return
	}
	slog.Info("[EMBED] all scores recomputed")
}

func bootstrapIfNeeded(ctx context.Context, database *db.DB) error {
	empty, err := database.VocabularyIsEmpty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}

	slog.Info("[EMBED] vocabulary empty, bootstrapping")
	added, deactivated, err := database.RebuildVocabulary(ctx)
	if err != nil {
		return err
	}
	slog.Info("[EMBED] vocabulary built", "added", added, "deactivated", deactivated)

	// Force re-embed all galleries by clearing tag_embedding.
	if err := database.ClearAllEmbeddings(ctx); err != nil {
		return err
	}

	// Embed will happen via the normal batch loop.
	if err := database.RebuildUserProfile(ctx); err != nil {
		slog.Warn("[EMBED] initial user_profile build failed (may have no favorites yet)", "error", err)
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
