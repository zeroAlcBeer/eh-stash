package task

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/parser"
)

// FullCheckpoint is the typed shape of a full sync task's checkpoint JSONB.
// Like incremental, one round is a run_id-chained series of one-page slices.
// Unlike incremental (periodic), full is manual: BANNED/ERROR must not drop
// the cursor — the worker keeps the chain alive with a delayed next slice.
type FullCheckpoint struct {
	RunID      string  // "" = no chain in flight
	NextGID    *string // EH list pagination cursor; nil = start of list
	Round      int
	Done       bool
	AnchorGID  int64 // max gid on the first page of the round; 0 = unset
	TotalCount int   // best-known category total from list pages; 0 = unknown
	DBCount    int   // galleries already in DB for this category; 0 = unknown
}

func FullCheckpointFromMap(m map[string]any) FullCheckpoint {
	cp := FullCheckpoint{
		NextGID:    getStateString(m, "next_gid"),
		Round:      getStateInt(m, "round"),
		Done:       getStateBool(m, "done"),
		AnchorGID:  int64(getStateFloat(m, "anchor_gid")),
		TotalCount: getStateInt(m, "total_count"),
		DBCount:    getStateInt(m, "db_count"),
	}
	if s, ok := m["run_id"].(string); ok {
		cp.RunID = s
	}
	return cp
}

// ToMap renders the exact JSONB shape the API and frontend read, explicit
// nulls included.
func (c FullCheckpoint) ToMap() map[string]any {
	m := map[string]any{
		"run_id":      nil,
		"next_gid":    nil,
		"anchor_gid":  nil,
		"total_count": nil,
		"db_count":    nil,
		"round":       c.Round,
		"done":        c.Done,
	}
	if c.RunID != "" {
		m["run_id"] = c.RunID
	}
	if c.NextGID != nil {
		m["next_gid"] = *c.NextGID
	}
	if c.AnchorGID > 0 {
		m["anchor_gid"] = c.AnchorGID
	}
	if c.TotalCount > 0 {
		m["total_count"] = c.TotalCount
	}
	if c.DBCount > 0 {
		m["db_count"] = c.DBCount
	}
	return m
}

// FullSliceStats summarizes one slice's page for the task event timeline.
type FullSliceStats struct {
	Items    int
	Upserted int
	Deleted  int
}

// FullSliceResult is what RunFullSlice returns to the worker so it can decide
// whether to chain the next slice immediately, resume later (BANNED/ERROR), or
// finalize the round.
type FullSliceResult struct {
	ExitReason string // "" = continue, "END" = round complete, "BANNED"/"ERROR" = delayed resume
	Checkpoint FullCheckpoint
	Pct        float64
	Stats      FullSliceStats
}

