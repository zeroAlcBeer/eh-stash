package task

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/config"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/parser"
)

// RunFavoritesOnce runs one round of the favorites sync task. Stop is signaled
// via ctx cancellation; state is persisted to sync_task_defs.checkpoint.
func RunFavoritesOnce(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	cfg *config.Config,
	def *db.TaskDef,
	signals *FavSignals,
) error {
	name := def.Name
	taskID := def.ID

	checkpoint := cloneState(def.Checkpoint)

	roundNum := getStateInt(checkpoint, "round")
	nextCursor := getStateString(checkpoint, "next_gid")
	isResuming := nextCursor != nil

	var collectedGIDs []int64
	failedGIDs := make(map[int64]bool)
	hasNewFavorites := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		favURL := cfg.ExBaseURL + "/favorites.php?inline_set=dm_e"
		if nextCursor != nil {
			favURL += "&next=" + *nextCursor
		}

		body, result, err := httpClient.FetchPage(ctx, favURL)
		if err != nil {
			slog.Warn(fmt.Sprintf("[FAV  ] [%s] fetch failed", name), "error", err)
			if nextCursor != nil {
				checkpoint["next_gid"] = *nextCursor
			}
			return database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "fetch failed", false)
		}
		if result == client.ResultBanned {
			slog.Warn(fmt.Sprintf("[FAV  ] [%s] IP banned", name))
			if nextCursor != nil {
				checkpoint["next_gid"] = *nextCursor
			}
			return database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "IP temporarily banned", false)
		}

		listResult, err := parser.ParseGalleryList(body)
		if err != nil {
			slog.Error(fmt.Sprintf("[FAV  ] [%s] parse failed", name), "error", err)
			return nil
		}

		if len(listResult.Items) == 0 {
			break
		}

		var pageGIDs []int64
		for _, item := range listResult.Items {
			pageGIDs = append(pageGIDs, item.GID)
		}

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
							checkpoint["next_gid"] = *nextCursor
						}
						return database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "IP temporarily banned", false)
					}
					failedGIDs[gid] = true
					continue
				}

				detail, err := parser.ParseDetail(detailBody)
				if err != nil || detail == nil {
					failedGIDs[gid] = true
					continue
				}

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

		for _, item := range listResult.Items {
			if item.IsDeleted {
				_ = database.MarkGalleryInactive(ctx, item.GID)
			}
		}

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

		checkpoint["next_gid"] = listResult.NextCursor
		checkpoint["round"] = float64(roundNum)
		_ = database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "", false)

		if listResult.NextCursor == nil {
			break
		}
		cursor := *listResult.NextCursor
		nextCursor = &cursor
	}

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

	if hasNewFavorites || hasRemovedFavorites {
		notify(signals.ProfileUpdate)
		slog.Info(fmt.Sprintf("[FAV  ] [%s] favorites changed, profile update signaled", name))
	} else if len(collectedGIDs) > 0 {
		slog.Info(fmt.Sprintf("[FAV  ] [%s] no favorites changed, skipping profile update", name))
	}

	slog.Info(fmt.Sprintf("[FAV  ] [%s] completed round=%d, total=%d favorites",
		name, roundNum+1, len(collectedGIDs)))

	checkpoint["next_gid"] = nil
	checkpoint["round"] = float64(roundNum + 1)
	return database.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 100, "", true)
}
