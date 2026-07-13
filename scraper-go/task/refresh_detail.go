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
// Progress is tracked from successful writes and the authoritative remaining
// count. A keyset cursor walks a pass without skipping rows as the pending set
// shrinks; failed rows are retried when the next pass resets the cursor.
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
	totalBefore, pendingBefore, err := database.GetGalleryRefreshCoverage(ctx, minFav)
	if err != nil {
		slog.Error("[RFRSH] coverage count failed", "name", name, "error", err)
		return RefreshDetailResult{Checkpoint: checkpoint}, fmt.Errorf("count gallery refresh coverage: %w", err)
	}
	completedBefore := totalBefore - pendingBefore

	var reset bool
	checkpoint, reset = ensureRefreshCheckpoint(checkpoint, minFav, completedBefore, pendingBefore)
	if reset {
		slog.Info("[RFRSH] checkpoint reset for scope",
			"name", name,
			"min_fav", minFav,
			"total_pending", pendingBefore,
		)
	}
	totalDone := completedBefore
	checkpoint["total_done"] = float64(totalDone)
	checkpoint["total_pending"] = float64(pendingBefore)
	cursorFav, cursorGID := refreshCursor(checkpoint)

	result := RefreshDetailResult{
		Checkpoint: checkpoint,
		Pct:        calcRefreshProgress(totalDone, pendingBefore),
	}

	slog.Info("[RFRSH] batch start",
		"name", name,
		"cursor_fav", cursorFav,
		"cursor_gid", cursorGID,
		"pass", getStateInt(checkpoint, "pass"),
		"total_done", totalDone,
		"total_pending", pendingBefore,
		"batch_size", batchSize,
		"min_fav", minFav,
	)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	if pendingBefore == 0 {
		checkpoint["total_pending"] = float64(0)
		result.ExitReason = "DONE"
		result.Pct = 100
		return result, nil
	}

	// Fetch candidates from DB using a stable keyset cursor.
	candidates, err := database.GetGalleriesNeedingRefresh(ctx, minFav, batchSize, cursorFav, cursorGID)
	if err != nil {
		slog.Error("[RFRSH] query candidates failed", "name", name, "error", err)
		result.ExitReason = "ERROR"
		return result, nil
	}

	if len(candidates) == 0 {
		// Remaining rows exist, so this pass only reached its end. Reset the
		// cursor and let the next periodic tick retry failures from the top.
		delete(checkpoint, "cursor_fav")
		delete(checkpoint, "cursor_gid")
		checkpoint["pass"] = float64(getStateInt(checkpoint, "pass") + 1)
		checkpoint["total_pending"] = float64(pendingBefore)
		result.Pct = calcRefreshProgress(totalDone, pendingBefore)
		slog.Info("[RFRSH] pass complete with pending rows, cursor reset",
			"name", name,
			"total_done", totalDone,
			"total_pending", pendingBefore,
			"next_pass", getStateInt(checkpoint, "pass"),
		)
		return result, nil
	}

	var rowsToUpsert []db.GalleryRow
	var commentBatches []CommentBatch
	nOK, nInactive, nFail := 0, 0, 0
	banned := false
	var lastScanned *db.RefreshCandidate

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
			if detailResult == client.ResultNotFound {
				if err := database.MarkGalleryInactive(ctx, c.GID); err != nil {
					slog.Error("[RFRSH] mark missing gallery inactive failed",
						"name", name,
						"gid", c.GID,
						"error", err,
					)
					nFail++
				} else {
					nInactive++
				}
				lastScanned = &db.RefreshCandidate{GID: c.GID, FavCount: c.FavCount}
				continue
			}
			lastScanned = &db.RefreshCandidate{GID: c.GID, FavCount: c.FavCount}
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
			lastScanned = &db.RefreshCandidate{GID: c.GID, FavCount: c.FavCount}
			continue
		}

		rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(c.GID, c.Token, detail, true))
		commentBatches = append(commentBatches, CommentBatch{
			GID:      c.GID,
			Comments: BuildCommentRows(c.GID, detail.Comments),
		})
		nOK++
		lastScanned = &db.RefreshCandidate{GID: c.GID, FavCount: c.FavCount}
		slog.Info("[RFRSH] item ok",
			"name", name,
			"i", i+1,
			"total", len(candidates),
			"gid", c.GID,
			"fav", c.FavCount,
			"latency", detailLatency,
		)
	}

	// Persist successful rows even when a later request in the same batch is
	// banned. Otherwise work completed before the ban would be lost.
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

	if lastScanned != nil {
		checkpoint["cursor_fav"] = float64(lastScanned.FavCount)
		checkpoint["cursor_gid"] = float64(lastScanned.GID)
	}

	totalAfter, pendingAfter, err := database.GetGalleryRefreshCoverage(ctx, minFav)
	if err != nil {
		return result, fmt.Errorf("count gallery refresh coverage after batch: %w", err)
	}
	totalDone = totalAfter - pendingAfter
	checkpoint["total_done"] = float64(totalDone)
	checkpoint["total_pending"] = float64(pendingAfter)
	result.Pct = calcRefreshProgress(totalDone, pendingAfter)

	if pendingAfter == 0 {
		result.ExitReason = "DONE"
		result.Pct = 100
	} else if banned {
		result.ExitReason = "BANNED"
		slog.Warn("[RFRSH] batch interrupted by ban",
			"name", name,
			"ok", nOK,
			"inactive", nInactive,
			"fail", nFail,
			"total_pending", pendingAfter,
		)
	}

	slog.Info("[RFRSH] batch summary",
		"name", name,
		"batch", len(candidates),
		"ok", nOK,
		"inactive", nInactive,
		"fail", nFail,
		"cursor_fav", checkpoint["cursor_fav"],
		"cursor_gid", checkpoint["cursor_gid"],
		"total_done", totalDone,
		"total_pending", pendingAfter,
		"pct", result.Pct,
	)

	return result, nil
}

func ensureRefreshCheckpoint(checkpoint map[string]any, minFav, completed, pending int) (map[string]any, bool) {
	scope, hasScope := checkpoint["scope_min_fav"]
	scopeFav, scopeOK := scope.(float64)
	if !hasScope || !scopeOK || int(scopeFav) != minFav {
		return map[string]any{
			"scope_min_fav": float64(minFav),
			"pass":          float64(1),
			"total_done":    float64(completed),
			"total_pending": float64(pending),
		}, true
	}

	// Remove the legacy shrinking-set pagination state if it survived an
	// upgrade with the same scope.
	delete(checkpoint, "offset")
	if getStateInt(checkpoint, "pass") < 1 {
		checkpoint["pass"] = float64(1)
	}
	checkpoint["total_done"] = float64(completed)
	checkpoint["total_pending"] = float64(pending)
	return checkpoint, false
}

func refreshCursor(checkpoint map[string]any) (*int, *int64) {
	gid := getStateInt(checkpoint, "cursor_gid")
	if gid <= 0 {
		return nil, nil
	}
	fav := getStateInt(checkpoint, "cursor_fav")
	gid64 := int64(gid)
	return &fav, &gid64
}

func calcRefreshProgress(done, pending int) float64 {
	if pending <= 0 {
		if done > 0 {
			return 100
		}
		return 0
	}
	total := done + pending
	if total <= 0 {
		return 0
	}
	return ClampProgress(float64(done) / float64(total) * 100)
}
