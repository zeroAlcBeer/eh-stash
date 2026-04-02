package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/client"
	"github.com/CheerChen/eh-stash/scraper-go/config"
	"github.com/CheerChen/eh-stash/scraper-go/db"
	"github.com/CheerChen/eh-stash/scraper-go/ratelimit"
	"github.com/CheerChen/eh-stash/scraper-go/task"
	"github.com/CheerChen/eh-stash/scraper-go/worker"
)

const (
	PollInterval = 10 * time.Second
	WarmupDelay  = 30 * time.Second
)

// Signals holds channels for inter-component communication.
type Signals struct {
	ScorerReset    chan struct{}
	GrouperTrigger chan struct{}
	ThumbNotify    chan struct{}
}

type Scheduler struct {
	db           *db.DB
	client       *client.Client
	cfg          *config.Config
	mainLimiter  *ratelimit.Limiter
	thumbLimiter *ratelimit.SimpleLimiter
	signals      *Signals
}

func New(
	database *db.DB,
	httpClient *client.Client,
	cfg *config.Config,
	mainLimiter *ratelimit.Limiter,
	thumbLimiter *ratelimit.SimpleLimiter,
	signals *Signals,
) *Scheduler {
	return &Scheduler{
		db:           database,
		client:       httpClient,
		cfg:          cfg,
		mainLimiter:  mainLimiter,
		thumbLimiter: thumbLimiter,
		signals:      signals,
	}
}

type activeTask struct {
	cancel context.CancelFunc
	doneCh <-chan error
}

// Run starts the scheduler main loop, background workers, and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	done := make(chan struct{}, 3)

	go func() {
		worker.RunThumbWorker(workerCtx, s.db, s.client, s.cfg, s.thumbLimiter, s.signals.ThumbNotify)
		done <- struct{}{}
	}()
	go func() {
		worker.RunRecommendedScorerReverse(workerCtx, s.db, s.signals.ScorerReset)
		done <- struct{}{}
	}()
	go func() {
		worker.RunGalleryGrouper(workerCtx, s.db, s.signals.GrouperTrigger)
		done <- struct{}{}
	}()

	slog.Info("warmup: waiting before starting task scheduler", "delay", WarmupDelay)
	select {
	case <-time.After(WarmupDelay):
	case <-ctx.Done():
		return
	}
	slog.Info("warmup complete, starting task scheduler")

	running := make(map[int]*activeTask)

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("scheduler shutting down")
			for id, at := range running {
				at.cancel()
				slog.Info("cancelled task", "task_id", id)
			}
			workerCancel()
			for i := 0; i < 3; i++ {
				<-done
			}
			return
		case <-ticker.C:
			s.poll(ctx, running)
		}
	}
}

func (s *Scheduler) poll(ctx context.Context, running map[int]*activeTask) {
	// Reap finished tasks
	for id, at := range running {
		select {
		case err := <-at.doneCh:
			if err != nil {
				slog.Error("task crashed", "task_id", id, "error", err)
				_ = s.db.UpdateTaskRuntime(ctx, id,
					db.WithStatus("error"), db.WithError(err.Error()), db.WithTouchRunTime())
				_ = s.db.SetTaskDesiredStatus(ctx, id, "stopped")
			} else {
				slog.Info("task finished", "task_id", id)
			}
			delete(running, id)
		default:
		}
	}

	tasks, err := s.db.ListSyncTasks(ctx)
	if err != nil {
		slog.Error("list sync tasks failed", "error", err)
		return
	}

	dbTaskMap := make(map[int]struct{})
	for _, t := range tasks {
		dbTaskMap[t.ID] = struct{}{}
	}

	// Cancel tasks deleted from DB
	for id, at := range running {
		if _, exists := dbTaskMap[id]; !exists {
			at.cancel()
			slog.Info("task deleted from DB, cancelling", "task_id", id)
		}
	}

	for _, t := range tasks {
		_, isRunning := running[t.ID]

		if t.DesiredStatus == "running" {
			if isRunning {
				continue
			}

			// Favorites cooldown
			if t.Type == "favorites" && t.Status == "completed" && t.LastRunAt != nil {
				intervalHours := 6.0
				if v, ok := t.Config["run_interval_hours"]; ok {
					if f, ok := v.(float64); ok && f > 0 {
						intervalHours = f
					}
				}
				if time.Since(*t.LastRunAt) < time.Duration(intervalHours*float64(time.Hour)) {
					continue
				}
			}

			taskCtx, taskCancel := context.WithCancel(ctx)
			doneCh := make(chan error, 1)
			taskID := t.ID

			go func() {
				doneCh <- s.runTask(taskCtx, taskID)
			}()

			running[t.ID] = &activeTask{cancel: taskCancel, doneCh: doneCh}
			slog.Info("started task", "task_id", t.ID, "name", t.Name, "type", t.Type)

		} else if isRunning {
			running[t.ID].cancel()
			slog.Info("stop requested", "task_id", t.ID)
		}
	}
}

