package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLimiterAcquireCanceledDuringIntervalWait(t *testing.T) {
	limiter := New(time.Hour, 0)

	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := limiter.Acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSimpleLimiterAcquireCanceledDuringIntervalWait(t *testing.T) {
	limiter := NewSimple(time.Hour)

	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := limiter.Acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
