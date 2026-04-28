package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/client"
	"github.com/CheerChen/eh-stash/scraper-go/config"
	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/ratelimit"
)

const (
	thumbIdleSleep = 30 * time.Second
	thumbMaxRetries = 10
)

// RunThumbWorker continuously downloads thumbnails from the queue.
// Signal-driven: blocks on notifyCh when queue is empty.
func RunThumbWorker(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	cfg *config.Config,
	limiter *ratelimit.SimpleLimiter,
	notifyCh <-chan struct{},
) {
	slog.Info("[THUMB] thumb worker started")

	// Ensure thumb directory exists
	if err := os.MkdirAll(cfg.ThumbDir, 0755); err != nil {
		slog.Error("[THUMB] create thumb dir failed", "error", err)
		return
	}

	// Reset stale processing items
	count, err := database.ResetStaleThumbProcessing(ctx)
	if err != nil {
		slog.Error("[THUMB] reset stale processing failed", "error", err)
	} else if count > 0 {
		slog.Info("[THUMB] reset stale processing items", "count", count)
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("[THUMB] worker stopped")
			return
		default:
		}

		if err := limiter.Acquire(ctx); err != nil {
			return // context cancelled
		}

		item, err := database.ClaimNextThumbQueueItem(ctx)
		if err != nil {
			slog.Error("[THUMB] claim item failed", "error", err)
			sleep(ctx, 10*time.Second)
			continue
		}

		if item == nil {
			// Queue empty — wait for signal or timeout
			select {
			case <-notifyCh:
				continue
			case <-time.After(thumbIdleSleep):
				continue
			case <-ctx.Done():
				return
			}
		}

		// Download thumbnail
		data, statusCode, err := httpClient.FetchThumb(ctx, item.ThumbURL)
		if err != nil {
			slog.Warn(fmt.Sprintf("[THUMB] gid=%d download failed", item.GID), "error", err)
			database.MarkThumbFailed(ctx, item.ID, thumbMaxRetries)
			continue
		}

		switch {
		case statusCode == 200:
			path := filepath.Join(cfg.ThumbDir, fmt.Sprintf("%d", item.GID))
			if err := os.WriteFile(path, data, 0644); err != nil {
				slog.Error(fmt.Sprintf("[THUMB] gid=%d write failed", item.GID), "error", err)
				database.MarkThumbFailed(ctx, item.ID, thumbMaxRetries)
				continue
			}
			database.MarkThumbDone(ctx, item.ID)

		case statusCode == 404:
			slog.Warn(fmt.Sprintf("[THUMB] gid=%d 404 permanent fail", item.GID))
			database.MarkThumbPermanentFailed(ctx, item.ID)

		default:
			slog.Warn(fmt.Sprintf("[THUMB] gid=%d HTTP %d", item.GID, statusCode))
			database.MarkThumbFailed(ctx, item.ID, thumbMaxRetries)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
