package task

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/parser"
)

// RunFullOnce runs one iteration of the full sync task. Returns (done, error);
// done=true means the round completed and the manual-schedule def should be
// disabled by the caller (MarkTaskDefFinished with terminal=true).
func RunFullOnce(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	def *db.TaskDef,
	grouperTrigger chan struct{},
) (bool, error) {
	name := def.Name
	category := fullCategory(def)
	taskID := def.ID

	checkpoint := cloneState(def.Checkpoint)

	if getStateBool(checkpoint, "done") {
		checkpoint = map[string]any{
			"next_gid":    nil,
			"round":       0,
			"done":        false,
			"anchor_gid":  nil,
			"total_count": nil,
		}
	}

	nextCursor := getStateString(checkpoint, "next_gid")

	slog.Info(fmt.Sprintf("[FULL ] [%s] category=%s fetching", name, category),
		"next_gid", nextCursor)

	listURL := BuildListURL(httpClient.BaseURL(), []string{category}, nextCursor)
	body, result, err := httpClient.FetchPage(ctx, listURL)
	if err != nil {
		slog.Warn(fmt.Sprintf("[FULL ] [%s] fetch_list_page failed, will retry", name), "error", err)
		_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, fullProgress(checkpoint, category, database, ctx), "", true)
		return false, nil
	}

	if result == client.ResultBanned {
		slog.Warn(fmt.Sprintf("[FULL ] [%s] IP temporarily banned", name))
		_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "IP temporarily banned, will retry when ban expires", false)
		return false, nil
	}

	listResult, err := parser.ParseGalleryList(body)
	if err != nil {
		slog.Error(fmt.Sprintf("[FULL ] [%s] parse list page failed", name), "error", err)
		_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "", true)
		return false, nil
	}

	if len(listResult.Items) > 0 && checkpoint["anchor_gid"] == nil {
		maxGID := listResult.Items[0].GID
		for _, item := range listResult.Items {
			if item.GID > maxGID {
				maxGID = item.GID
			}
		}
		checkpoint["anchor_gid"] = float64(maxGID)
	}

	if listResult.TotalCount != nil {
		existing := getStateInt(checkpoint, "total_count")
		if *listResult.TotalCount > existing {
			checkpoint["total_count"] = float64(*listResult.TotalCount)
		}
	}

	slog.Info(fmt.Sprintf("[FULL ] [%s] category=%s page_items=%d next_gid=%v total_count=%v",
		name, category, len(listResult.Items), listResult.NextCursor, checkpoint["total_count"]))

	var rowsToUpsert []db.GalleryRow
	nDeleted := 0

	for _, item := range listResult.Items {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
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
			_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "IP temporarily banned, will retry when ban expires", false)
			return false, nil
		}

		detail, err := parser.ParseDetail(detailBody)
		if err != nil || detail == nil {
			slog.Warn(fmt.Sprintf("[FULL ] [%s] gid=%d detail parse failed, skipping", name, item.GID))
			continue
		}

		row := BuildUpsertRow(item.GID, item.Token, detail, !item.IsDeleted)
		rowsToUpsert = append(rowsToUpsert, row)
	}

	slog.Info(fmt.Sprintf("[FULL ] [%s] page_items=%d upsert=%d deleted=%d",
		name, len(listResult.Items), len(rowsToUpsert), nDeleted))

	if len(rowsToUpsert) > 0 {
		if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
			return false, fmt.Errorf("upsert galleries: %w", err)
		}
		notify(grouperTrigger)
	}

	done := len(listResult.Items) == 0 || listResult.NextCursor == nil
	roundNum := getStateInt(checkpoint, "round")

	if done {
		checkpoint["next_gid"] = nil
		checkpoint["round"] = float64(roundNum + 1)
		checkpoint["done"] = true
		_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 100, "", true)
		slog.Info(fmt.Sprintf("[FULL ] [%s] completed round=%d", name, roundNum+1))
		return true, nil
	}

	checkpoint["next_gid"] = *listResult.NextCursor
	checkpoint["done"] = false

	dbCount, _ := database.CountGalleriesByCategory(ctx, category)
	checkpoint["db_count"] = float64(dbCount)

	tc := getStateInt(checkpoint, "total_count")
	var tcPtr *int
	if tc > 0 {
		tcPtr = &tc
	}
	progressPct := CalcFullProgress(dbCount, tcPtr, false)

	slog.Info(fmt.Sprintf("[FULL ] [%s] upserted=%d db_count=%d progress=%.2f%%",
		name, len(rowsToUpsert), dbCount, progressPct))

	_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, progressPct, "", true)

	return false, nil
}

func fullCategory(def *db.TaskDef) string {
	if s, ok := def.Scope["category"].(string); ok {
		return s
	}
	return ""
}

// fullProgress is a best-effort pct for transient failure paths where we don't
// have the latest list response. Falls back to whatever is in checkpoint.
func fullProgress(checkpoint map[string]any, category string, database *db.DB, ctx context.Context) float64 {
	tc := getStateInt(checkpoint, "total_count")
	if tc <= 0 {
		return 0
	}
	dbCount, _ := database.CountGalleriesByCategory(ctx, category)
	return CalcFullProgress(dbCount, &tc, false)
}