// RunFullSlice fetches exactly one page of the full category scan and detail-
// fetches every item on it. The worker persists the checkpoint and decides the
// next action. Stop is signaled via ctx cancellation.
func RunFullSlice(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	def *db.TaskDef,
	grouperTrigger chan struct{},
) (FullSliceResult, error) {
	name := def.Name
	category := fullCategory(def)

	cp := FullCheckpointFromMap(def.Checkpoint)
	result := FullSliceResult{Checkpoint: cp, Pct: fullProgress(ctx, database, cp, category)}

	slog.Info(fmt.Sprintf("[FULL ] [%s] category=%s fetching", name, category),
		"next_gid", cp.NextGID)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	listURL := BuildListURL(httpClient.BaseURL(), []string{category}, cp.NextGID)
	body, fetchResult, err := httpClient.FetchPage(ctx, listURL)
	if err != nil {
		slog.Warn(fmt.Sprintf("[FULL ] [%s] fetch_list_page failed", name), "error", err)
		result.ExitReason = "ERROR"
		return result, nil
	}
	if fetchResult == client.ResultBanned {
		slog.Warn(fmt.Sprintf("[FULL ] [%s] IP temporarily banned", name))
		result.ExitReason = "BANNED"
		return result, nil
	}

	listResult, err := parser.ParseGalleryList(body)
	if err != nil {
		slog.Error(fmt.Sprintf("[FULL ] [%s] parse list page failed", name), "error", err)
		result.ExitReason = "ERROR"
		return result, nil
	}

	if len(listResult.Items) > 0 && result.Checkpoint.AnchorGID == 0 {
		maxGID := listResult.Items[0].GID
		for _, item := range listResult.Items {
			if item.GID > maxGID {
				maxGID = item.GID
			}
		}
		result.Checkpoint.AnchorGID = maxGID
	}

	if listResult.TotalCount != nil && *listResult.TotalCount > result.Checkpoint.TotalCount {
		result.Checkpoint.TotalCount = *listResult.TotalCount
	}

	slog.Info(fmt.Sprintf("[FULL ] [%s] category=%s page_items=%d next_gid=%v total_count=%d",
		name, category, len(listResult.Items), listResult.NextCursor, result.Checkpoint.TotalCount))

	var rowsToUpsert []db.GalleryRow
	var commentBatches []CommentBatch
	nDeleted := 0

	for _, item := range listResult.Items {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		if item.IsDeleted {
			nDeleted++
		}

		detailURL := BuildDetailURL(httpClient.BaseURL(), item.GID, item.Token)
		detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
		if err != nil {
			slog.Warn(fmt.Sprintf("[FULL ] [%s] gid=%d detail fetch failed", name, item.GID), "error", err)
			continue
		}
		if detailResult == client.ResultBanned {
			slog.Warn(fmt.Sprintf("[FULL ] [%s] gid=%d IP banned during detail fetch", name, item.GID))
			result.ExitReason = "BANNED"
			return result, nil
		}

		detail, err := parser.ParseDetail(detailBody)
		if err != nil || detail == nil {
			slog.Warn(fmt.Sprintf("[FULL ] [%s] gid=%d detail parse failed, skipping", name, item.GID))
			continue
		}

		row := BuildUpsertRow(item.GID, item.Token, detail, !item.IsDeleted)
		rowsToUpsert = append(rowsToUpsert, row)
		commentBatches = append(commentBatches, CommentBatch{
			GID:      item.GID,
			Comments: BuildCommentRows(item.GID, detail.Comments),
		})
	}

	result.Stats = FullSliceStats{Items: len(listResult.Items), Upserted: len(rowsToUpsert), Deleted: nDeleted}

	slog.Info(fmt.Sprintf("[FULL ] [%s] page_items=%d upsert=%d deleted=%d",
		name, len(listResult.Items), len(rowsToUpsert), nDeleted))

	if len(rowsToUpsert) > 0 {
		if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
			return result, fmt.Errorf("upsert galleries: %w", err)
		}
		FlushCommentBatches(ctx, database, commentBatches)
		notify(grouperTrigger)
	}

	if len(listResult.Items) == 0 || listResult.NextCursor == nil {
		result.ExitReason = "END"
		result.Pct = 100
		return result, nil
	}

	result.Checkpoint.NextGID = listResult.NextCursor

	dbCount, _ := database.CountGalleriesByCategory(ctx, category)
	result.Checkpoint.DBCount = dbCount
	result.Pct = fullProgress(ctx, database, result.Checkpoint, category)

	slog.Info(fmt.Sprintf("[FULL ] [%s] upserted=%d db_count=%d progress=%.2f%%",
		name, len(rowsToUpsert), dbCount, result.Pct))

	return result, nil
}

func fullCategory(def *db.TaskDef) string {
	if s, ok := def.Scope["category"].(string); ok {
		return s
	}
	return ""
}

// fullProgress is a best-effort pct: db coverage of the best-known category
// total. Returns 0 while the total is still unknown.
func fullProgress(ctx context.Context, database *db.DB, cp FullCheckpoint, category string) float64 {
	if cp.TotalCount <= 0 {
		return 0
	}
	dbCount, _ := database.CountGalleriesByCategory(ctx, category)
	tc := cp.TotalCount
	return CalcFullProgress(dbCount, &tc, false)
}
