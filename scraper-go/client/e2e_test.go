//go:build integration

package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/parser"
	"github.com/CheerChen/eh-stash/scraper-go/ratelimit"
)

func TestE2EListAndParse(t *testing.T) {
	cfg := loadTestConfig(t)
	limiter := ratelimit.New(
		time.Duration(cfg.RateInterval*float64(time.Second)),
		time.Duration(cfg.BanCooldown*float64(time.Second)),
	)

	c, err := New(cfg, limiter)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fetch and parse list page
	listURL := cfg.ExBaseURL + "/?f_cats=1021&inline_set=dm_e"
	body, result, err := c.FetchPage(ctx, listURL)
	if err != nil || result != ResultOK {
		t.Fatalf("list page: result=%s err=%v", result, err)
	}

	listResult, err := parser.ParseGalleryList(body)
	if err != nil {
		t.Fatalf("ParseGalleryList: %v", err)
	}

	t.Logf("Parsed %d items, total=%v, nextCursor=%v",
		len(listResult.Items), listResult.TotalCount, listResult.NextCursor)

	if len(listResult.Items) == 0 {
		t.Fatal("no items parsed from list page")
	}

	item := listResult.Items[0]
	t.Logf("First item: gid=%d token=%s title=%q rating=%v",
		item.GID, item.Token, item.Title, item.RatingEst)

	if item.GID == 0 || item.Token == "" || item.Title == "" {
		t.Errorf("incomplete item: %+v", item)
	}

	// Fetch and parse detail page for first item
	detailURL := fmt.Sprintf("%s/g/%d/%s/", cfg.ExBaseURL, item.GID, item.Token)
	detailBody, result, err := c.FetchPage(ctx, detailURL)
	if err != nil || result != ResultOK {
		t.Fatalf("detail page: result=%s err=%v", result, err)
	}

	detail, err := parser.ParseDetail(detailBody)
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("ParseDetail returned nil")
	}

	t.Logf("Detail: title=%q category=%q rating=%v pages=%d fav=%d tags=%d namespaces",
		detail.Title, detail.Category, detail.Rating, detail.Pages, detail.FavCount, len(detail.Tags))

	if detail.Title == "" {
		t.Error("detail.Title is empty")
	}
	if detail.Category == "" {
		t.Error("detail.Category is empty")
	}
	if len(detail.Tags) == 0 {
		t.Error("detail.Tags is empty")
	}
}
