//go:build integration

package client

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/config"
	"github.com/CheerChen/eh-stash/scraper-go/ratelimit"
)

var gidTokenRE = regexp.MustCompile(`/g/(\d+)/([a-f0-9]+)/`)

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.Cookies) == 0 {
		t.Skip("EX_COOKIES not set, skipping integration test")
	}
	return cfg
}

func TestValidateAccess(t *testing.T) {
	cfg := loadTestConfig(t)
	limiter := ratelimit.New(time.Duration(cfg.RateInterval*float64(time.Second)), time.Duration(cfg.BanCooldown*float64(time.Second)))

	c, err := New(cfg, limiter)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = c.ValidateAccess(ctx)
	if err != nil {
		t.Fatalf("ValidateAccess failed: %v", err)
	}
	t.Log("ValidateAccess passed — ExHentai accessible with uTLS")
}

func TestFetchListPage(t *testing.T) {
	cfg := loadTestConfig(t)
	limiter := ratelimit.New(time.Duration(cfg.RateInterval*float64(time.Second)), time.Duration(cfg.BanCooldown*float64(time.Second)))

	c, err := New(cfg, limiter)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fetch first page with Doujinshi category (f_cats = 1023 - 2 = 1021)
	pageURL := cfg.ExBaseURL + "/?f_cats=1021&inline_set=dm_e"
	body, result, err := c.FetchPage(ctx, pageURL)
	if err != nil {
		t.Fatalf("FetchPage error: %v", err)
	}
	if result == ResultBanned {
		t.Fatal("IP is banned")
	}
	if result != ResultOK {
		t.Fatalf("unexpected result: %s", result)
	}

	// Should contain gallery list table
	if !strings.Contains(body, "itg") {
		t.Fatal("response does not contain gallery list (.itg)")
	}
	t.Logf("FetchListPage passed — got %d bytes", len(body))
}

func TestFetchDetailPage(t *testing.T) {
	cfg := loadTestConfig(t)
	limiter := ratelimit.New(time.Duration(cfg.RateInterval*float64(time.Second)), time.Duration(cfg.BanCooldown*float64(time.Second)))

	c, err := New(cfg, limiter)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First fetch a list page to get a real GID/token
	listURL := cfg.ExBaseURL + "/?f_cats=1021&inline_set=dm_e"
	listBody, result, err := c.FetchPage(ctx, listURL)
	if err != nil || result != ResultOK {
		t.Fatalf("FetchPage list error: result=%s err=%v", result, err)
	}

	// Extract first gallery link
	m := gidTokenRE.FindStringSubmatch(listBody)
	if m == nil {
		t.Fatal("no gallery link found in list page")
	}
	gid, token := m[1], m[2]
	t.Logf("Using gallery gid=%s token=%s", gid, token)

	// Fetch detail page
	detailURL := cfg.ExBaseURL + "/g/" + gid + "/" + token + "/"
	body, result, err := c.FetchPage(ctx, detailURL)
	if err != nil {
		t.Fatalf("FetchPage detail error: %v", err)
	}
	if result == ResultBanned {
		t.Fatal("IP is banned")
	}
	if result != ResultOK {
		t.Fatalf("unexpected result: %s", result)
	}

	if !strings.Contains(body, "gn") || !strings.Contains(body, "taglist") {
		t.Fatal("response does not look like a detail page")
	}
	t.Logf("FetchDetailPage passed — got %d bytes", len(body))
}
