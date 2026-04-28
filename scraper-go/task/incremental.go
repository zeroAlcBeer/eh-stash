package task

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/CheerChen/eh-stash/scraper-go/client"
	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/parser"
)

// RunIncrementalOnce runs one iteration of the incremental scan task.
func RunIncrementalOnce(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	taskID int,
	runtime *db.SyncTask,
	grouperTrigger chan struct{},
) (bool, error) {
	name := runtime.Name

	// Parse config
	categories := parseCategories(runtime.Config)
	scanWindow := 10000
	if v, ok := runtime.Config["scan_window"]; ok {
		if f, ok := v.(float64); ok && f > 0 {
			scanWindow = int(f)
		}
	}
	ratingThreshold := 0.5
	if v, ok := runtime.Config["rating_diff_threshold"]; ok {
		if f, ok := v.(float64); ok {
			ratingThreshold = f
		}
	}

	// Normalize state
	state := make(map[string]any)
	for k, v := range runtime.State {
		state[k] = v
	}

	nextCursor := getStateString(state, "next_gid")
	scannedCount := getStateInt(state, "scanned_count")
	roundNum := getStateInt(state, "round")

	exitReason := ""

	for exitReason == "" {
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
			_ = database.UpdateTaskRuntime(ctx, taskID,
				db.WithState(state), db.WithStatus("stopped"), db.WithTouchRunTime())
			return true, nil
		}

		// Fetch list page
		listURL := BuildListURL(httpClient.BaseURL(), categories, nextCursor)
		body, result, err := httpClient.FetchPage(ctx, listURL)
		if err != nil {
			slog.Warn(fmt.Sprintf("[INCR ] [%s] fetch failed", name), "error", err)
			exitReason = "ERROR"
			break
		}
		if result == client.ResultBanned {
			exitReason = "BANNED"
			break
		}

		listResult, err := parser.ParseGalleryList(body)
		if err != nil {
			slog.Error(fmt.Sprintf("[INCR ] [%s] parse failed", name), "error", err)
			exitReason = "ERROR"
			break
		}

		if len(listResult.Items) == 0 {
			exitReason = "END"
			break
		}

		// First page: record latest_gid
		if state["latest_gid"] == nil {
			maxGID := int64(0)
			for _, item := range listResult.Items {
				if item.GID > maxGID {
					maxGID = item.GID
				}
			}
			state["latest_gid"] = float64(maxGID)
		}

		// Process items
		var rowsToUpsert []db.GalleryRow
		nNew, nRefresh, nSkip := 0, 0, 0

		for _, item := range listResult.Items {
			existing, err := database.GetGalleryByGID(ctx, item.GID)
			if err != nil {
				slog.Error(fmt.Sprintf("[INCR ] [%s] DB error for gid=%d", name, item.GID), "error", err)
				continue
			}

			if item.IsDeleted && existing != nil {
				_ = database.MarkGalleryInactive(ctx, item.GID)
				nSkip++
				continue
			}

			if existing == nil {
				// New gallery
				detailURL := BuildDetailURL(httpClient.BaseURL(), item.GID, item.Token)
				detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
				if err != nil || detailResult != client.ResultOK {
					if detailResult == client.ResultBanned {
						exitReason = "BANNED"
						break
					}
					continue
				}
				detail, err := parser.ParseDetail(detailBody)
				if err != nil || detail == nil {
					continue
				}
				rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(item.GID, item.Token, detail, true))
				nNew++
			} else {
				// Check if refresh needed
				shouldRefresh := shouldRefreshFromList(existing, &item, ratingThreshold)
				if shouldRefresh {
					detailURL := BuildDetailURL(httpClient.BaseURL(), item.GID, item.Token)
					detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
					if err != nil || detailResult != client.ResultOK {
						if detailResult == client.ResultBanned {
							exitReason = "BANNED"
							break
						}
						continue
					}
					detail, err := parser.ParseDetail(detailBody)
					if err != nil || detail == nil {
						continue
					}
					rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(item.GID, item.Token, detail, true))
					nRefresh++
				} else {
					nSkip++
				}
			}
		}

		if exitReason != "" {
			break
		}

		scannedCount += len(listResult.Items)
		state["scanned_count"] = float64(scannedCount)

		if len(rowsToUpsert) > 0 {
			if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
				return false, fmt.Errorf("upsert galleries: %w", err)
			}
			notify(grouperTrigger)
		}

		slog.Info(fmt.Sprintf("[INCR ] [%s] scanned=%d new=%d refresh=%d skip=%d",
			name, scannedCount, nNew, nRefresh, nSkip))

		progress := ClampProgress(float64(scannedCount) / float64(scanWindow) * 100)
		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithState(state), db.WithProgress(progress),
			db.WithStatus("running"), db.WithError(""))

		// Check exit conditions
		if listResult.NextCursor == nil {
			exitReason = "END"
		} else if scannedCount >= scanWindow {
			exitReason = "WINDOW"
		} else {
			nextCursor = listResult.NextCursor
			state["next_gid"] = *listResult.NextCursor
		}
	}

	// Post-loop: reset state for next cycle
	slog.Info(fmt.Sprintf("[INCR ] [%s] exit_reason=%s scanned=%d round=%d",
		name, exitReason, scannedCount, roundNum))

	if exitReason == "END" || exitReason == "WINDOW" {
		state["next_gid"] = nil
		state["round"] = float64(roundNum + 1)
		state["scanned_count"] = float64(0)
		state["latest_gid"] = nil
		_ = database.UpdateTaskRuntime(ctx, taskID,
			db.WithState(state), db.WithProgress(100),
			db.WithStatus("running"), db.WithError(""), db.WithTouchRunTime())
		return false, nil // continue next cycle
	}

	// BANNED or ERROR — keep state for resumption
	_ = database.UpdateTaskRuntime(ctx, taskID,
		db.WithState(state), db.WithStatus("running"),
		db.WithError(fmt.Sprintf("exit: %s", exitReason)))
	return false, nil
}

func parseCategories(cfg map[string]any) []string {
	cats, ok := cfg["categories"]
	if !ok {
		return []string{"Doujinshi", "Manga", "Cosplay"}
	}
	catList, ok := cats.([]any)
	if !ok {
		return []string{"Doujinshi", "Manga", "Cosplay"}
	}
	var result []string
	for _, c := range catList {
		if s, ok := c.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return []string{"Doujinshi", "Manga", "Cosplay"}
	}
	return result
}

func shouldRefreshFromList(existing map[string]any, item *parser.GalleryListItem, threshold float64) bool {
	// Check tag diff
	existingTags, _ := existing["tags"].(map[string][]string)
	detailTags := flattenTags(existingTags)
	for _, tag := range item.VisibleTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !detailTags[tag] {
			return true // missing tag
		}
	}

	// Check rating diff
	existingRating, _ := existing["rating"].(*float64)
	if existingRating == nil && item.RatingEst != nil {
		return true
	}
	if existingRating != nil && item.RatingEst != nil {
		eBucket := math.Round(*existingRating*2) / 2
		lBucket := math.Round(*item.RatingEst*2) / 2
		if math.Abs(eBucket-lBucket) >= threshold {
			return true
		}
	}

	return false
}

func flattenTags(tags map[string][]string) map[string]bool {
	result := make(map[string]bool)
	for _, values := range tags {
		for _, v := range values {
			v = strings.ToLower(strings.TrimSpace(v))
			if v != "" {
				result[v] = true
			}
		}
	}
	return result
}
