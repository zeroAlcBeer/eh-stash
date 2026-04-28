package task

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CheerChen/eh-stash/scraper-go/client"
	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/parser"
)

// RunFullOnce runs one iteration of the full sync task.
// Returns (done, error). done=true means task completed this round.
func RunFullOnce(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	taskID int,
	runtime *db.SyncTask,
	grouperTrigger chan struct{},
) (bool, error) {
	name := runtime.Name
	category := runtime.Category

	// Normalize state
	state := make(map[string]any)
	for k, v := range runtime.State {
		state[k] = v
	}

	// Reset state if completed
	if getStateBool(state, "done") && runtime.Status == "completed" {
		state = map[string]any{
			"next_gid":    nil,
			"round":       0,
			"done":        false,
			"anchor_gid":  nil,
			"total_count": nil,
		}
	}

	nextCursor := getStateString(state, "next_gid")

	slog.Info(fmt.Sprintf("[FULL ] [%s] category=%s fetching", name, category),
		"next_gid", nextCursor)

	// Fetch list page
	listURL := BuildListURL(httpClient.BaseURL(), []string{category}, nextCursor)
	body, result, err := httpClient.FetchPage(ctx, listURL)
	if err != nil {
		slog.Warn(fmt.Sprintf("[FULL ] [%s] fetch_list_page failed, will retry", name), "error", err)
		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithState(state), db.WithStatus("running"), db.WithError(""), db.WithTouchRunTime())
		return false, nil
	}

	if result == client.ResultBanned {
		slog.Warn(fmt.Sprintf("[FULL ] [%s] IP temporarily banned", name))
		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithState(state), db.WithStatus("running"),
			db.WithError("IP temporarily banned, will retry when ban expires"))
		return false, nil
	}

	listResult, err := parser.ParseGalleryList(body)
	if err != nil {
		slog.Error(fmt.Sprintf("[FULL ] [%s] parse list page failed", name), "error", err)
		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithState(state), db.WithStatus("running"), db.WithError(""), db.WithTouchRunTime())
		return false, nil
	}

	// Update anchor_gid and total_count
	if len(listResult.Items) > 0 && state["anchor_gid"] == nil {
		maxGID := listResult.Items[0].GID
		for _, item := range listResult.Items {
			if item.GID > maxGID {
				maxGID = item.GID
			}
		}
		state["anchor_gid"] = float64(maxGID)
	}

	if listResult.TotalCount != nil {
		existing := getStateInt(state, "total_count")
		if *listResult.TotalCount > existing {
			state["total_count"] = float64(*listResult.TotalCount)
		}
	}

	slog.Info(fmt.Sprintf("[FULL ] [%s] category=%s page_items=%d next_gid=%v total_count=%v",
		name, category, len(listResult.Items), listResult.NextCursor, state["total_count"]))

	// Fetch details for each item
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
			_ = database.UpdateTaskRuntime(ctx, taskID,
				db.WithState(state), db.WithStatus("running"),
				db.WithError("IP temporarily banned, will retry when ban expires"))
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

	// Bulk upsert
	if len(rowsToUpsert) > 0 {
		if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
			return false, fmt.Errorf("upsert galleries: %w", err)
		}
		notify(grouperTrigger)
	}

	// Check completion
	done := len(listResult.Items) == 0 || listResult.NextCursor == nil
	roundNum := getStateInt(state, "round")

	if done {
		state["next_gid"] = nil
		state["round"] = float64(roundNum + 1)
		state["done"] = true

		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithState(state), db.WithProgress(100.0),
			db.WithStatus("completed"), db.WithError(""), db.WithTouchRunTime())
		_ = database.SetTaskDesiredStatus(ctx, taskID, "stopped")
		slog.Info(fmt.Sprintf("[FULL ] [%s] completed round=%d", name, roundNum+1))
		return true, nil
	}

	// Continue to next page
	state["next_gid"] = *listResult.NextCursor
	state["done"] = false

	dbCount, _ := database.CountGalleriesByCategory(ctx, category)
	state["db_count"] = float64(dbCount)

	tc := getStateInt(state, "total_count")
	var tcPtr *int
	if tc > 0 {
		tcPtr = &tc
	}
	progressPct := CalcFullProgress(dbCount, tcPtr, false)

	slog.Info(fmt.Sprintf("[FULL ] [%s] upserted=%d db_count=%d progress=%.2f%%",
		name, len(rowsToUpsert), dbCount, progressPct))

	_ = database.UpdateTaskRuntime(ctx, taskID,
		db.WithState(state), db.WithProgress(progressPct),
		db.WithStatus("running"), db.WithError(""), db.WithTouchRunTime())

	return false, nil
}
