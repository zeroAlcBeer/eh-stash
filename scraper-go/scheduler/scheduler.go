package scheduler

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/client"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/config"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/egress"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/ratelimit"
)

// Signals holds channels for inter-component communication.
type Signals struct {
	ProfileUpdate  chan struct{}
	GrouperTrigger chan struct{}
	ThumbNotify    chan struct{}
}

type Scheduler struct {
	db           *db.DB
	client       *client.Client
	cfg          *config.Config
	egress       *egress.Manager
	mainLimiter  *ratelimit.Limiter
	thumbLimiter *ratelimit.SimpleLimiter
	signals      *Signals
	riverClient  *river.Client[pgx.Tx]
}

func New(
	database *db.DB,
	httpClient *client.Client,
	cfg *config.Config,
	egressMgr *egress.Manager,
	mainLimiter *ratelimit.Limiter,
	thumbLimiter *ratelimit.SimpleLimiter,
	signals *Signals,
) *Scheduler {
	return &Scheduler{
		db:           database,
		client:       httpClient,
		cfg:          cfg,
		egress:       egressMgr,
		mainLimiter:  mainLimiter,
		thumbLimiter: thumbLimiter,
		signals:      signals,
	}
}

// Run starts the scheduler main loop, background workers, and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.runRiver(ctx)
}
