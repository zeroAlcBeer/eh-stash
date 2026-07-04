package task

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/parser"
)

// RefreshDetailResult is what RunRefreshDetailBatch returns to the scheduler.
type RefreshDetailResult struct {
	ExitReason string // "" = continue, "DONE" = all refreshed, "BANNED"/"ERROR" = pause
	Checkpoint map[string]any
	Pct        float64
}

// RunRefreshDetailBatch fetches detail pages for up to batchSize galleries
// that have file_size IS NULL (old-style rows predating 006_detail_extras).
// It reads candidates directly from the DB — no list page scraping needed.
// Progress is tracked via offset in the checkpoint.
func RunRefreshDetailBatch(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	def *db.TaskDef,
	grouperTrigger chan struct{},
) (RefreshDetailResult, error) {
	name := def.Name

	// Config
	batchSize := 25
	if v, ok := def.Config["batch_size"]; ok {
		if f, ok := v.(float64); ok && f > 0 {
			batchSize = int(f)
		}
	}
	minFav := 0
	if v, ok := def.Config["min_fav"]; ok {
		if f, ok := v.(float64); ok {
			minFav = int(f)
		}
	}

	checkpoint := cloneState(def.Checkpoint)
	offset := getStateInt(checkpoint, "offset")
	totalDone := getStateInt(checkpoint, "total_done")

	// Get total count for progress
	totalCount, err := database.CountGalleriesNeedingRefresh(ctx, minFav)
	if err != nil {
		slog.Error("[RFRSH] count failed", "name", name, "error", err)
	}
	pctFor := func(done int) float64 {
		if totalCount <= 0 {
			return 0
		}
		return ClampProgress(float64(done) / float64(totalCount) * 100)
	}

	result := RefreshDetailResult{Checkpoint: checkpoint, Pct: pctFor(totalDone)}

	slog.Info("[RFRSH] batch start",
		"name", name,
		"offset", offset,
		"total_done", totalDone,
		"total_pending", totalCount,
		"batch_size", batchSize,
		"min_fav", minFav,
	)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Fetch candidates from DB
	candidates, err := database.GetGalleriesNeedingRefresh(ctx, minFav, batchSize, offset)
	if err != nil {
		slog.Error("[RFRSH] query candidates failed", "name", name, "error", err)
		result.ExitReason = "ERROR"
		return result, nil
	}

	if len(candidates) == 0 {
		slog.Info("[RFRSH] no more candidates, round complete", "name", name, "total_done", totalDone)
		result.ExitReason = "DONE"
		result.Pct = 100
		return result, nil
	}

	var rowsToUpsert []db.GalleryRow
	var commentBatches []CommentBatch
	nOK, nFail := 0, 0
	banned := false

	for i, c := range candidates {
		if err := ctx.Err(); err != nil {
			slog.Warn("[RFRSH] batch cancelled",
				"name", name,
				"i", i+1,
				"total", len(candidates),
				"ok_so_far", nOK,
				"fail_so_far", nFail,
			)
			return result, err
		}

		detailURL := BuildDetailURL(httpClient.BaseURL(), c.GID, c.Token)
		detailStart := time.Now()
		detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
		detailLatency := time.Since(detailStart)

		if err != nil || detailResult != client.ResultOK {
			slog.Warn("[RFRSH] detail fetch failed",
				"name", name,
				"i", i+1,
				"total", len(candidates),
				"gid", c.GID,
				"fav", c.FavCount,
				"latency", detailLatency,
				"result", detailResult,
				"error", err,
			)
			if detailResult == client.ResultBanned {
				banned = true
				break
			}
			nFail++
			continue
		}

		detail, err := parser.ParseDetail(detailBody)
		if err != nil || detail == nil {
			slog.Warn("[RFRSH] detail parse failed",
				"name", name,
				"i", i+1,
				"gid", c.GID,
				"error", err,
			)
			nFail++
			continue
		}

		rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(c.GID, c.Token, detail, true))
		commentBatches = append(commentBatches, CommentBatch{
			GID:      c.GID,
			Comments: BuildCommentRows(c.GID, detail.Comments),
		})
		nOK++
		slog.Info("[RFRSH] item ok",
			"name", name,
			"i", i+1,
			"total", len(candidates),
			"gid", c.GID,
			"fav", c.FavCount,
			"latency", detailLatency,
		)
	}

	if banned {
		slog.Warn("[RFRSH] batch interrupted by ban",
			"name", name,
			"ok", nOK,
			"fail", nFail,
		)
		result.ExitReason = "BANNED"
		return result, nil
	}

	// Upsert
	if len(rowsToUpsert) > 0 {
		if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
			slog.Error("[RFRSH] upsert failed",
				"name", name,
				"rows", len(rowsToUpsert),
				"error", err,
			)
			return result, fmt.Errorf("upsert galleries: %w", err)
		}
		FlushCommentBatches(ctx, database, commentBatches)
		notify(grouperTrigger)
	}

	// Advance offset by the full batch size (not just nOK), so failed
	// items don't get stuck in a retry loop — they'll be retried on the
	// next round if still NULL.
	offset += len(candidates)
	totalDone += nOK
	checkpoint["offset"] = float64(offset)
	checkpoint["total_done"] = float64(totalDone)
	checkpoint["total_pending"] = float64(totalCount)

	result.Pct = pctFor(totalDone)

	slog.Info("[RFRSH] batch summary",
		"name", name,
		"batch", len(candidates),
		"ok", nOK,
		"fail", nFail,
		"offset", offset,
		"total_done", totalDone,
		"total_pending", totalCount,
		"pct", result.Pct,
	)

	return result, nil
}
