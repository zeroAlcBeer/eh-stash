package ratelimit

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Limiter is a global rate limiter with ban awareness.
// All main-site HTTP requests must call Acquire before proceeding.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	lastTime time.Time

	banMu      sync.RWMutex
	banUntil   time.Time
	banCooldown time.Duration
}

func New(interval time.Duration, banCooldown time.Duration) *Limiter {
	return &Limiter{
		interval:    interval,
		banCooldown: banCooldown,
	}
}

// Acquire blocks until the rate limit allows the next request.
// It also waits if the IP is currently banned.
// Returns an error only if the context is cancelled.
func (l *Limiter) Acquire(ctx context.Context) error {
	// Wait for ban to expire
	if err := l.waitBan(ctx); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check ban after acquiring lock
	l.banMu.RLock()
	banned := time.Now().Before(l.banUntil)
	l.banMu.RUnlock()
	if banned {
		l.mu.Unlock()
		if err := l.waitBan(ctx); err != nil {
			return err
		}
		l.mu.Lock()
	}

	wait := l.interval - time.Since(l.lastTime)
	if wait > 0 {
		l.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		l.mu.Lock()
	}
	l.lastTime = time.Now()
	return nil
}

// SetBan sets a global ban barrier. All requests will block until the ban expires.
func (l *Limiter) SetBan(duration time.Duration) {
	l.banMu.Lock()
	defer l.banMu.Unlock()
	l.banUntil = time.Now().Add(duration)
	slog.Warn("IP banned, all main-site requests paused",
		"duration", duration,
		"until", l.banUntil.Format("15:04:05"))
}

// IsBanned returns true if currently banned.
func (l *Limiter) IsBanned() bool {
	l.banMu.RLock()
	defer l.banMu.RUnlock()
	return time.Now().Before(l.banUntil)
}

func (l *Limiter) waitBan(ctx context.Context) error {
	l.banMu.RLock()
	until := l.banUntil
	l.banMu.RUnlock()

	remaining := time.Until(until)
	if remaining <= 0 {
		return nil
	}

	slog.Info("waiting for ban to expire", "remaining", remaining.Round(time.Second))
	select {
	case <-time.After(remaining):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Cooldown after ban expires
	slog.Info("ban expired, cooling down", "cooldown", l.banCooldown)
	select {
	case <-time.After(l.banCooldown):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Clear ban
	l.banMu.Lock()
	if !time.Now().Before(l.banUntil) {
		l.banUntil = time.Time{}
	}
	l.banMu.Unlock()

	slog.Info("cooldown complete, resuming requests")
	return nil
}

// SimpleLimiter is a rate limiter without ban awareness (for CDN/thumb requests).
type SimpleLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	lastTime time.Time
}

func NewSimple(interval time.Duration) *SimpleLimiter {
	return &SimpleLimiter{interval: interval}
}

func (l *SimpleLimiter) Acquire(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	wait := l.interval - time.Since(l.lastTime)
	if wait > 0 {
		l.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		l.mu.Lock()
	}
	l.lastTime = time.Now()
	return nil
}