func (s *Scheduler) runTask(ctx context.Context, taskID int) error {
	slog.Info("task start", "task_id", taskID)

	for {
		select {
		case <-ctx.Done():
			runtime, _ := s.db.GetTaskRuntime(context.Background(), taskID)
			if runtime != nil && runtime.Status != "completed" && runtime.Status != "error" {
				_ = s.db.UpdateTaskRuntime(context.Background(), taskID,
					db.WithStatus("stopped"), db.WithTouchRunTime())
			}
			return nil
		default:
		}

		runtime, err := s.db.GetTaskRuntime(ctx, taskID)
		if err != nil {
			return fmt.Errorf("get task runtime: %w", err)
		}
		if runtime == nil {
			slog.Info("task deleted", "task_id", taskID)
			return nil
		}

		if runtime.DesiredStatus != "running" {
			if runtime.Status != "completed" && runtime.Status != "error" {
				_ = s.db.UpdateTaskRuntime(ctx, taskID,
					db.WithStatus("stopped"), db.WithTouchRunTime())
			}
			slog.Info("task stop requested", "task_id", taskID, "name", runtime.Name)
			return nil
		}

		_ = s.db.UpdateTaskRuntime(ctx, taskID, db.WithStatus("running"), db.WithError(""), db.WithTouchRunTime())

		switch runtime.Type {
		case "full":
			if err := task.ValidateFullTask(runtime); err != nil {
				_ = s.db.UpdateTaskRuntime(ctx, taskID,
					db.WithStatus("error"), db.WithError(err.Error()), db.WithTouchRunTime())
				_ = s.db.SetTaskDesiredStatus(ctx, taskID, "stopped")
				return nil
			}
			done, err := task.RunFullOnce(ctx, s.db, s.client, taskID, runtime, s.signals.GrouperTrigger)
			if err != nil {
				return err
			}
			if done {
				return nil
			}

		case "incremental":
			if err := task.ValidateIncrementalTask(runtime); err != nil {
				_ = s.db.UpdateTaskRuntime(ctx, taskID,
					db.WithStatus("error"), db.WithError(err.Error()), db.WithTouchRunTime())
				_ = s.db.SetTaskDesiredStatus(ctx, taskID, "stopped")
				return nil
			}
			done, err := task.RunIncrementalOnce(ctx, s.db, s.client, taskID, runtime, s.signals.GrouperTrigger)
			if err != nil {
				return err
			}
			if done {
				return nil
			}

		case "favorites":
			favSignals := &task.FavSignals{
				ScorerReset:    s.signals.ScorerReset,
				GrouperTrigger: s.signals.GrouperTrigger,
			}
			done, err := task.RunFavoritesOnce(ctx, s.db, s.client, s.cfg, taskID, runtime, favSignals)
			if err != nil {
				return err
			}
			if done {
				return nil
			}

		default:
			msg := fmt.Sprintf("unknown task type: %s", runtime.Type)
			_ = s.db.UpdateTaskRuntime(ctx, taskID,
				db.WithStatus("error"), db.WithError(msg), db.WithTouchRunTime())
			_ = s.db.SetTaskDesiredStatus(ctx, taskID, "stopped")
			return nil
		}
	}
}
