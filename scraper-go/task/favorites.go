package task

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CheerChen/eh-stash/scraper-go/client"
	"github.com/CheerChen/eh-stash/scraper-go/config"
	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/parser"
)

// RunFavoritesOnce runs the favorites sync task.
func RunFavoritesOnce(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	cfg *config.Config,
	taskID int,
	runtime *db.SyncTask,
	signals *FavSignals,
) (bool, error) {
	name := runtime.Name

	state := make(map[string]any)
	for k, v := range runtime.State {
		state[k] = v
	}

	roundNum := getStateInt(state, "round")
	nextCursor := getStateString(state, "next_gid")
	isResuming := nextCursor != nil

	var collectedGIDs []int64
	failedGIDs := make(map[int64]bool)
	hasNewFavorites := false // track if any page had new favorites

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		// Check desired_status
		rt, err := database.GetTaskRuntime(ctx, taskID)
		if err != nil {
			return false, err
		}
		if rt == nil || rt.DesiredStatus != "running" {
			state["next_gid"] = nil
			if nextCursor != nil {
				state["next_gid"] = *nextCursor
			}
			_ = database.UpdateTaskRuntime(ctx, taskID,
				db.WithState(state), db.WithStatus("stopped"), db.WithTouchRunTime())
			return true, nil
		}

		// Fetch favorites page
		favURL := cfg.ExBaseURL + "/favorites.php?inline_set=dm_e"
		if nextCursor != nil {
			favURL += "&next=" + *nextCursor
		}

		body, result, err := httpClient.FetchPage(ctx, favURL)
		if err != nil {
			slog.Warn(fmt.Sprintf("[FAV  ] [%s] fetch failed", name), "error", err)
			if nextCursor != nil {
				state["next_gid"] = *nextCursor
			}
			_ = database.UpdateTaskRuntime(ctx, taskID,
				db.WithState(state), db.WithStatus("running"), db.WithError("fetch failed"))
			return false, nil
		}
		if result == client.ResultBanned {
			slog.Warn(fmt.Sprintf("[FAV  ] [%s] IP banned", name))
			if nextCursor != nil {
				state["next_gid"] = *nextCursor
			}
			_ = database.UpdateTaskRuntime(ctx, taskID,
				db.WithState(state), db.WithStatus("running"),
				db.WithError("IP temporarily banned"))
			return false, nil
		}

		listResult, err := parser.ParseGalleryList(body)
		if err != nil {
			slog.Error(fmt.Sprintf("[FAV  ] [%s] parse failed", name), "error", err)
			return false, nil
		}

		if len(listResult.Items) == 0 {
			break // no more pages
		}

		// Collect GIDs from this page
		var pageGIDs []int64
		for _, item := range listResult.Items {
			pageGIDs = append(pageGIDs, item.GID)
		}

		// Backfill missing galleries
		nonExisting, err := database.GetNonExistingGIDs(ctx, pageGIDs)
		if err != nil {
			slog.Error(fmt.Sprintf("[FAV  ] [%s] get non-existing GIDs failed", name), "error", err)
		}

		if len(nonExisting) > 0 {
			var rowsToUpsert []db.GalleryRow
			for _, gid := range nonExisting {
				if failedGIDs[gid] {
					continue
				}
				// Find token for this GID
				var token string
				for _, item := range listResult.Items {
					if item.GID == gid {
						token = item.Token
						break
					}
				}
				if token == "" {
					continue
				}

				detailURL := BuildDetailURL(httpClient.BaseURL(), gid, token)
				detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
				if err != nil || detailResult != client.ResultOK {
					if detailResult == client.ResultBanned {
						if nextCursor != nil {
							state["next_gid"] = *nextCursor
						}
						_ = database.UpdateTaskRuntime(ctx, taskID,
							db.WithState(state), db.WithStatus("running"),
							db.WithError("IP temporarily banned"))
						return false, nil
					}
					failedGIDs[gid] = true
					continue
				}

				detail, err := parser.ParseDetail(detailBody)
				if err != nil || detail == nil {
					failedGIDs[gid] = true
					continue
				}

				// Check if deleted
				isActive := true
				for _, item := range listResult.Items {
					if item.GID == gid && item.IsDeleted {
						isActive = false
						break
					}
				}

				rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(gid, token, detail, isActive))
			}

			if len(rowsToUpsert) > 0 {
				if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
					slog.Error(fmt.Sprintf("[FAV  ] [%s] upsert failed", name), "error", err)
				} else {
					notify(signals.GrouperTrigger)
				}
			}
			slog.Info(fmt.Sprintf("[FAV  ] [%s] backfilled %d missing galleries", name, len(rowsToUpsert)))
		}

		// Mark deleted existing galleries
		for _, item := range listResult.Items {
			if item.IsDeleted {
				_ = database.MarkGalleryInactive(ctx, item.GID)
			}
		}

		// Upsert favorites
		var favRows []db.FavoriteRow
		for _, item := range listResult.Items {
			favRows = append(favRows, db.FavoriteRow{
				GID:         item.GID,
				FavoritedAt: item.FavoritedAt,
			})
		}
		newCount, err := database.UpsertFavoritesCountNew(ctx, favRows)
		if err != nil {
			slog.Error(fmt.Sprintf("[FAV  ] [%s] upsert favorites failed", name), "error", err)
		} else {
			if newCount > 0 {
				hasNewFavorites = true
			}
			slog.Info(fmt.Sprintf("[FAV  ] [%s] upserted favorites, %d new", name, newCount))
		}

		collectedGIDs = append(collectedGIDs, pageGIDs...)

		// Advance cursor
		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithStatus("running"), db.WithError(""),
			db.WithState(map[string]any{
				"next_gid": listResult.NextCursor,
				"round":    float64(roundNum),
			}))

		if listResult.NextCursor == nil {
			break
		}
		cursor := *listResult.NextCursor
		nextCursor = &cursor
	}

	// Full traversal completed
	hasRemovedFavorites := false
	if !isResuming && len(collectedGIDs) > 0 {
		removed, err := database.CleanupStaleFavorites(ctx, collectedGIDs)
		if err != nil {
			slog.Error(fmt.Sprintf("[FAV  ] [%s] cleanup failed", name), "error", err)
		} else if removed > 0 {
			hasRemovedFavorites = true
			slog.Info(fmt.Sprintf("[FAV  ] [%s] cleanup: removed %d stale favorites", name, removed))
		}
	}

	// Rebuild preference tags only if favorites actually changed
	if hasNewFavorites || hasRemovedFavorites {
		tagCount, err := database.RebuildPreferenceTags(ctx)
		if err != nil {
			slog.Error(fmt.Sprintf("[FAV  ] [%s] rebuild preference tags failed", name), "error", err)
		} else {
			slog.Info(fmt.Sprintf("[FAV  ] [%s] rebuilt %d preference tags", name, tagCount))
			notify(signals.ScorerReset)
		}
	} else if len(collectedGIDs) > 0 {
		slog.Info(fmt.Sprintf("[FAV  ] [%s] no favorites changed, skipping rebuild and scorer", name))
	}

	slog.Info(fmt.Sprintf("[FAV  ] [%s] completed round=%d, total=%d favorites",
		name, roundNum+1, len(collectedGIDs)))

	_ = database.UpdateTaskRuntime(ctx, taskID,
		db.WithState(map[string]any{
			"next_gid": nil,
			"round":    float64(roundNum + 1),
		}),
		db.WithStatus("completed"),
		db.WithProgress(100.0),
		db.WithError(""),
		db.WithTouchRunTime(),
	)
	return true, nil
}
