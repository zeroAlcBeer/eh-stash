package task

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/parser"
)

// IncrementalSliceResult is what RunIncrementalSlice returns to the worker so
// it can decide whether to chain another slice, finalize the round, or surface
// an exit reason.
type IncrementalSliceResult struct {
	ExitReason string  // "" = continue, "END"/"WINDOW" = round done, "BANNED"/"ERROR" = pause round
	Checkpoint map[string]any
	Pct        float64
}

// RunIncrementalSlice fetches exactly one page of the incremental scan and
// returns the updated checkpoint. The worker is responsible for persisting it
// and deciding the next action. Stop is signaled via ctx cancellation.
func RunIncrementalSlice(
	ctx context.Context,
	database *db.DB,
	httpClient *client.Client,
	def *db.TaskDef,
	grouperTrigger chan struct{},
) (IncrementalSliceResult, error) {
	name := def.Name

	categories := parseCategories(def.Config)
	scanWindow := 10000
	if v, ok := def.Config["scan_window"]; ok {
		if f, ok := v.(float64); ok && f > 0 {
			scanWindow = int(f)
		}
	}
	ratingThreshold := 0.5
	if v, ok := def.Config["rating_diff_threshold"]; ok {
		if f, ok := v.(float64); ok {
			ratingThreshold = f
		}
	}

	checkpoint := cloneState(def.Checkpoint)
	nextCursor := getStateString(checkpoint, "next_gid")
	scannedCount := getStateInt(checkpoint, "scanned_count")

	pctFor := func(scanned int) float64 {
		return ClampProgress(float64(scanned) / float64(scanWindow) * 100)
	}

	result := IncrementalSliceResult{Checkpoint: checkpoint, Pct: pctFor(scannedCount)}

	slog.Info("[INCR ] slice start",
		"name", name,
		"next_gid", nextCursor,
		"scanned_count", scannedCount,
		"scan_window", scanWindow,
		"categories", categories,
	)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	listURL := BuildListURL(httpClient.BaseURL(), categories, nextCursor)
	listStart := time.Now()
	body, fetchResult, err := httpClient.FetchPage(ctx, listURL)
	listLatency := time.Since(listStart)
	if err != nil {
		slog.Warn("[INCR ] list fetch failed", "name", name, "url", listURL, "latency", listLatency, "error", err)
		result.ExitReason = "ERROR"
		return result, nil
	}
	if fetchResult == client.ResultBanned {
		slog.Warn("[INCR ] list fetch banned", "name", name, "url", listURL, "latency", listLatency)
		result.ExitReason = "BANNED"
		return result, nil
	}
	slog.Info("[INCR ] list fetched", "name", name, "url", listURL, "latency", listLatency, "bytes", len(body))

	listResult, err := parser.ParseGalleryList(body)
	if err != nil {
		slog.Error("[INCR ] list parse failed", "name", name, "error", err)
		result.ExitReason = "ERROR"
		return result, nil
	}

	if len(listResult.Items) == 0 {
		slog.Info("[INCR ] list empty, round ending", "name", name)
		result.ExitReason = "END"
		return result, nil
	}

	hasNext := listResult.NextCursor != nil
	nextStr := ""
	if hasNext {
		nextStr = *listResult.NextCursor
	}
	slog.Info("[INCR ] list parsed",
		"name", name,
		"items", len(listResult.Items),
		"has_next", hasNext,
		"next_cursor", nextStr,
	)

	if checkpoint["latest_gid"] == nil {
		maxGID := int64(0)
		for _, item := range listResult.Items {
			if item.GID > maxGID {
				maxGID = item.GID
			}
		}
		checkpoint["latest_gid"] = float64(maxGID)
	}

	var rowsToUpsert []db.GalleryRow
	nNew, nRefresh, nSkip := 0, 0, 0
	banned := false
	total := len(listResult.Items)

	for i, item := range listResult.Items {
		if err := ctx.Err(); err != nil {
			slog.Warn("[INCR ] item loop cancelled",
				"name", name,
				"i", i+1,
				"total", total,
				"new_so_far", nNew,
				"refresh_so_far", nRefresh,
				"skip_so_far", nSkip,
				"ctx_err", err,
			)
			return result, err
		}
		existing, err := database.GetGalleryByGID(ctx, item.GID)
		if err != nil {
			slog.Error("[INCR ] item DB lookup failed",
				"name", name,
				"i", i+1,
				"total", total,
				"gid", item.GID,
				"error", err,
			)
			continue
		}

		if item.IsDeleted && existing != nil {
			_ = database.MarkGalleryInactive(ctx, item.GID)
			nSkip++
			slog.Info("[INCR ] item deleted",
				"name", name,
				"i", i+1,
				"total", total,
				"gid", item.GID,
			)
			continue
		}

		if existing == nil {
			slog.Info("[INCR ] item new, fetching detail",
				"name", name,
				"i", i+1,
				"total", total,
				"gid", item.GID,
			)
			detailURL := BuildDetailURL(httpClient.BaseURL(), item.GID, item.Token)
			detailStart := time.Now()
			detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
			detailLatency := time.Since(detailStart)
			if err != nil || detailResult != client.ResultOK {
				slog.Warn("[INCR ] detail fetch failed",
					"name", name,
					"i", i+1,
					"gid", item.GID,
					"latency", detailLatency,
					"result", detailResult,
					"error", err,
				)
				if detailResult == client.ResultBanned {
					banned = true
					break
				}
				continue
			}
			detail, err := parser.ParseDetail(detailBody)
			if err != nil || detail == nil {
				slog.Warn("[INCR ] detail parse failed",
					"name", name,
					"i", i+1,
					"gid", item.GID,
					"latency", detailLatency,
					"error", err,
				)
				continue
			}
			rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(item.GID, item.Token, detail, true))
			nNew++
			slog.Info("[INCR ] item new ok",
				"name", name,
				"i", i+1,
				"total", total,
				"gid", item.GID,
				"latency", detailLatency,
			)
		} else {
			shouldRefresh := shouldRefreshFromList(existing, &item, ratingThreshold)
			if shouldRefresh {
				slog.Info("[INCR ] item refresh, fetching detail",
					"name", name,
					"i", i+1,
					"total", total,
					"gid", item.GID,
				)
				detailURL := BuildDetailURL(httpClient.BaseURL(), item.GID, item.Token)
				detailStart := time.Now()
				detailBody, detailResult, err := httpClient.FetchPage(ctx, detailURL)
				detailLatency := time.Since(detailStart)
				if err != nil || detailResult != client.ResultOK {
					slog.Warn("[INCR ] detail fetch failed (refresh)",
						"name", name,
						"i", i+1,
						"gid", item.GID,
						"latency", detailLatency,
						"result", detailResult,
						"error", err,
					)
					if detailResult == client.ResultBanned {
						banned = true
						break
					}
					continue
				}
				detail, err := parser.ParseDetail(detailBody)
				if err != nil || detail == nil {
					slog.Warn("[INCR ] detail parse failed (refresh)",
						"name", name,
						"i", i+1,
						"gid", item.GID,
						"latency", detailLatency,
						"error", err,
					)
					continue
				}
				rowsToUpsert = append(rowsToUpsert, BuildUpsertRow(item.GID, item.Token, detail, true))
				nRefresh++
				slog.Info("[INCR ] item refresh ok",
					"name", name,
					"i", i+1,
					"total", total,
					"gid", item.GID,
					"latency", detailLatency,
				)
			} else {
				nSkip++
			}
		}
	}

	if banned {
		slog.Warn("[INCR ] page interrupted by ban",
			"name", name,
			"new", nNew,
			"refresh", nRefresh,
			"skip", nSkip,
		)
		result.ExitReason = "BANNED"
		return result, nil
	}

	scannedCount += len(listResult.Items)
	checkpoint["scanned_count"] = float64(scannedCount)

	if len(rowsToUpsert) > 0 {
		if _, err := database.UpsertGalleriesBulk(ctx, rowsToUpsert); err != nil {
			slog.Error("[INCR ] upsert failed",
				"name", name,
				"rows", len(rowsToUpsert),
				"error", err,
			)
			return result, fmt.Errorf("upsert galleries: %w", err)
		}
		notify(grouperTrigger)
	}

	slog.Info("[INCR ] page summary",
		"name", name,
		"scanned_count", scannedCount,
		"new", nNew,
		"refresh", nRefresh,
		"skip", nSkip,
		"upserted", len(rowsToUpsert),
	)

	result.Pct = pctFor(scannedCount)

	if listResult.NextCursor == nil {
		slog.Info("[INCR ] page exit END: no next cursor", "name", name, "scanned_count", scannedCount)
		result.ExitReason = "END"
		return result, nil
	}
	if scannedCount >= scanWindow {
		slog.Info("[INCR ] page exit WINDOW: hit scan_window",
			"name", name,
			"scanned_count", scannedCount,
			"scan_window", scanWindow,
		)
		result.ExitReason = "WINDOW"
		return result, nil
	}

	checkpoint["next_gid"] = *listResult.NextCursor
	slog.Info("[INCR ] page continue, chain next",
		"name", name,
		"scanned_count", scannedCount,
		"next_gid", *listResult.NextCursor,
		"pct", result.Pct,
	)
	return result, nil
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
	existingTags, _ := existing["tags"].(map[string][]string)
	detailTags := flattenTags(existingTags)
	for _, tag := range item.VisibleTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !detailTags[tag] {
			return true
		}
	}

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
