package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/db"
)

const grouperIdleInterval = 60 * time.Second

// RunGalleryGrouper maintains gallery_group_members.
// Event-driven: runs incremental grouping on each trigger signal.
// With base_title indexed column, each run is <20ms — no debounce needed.
func RunGalleryGrouper(ctx context.Context, database *db.DB, triggerCh <-chan struct{}) {
	slog.Info("[GROUP] gallery grouper started")

	// Initial run
	empty, err := database.GalleryGroupIsEmpty(ctx)
	if err != nil {
		slog.Error("[GROUP] check empty failed", "error", err)
	} else if empty {
		count, err := database.GalleryGroupFullRebuild(ctx)
		if err != nil {
			slog.Error("[GROUP] full rebuild failed", "error", err)
		} else {
			slog.Info("[GROUP] full rebuild complete", "rows", count)
		}
	} else {
		count, err := database.GalleryGroupIncremental(ctx)
		if err != nil {
			slog.Error("[GROUP] startup incremental failed", "error", err)
		} else if count > 0 {
			slog.Info("[GROUP] startup incremental", "rows", count)
		}
	}

	for {
		select {
		case <-triggerCh:
			// triggered
		case <-time.After(grouperIdleInterval):
			continue
		case <-ctx.Done():
			slog.Info("[GROUP] grouper stopped")
			return
		}

		count, err := database.GalleryGroupIncremental(ctx)
		if err != nil {
			slog.Error("[GROUP] incremental error", "error", err)
			sleep(ctx, 10*time.Second)
			continue
		}
		if count > 0 {
			slog.Info("[GROUP] incremental", "rows", count)
		}
	}
}
