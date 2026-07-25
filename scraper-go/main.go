package main

import (
	"context"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/config"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/egress"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/ratelimit"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/scheduler"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("Starting Scraper Service (Go)")

	// pprof for live diagnostics — curl http://<host>:6060/debug/pprof/goroutine?debug=2
	// when the scraper hangs to see what every goroutine is blocked on.
	// /healthz shares this listener: a liveness probe for Uptime Kuma and the
	// deploy Verify step (the scraper has no other inbound HTTP surface).
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	go func() {
		if err := http.ListenAndServe("0.0.0.0:6060", nil); err != nil {
			slog.Warn("pprof server exited", "error", err)
		}
	}()

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

	// Schema migrations (River's own schema is migrated in the scheduler)
	if err := database.RunMigrations(ctx, cfg.MigrationsDir); err != nil {
		slog.Error("schema migrations failed", "error", err)
		os.Exit(1)
	}

	// Rate limiters
	mainLimiter := ratelimit.New(
		time.Duration(cfg.RateInterval*float64(time.Second)),
		time.Duration(cfg.BanCooldown*float64(time.Second)),
	)
	thumbLimiter := ratelimit.NewSimple(
		time.Duration(cfg.ThumbRateInterval * float64(time.Second)),
	)

	egressMgr := egress.New(egress.Config{
		ProxyURL: cfg.ProxyURL,
	})

	// HTTP client
	httpClient, err := client.New(cfg, mainLimiter, egressMgr)
	if err != nil {
		slog.Error("HTTP client init failed", "error", err)
		os.Exit(1)
	}
	egressMgr.SetProber(func(ctx context.Context, mode egress.Mode) error {
		return httpClient.ProbeAccess(ctx, mode)
	})
	mainLimiter.SetBanProber(httpClient.BanProbe)
	httpClient.SetBanEventHandler(func(secs int, pageURL string) {
		slog.Warn("ban detected, writing event", "duration", secs, "url", pageURL)
		_ = database.InsertTaskEvent(context.Background(), 0, nil, "proxy.banned",
			"IP temporarily banned",
			map[string]any{"duration_secs": secs, "url": pageURL})
	})

	// Signal channels for workers
	signals := &scheduler.Signals{
		ProfileUpdate:  make(chan struct{}, 1),
		GrouperTrigger: make(chan struct{}, 1),
		ThumbNotify:    make(chan struct{}, 1),
	}

	var wg sync.WaitGroup

	// Start scheduler
	sched := scheduler.New(database, httpClient, cfg, egressMgr, mainLimiter, thumbLimiter, signals)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.Run(ctx)
	}()

	wg.Wait()
	slog.Info("scraper stopped")
}
