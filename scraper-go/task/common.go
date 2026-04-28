package task

import (
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/parser"
)

var (
	// Markers to strip for base_title normalization:
	// [中国翻訳] [中国語] — translation markers
	// [DL版] — digital version
	// [無修正] — uncensored
	// (C\d+) — Comiket convention prefix e.g. (C107)
	stripMarkersRE = regexp.MustCompile(
		`\s*\[中国翻訳\]|\s*\[中国語\]|\s*\[DL版\]|\s*\[無修正\]|\s*\(C\d+\)`,
	)
	whitespaceRE = regexp.MustCompile(`\s+`)
)

// NormalizeBaseTitle computes the normalized base_title for grouping.
// Prefers title_jpn, falls back to title if empty.
func NormalizeBaseTitle(titleJPN, title string) string {
	src := titleJPN
	if src == "" {
		src = title
	}
	if src == "" {
		return ""
	}
	s := stripMarkersRE.ReplaceAllString(src, "")
	s = whitespaceRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// FavSignals holds channels needed by the favorites task.
type FavSignals struct {
	ScorerReset    chan struct{}
	GrouperTrigger chan struct{}
}

// Category bits for ExHentai f_cats bitmask (each bit = category to EXCLUDE)
var CategoryBits = map[string]int{
	"Misc":       1,
	"Doujinshi":  2,
	"Manga":      4,
	"Artist CG":  8,
	"Game CG":    16,
	"Image Set":  32,
	"Cosplay":    64,
	"Asian Porn": 128,
	"Non-H":      256,
	"Western":    512,
}

const AllCats = 1023

func ValidCategory(cat string) bool {
	_, ok := CategoryBits[cat]
	return ok
}

func CalcFCats(categories []string) int {
	include := 0
	for _, c := range categories {
		include |= CategoryBits[c]
	}
	return AllCats - include
}

func ValidateFullTask(runtime *db.SyncTask) error {
	if !ValidCategory(runtime.Category) {
		return fmt.Errorf("category '%s' is not a valid ExHentai category", runtime.Category)
	}
	return nil
}

func ValidateIncrementalTask(runtime *db.SyncTask) error {
	if runtime.Category != "Mixed" {
		return fmt.Errorf("incremental task category must be 'Mixed', got '%s'", runtime.Category)
	}
	cats, ok := runtime.Config["categories"]
	if !ok {
		return fmt.Errorf("incremental config.categories is required")
	}
	catList, ok := cats.([]any)
	if !ok || len(catList) == 0 {
		return fmt.Errorf("incremental config.categories must be a non-empty list")
	}
	for _, c := range catList {
		s, ok := c.(string)
		if !ok {
			return fmt.Errorf("incremental config.categories must contain strings")
		}
		if !ValidCategory(s) {
			return fmt.Errorf("invalid category '%s' in incremental config.categories", s)
		}
	}
	return nil
}

func BuildListURL(baseURL string, categories []string, nextCursor *string) string {
	fcats := CalcFCats(categories)
	url := fmt.Sprintf("%s/?f_cats=%d&inline_set=dm_e", baseURL, fcats)
	if nextCursor != nil {
		url += "&next=" + *nextCursor
	}
	return url
}

func BuildDetailURL(baseURL string, gid int64, token string) string {
	return fmt.Sprintf("%s/g/%d/%s/", baseURL, gid, token)
}

func BuildUpsertRow(gid int64, token string, detail *parser.GalleryDetail, isActive bool) db.GalleryRow {
	var postedAt *time.Time
	if detail.Posted != "" {
		// Try parsing the posted date
		for _, layout := range []string{
			"2006-01-02 15:04",
			"2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, detail.Posted); err == nil {
				postedAt = &t
				break
			}
		}
	}

	return db.GalleryRow{
		GID:          gid,
		Token:        token,
		Category:     detail.Category,
		Title:        detail.Title,
		TitleJPN:     detail.TitleJPN,
		BaseTitle:    NormalizeBaseTitle(detail.TitleJPN, detail.Title),
		Uploader:     detail.Uploader,
		PostedAt:     postedAt,
		Language:     detail.Language,
		Pages:        detail.Pages,
		Rating:       detail.Rating,
		FavCount:     detail.FavCount,
		CommentCount: detail.CommentCount,
		Thumb:        detail.Thumb,
		Tags:         detail.Tags,
		IsActive:     isActive,
	}
}

func ClampProgress(v float64) float64 {
	return math.Max(0, math.Min(100, v))
}

func CalcFullProgress(dbCount int, totalCount *int, done bool) float64 {
	if done {
		return 100.0
	}
	if totalCount == nil || *totalCount <= 0 {
		return 0.0
	}
	return ClampProgress(float64(dbCount) / float64(*totalCount) * 100)
}

// notify sends a non-blocking signal to a channel.
func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func getStateString(state map[string]any, key string) *string {
	v, ok := state[key]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func getStateFloat(state map[string]any, key string) float64 {
	v, ok := state[key]
	if !ok {
		return 0
	}
	switch f := v.(type) {
	case float64:
		return f
	case int:
		return float64(f)
	default:
		return 0
	}
}

func getStateBool(state map[string]any, key string) bool {
	v, ok := state[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func getStateInt(state map[string]any, key string) int {
	return int(getStateFloat(state, key))
}

func logTaskEvent(taskType, name, msg string, args ...any) {
	prefix := fmt.Sprintf("[%-5s] [%s] %s", taskType, name, msg)
	slog.Info(prefix, args...)
}
