package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/client"
	"github.com/CheerChen/eh-stash/scraper-go/config"
	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/ratelimit"
	"github.com/CheerChen/eh-stash/scraper-go/scheduler"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("Starting Scraper Service (Go)")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received exit signal", "signal", sig)
		cancel()
	}()

	// Database
	database, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Rate limiters
	mainLimiter := ratelimit.New(
		time.Duration(cfg.RateInterval*float64(time.Second)),
		time.Duration(cfg.BanCooldown*float64(time.Second)),
	)
	thumbLimiter := ratelimit.NewSimple(
		time.Duration(cfg.ThumbRateInterval * float64(time.Second)),
	)

	// HTTP client
	httpClient, err := client.New(cfg, mainLimiter)
	if err != nil {
		slog.Error("HTTP client init failed", "error", err)
		os.Exit(1)
	}

	// Validate access
	slog.Info("validating ExHentai access...")
	if err := httpClient.ValidateAccess(ctx); err != nil {
		slog.Error("access validation failed", "error", err)
		os.Exit(1)
	}
	slog.Info("access check passed")

	// Signal channels for workers
	signals := &scheduler.Signals{
		ScorerReset:    make(chan struct{}, 1),
		GrouperTrigger: make(chan struct{}, 1),
		ThumbNotify:    make(chan struct{}, 1),
	}

	var wg sync.WaitGroup

	// Start scheduler
	sched := scheduler.New(database, httpClient, cfg, mainLimiter, thumbLimiter, signals)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.Run(ctx)
	}()

	wg.Wait()
	slog.Info("scraper stopped")
}
