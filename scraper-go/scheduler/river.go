package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/db"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/task"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/worker"
)

const (
	RiverQueue          = "sync"
	ManagerPollInterval = 5 * time.Second
	IncrementalInterval = 30 * time.Second
	SyncJobTimeout      = 30 * time.Minute
	SliceJobTimeout     = 30 * time.Minute
	KickJobTimeout      = 1 * time.Minute
	SliceMaxAttempts    = 2
)

type FullSyncArgs struct {
	TaskID int `json:"task_id" river:"unique"`
}

func (FullSyncArgs) Kind() string { return "ehstash_full_sync" }

type IncrementalSyncArgs struct {
	TaskID int `json:"task_id" river:"unique"`
}

func (IncrementalSyncArgs) Kind() string { return "ehstash_incremental_sync" }

// IncrementalSliceArgs is one page's worth of work. The chain re-enqueues
// itself with the same RunID until the round ends. A periodic kick assigns a
// fresh RunID for each new round; slices whose RunID no longer matches the
// task's current run are dropped. RunID is a string because JSONB numbers
// decode as float64 and lose precision for UnixNano-scale ints.
type IncrementalSliceArgs struct {
	TaskID int    `json:"task_id"`
	RunID  string `json:"run_id"`
}

func (IncrementalSliceArgs) Kind() string { return "ehstash_incremental_slice" }

type FavoritesSyncArgs struct {
	TaskID int `json:"task_id" river:"unique"`
}

func (FavoritesSyncArgs) Kind() string { return "ehstash_favorites_sync" }

type fullSyncWorker struct {
	river.WorkerDefaults[FullSyncArgs]
	s *Scheduler
}

func (w *fullSyncWorker) Work(ctx context.Context, job *river.Job[FullSyncArgs]) error {
	return w.s.workFull(ctx, job.Args.TaskID, job.ID)
}

func (w *fullSyncWorker) Timeout(*river.Job[FullSyncArgs]) time.Duration {
	return -1
}

type incrementalSyncWorker struct {
	river.WorkerDefaults[IncrementalSyncArgs]
	s *Scheduler
}

func (w *incrementalSyncWorker) Work(ctx context.Context, job *river.Job[IncrementalSyncArgs]) error {
	return w.s.workIncrementalKick(ctx, job.Args.TaskID)
}

func (w *incrementalSyncWorker) Timeout(*river.Job[IncrementalSyncArgs]) time.Duration {
	return KickJobTimeout
}

type incrementalSliceWorker struct {
	river.WorkerDefaults[IncrementalSliceArgs]
	s *Scheduler
}

func (w *incrementalSliceWorker) Work(ctx context.Context, job *river.Job[IncrementalSliceArgs]) error {
	return w.s.workIncrementalSlice(ctx, job.Args.TaskID, job.Args.RunID, job.ID)
}

func (w *incrementalSliceWorker) Timeout(*river.Job[IncrementalSliceArgs]) time.Duration {
	return SliceJobTimeout
}

type favoritesSyncWorker struct {
	river.WorkerDefaults[FavoritesSyncArgs]
	s *Scheduler
}

func (w *favoritesSyncWorker) Work(ctx context.Context, job *river.Job[FavoritesSyncArgs]) error {
	return w.s.workFavorites(ctx, job.Args.TaskID, job.ID)
}

func (w *favoritesSyncWorker) Timeout(*river.Job[FavoritesSyncArgs]) time.Duration {
	return SyncJobTimeout
}

func (s *Scheduler) runRiver(ctx context.Context) {
	if err := s.migrateRiver(ctx); err != nil {
		slog.Error("river migration failed", "error", err)
		return
	}
	if n, err := s.db.ClearTerminalTaskDefCurrentJobs(ctx); err != nil {
		slog.Error("clear terminal current jobs failed", "error", err)
	} else if n > 0 {
		slog.Info("cleared terminal current jobs", "count", n)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	if n, err := s.db.ResetStaleThumbProcessing(ctx); err != nil {
		slog.Error("[THUMB] reset stale processing failed", "error", err)
	} else if n > 0 {
		slog.Info("[THUMB] reset stale processing items", "count", n)
	}

	thumbWorkers := s.cfg.ThumbWorkers
	if thumbWorkers < 1 {
		thumbWorkers = 1
	}
	done := make(chan struct{}, thumbWorkers+2)

	for i := 0; i < thumbWorkers; i++ {
		workerID := i + 1
		go func() {
			worker.RunThumbWorker(workerCtx, workerID, s.db, s.client, s.cfg, s.thumbLimiter, s.signals.ThumbNotify)
			done <- struct{}{}
		}()
	}
	go func() {
		worker.RunEmbeddings(workerCtx, s.db, s.signals.ProfileUpdate)
		done <- struct{}{}
	}()
	go func() {
		worker.RunGalleryGrouper(workerCtx, s.db, s.signals.GrouperTrigger)
		done <- struct{}{}
	}()

	riverClient, err := s.newRiverClient(ctx)
	if err != nil {
		slog.Error("river client init failed", "error", err)
		workerCancel()
		return
	}
	s.riverClient = riverClient

	if err := riverClient.Start(ctx); err != nil {
		slog.Error("river client start failed", "error", err)
		workerCancel()
		return
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := riverClient.Stop(stopCtx); err != nil {
			slog.Error("river client stop failed", "error", err)
		}
	}()

	managerDone := make(chan struct{})
	go func() {
		defer close(managerDone)
		s.runRiverManager(ctx, riverClient)
	}()

	slog.Info("river scheduler started")
	<-ctx.Done()
	slog.Info("river scheduler shutting down")
	workerCancel()
	for i := 0; i < thumbWorkers+2; i++ {
		<-done
	}
	<-managerDone
}

func (s *Scheduler) migrateRiver(ctx context.Context) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(s.db.Pool()), nil)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

func (s *Scheduler) newRiverClient(ctx context.Context) (*river.Client[pgx.Tx], error) {
	defs, err := s.db.ListTaskDefs(ctx)
	if err != nil {
		return nil, err
	}

	periodicJobs := make([]*river.PeriodicJob, 0)
	for _, def := range defs {
		slog.Info("[SCHED] task def scanned",
			"task_id", def.ID,
			"source", def.Source,
			"strategy", def.Strategy,
			"schedule_kind", def.ScheduleKind,
			"interval_sec", def.ScheduleIntervalSec,
			"enabled", def.Enabled,
		)
		if def.Source == "favorites" && def.Strategy == "full" && def.ScheduleKind == "periodic" {
			interval := 6 * time.Hour
			if def.ScheduleIntervalSec != nil && *def.ScheduleIntervalSec > 0 {
				interval = time.Duration(*def.ScheduleIntervalSec) * time.Second
			}
			taskID := def.ID
			periodicJobs = append(periodicJobs, river.NewPeriodicJob(
				river.PeriodicInterval(interval),
				func() (river.JobArgs, *river.InsertOpts) {
					return FavoritesSyncArgs{TaskID: taskID}, uniqueInsertOpts()
				},
				&river.PeriodicJobOpts{ID: fmt.Sprintf("favorites-%d", taskID)},
			))
			slog.Info("[SCHED] periodic registered",
				"task_id", taskID,
				"id", fmt.Sprintf("favorites-%d", taskID),
				"interval", interval,
				"run_on_start", false,
			)
		}
		if def.Source == "gallery_list" && def.Strategy == "incremental" && def.ScheduleKind == "periodic" {
			taskID := def.ID
			interval := IncrementalInterval
			if def.ScheduleIntervalSec != nil && *def.ScheduleIntervalSec > 0 {
				interval = time.Duration(*def.ScheduleIntervalSec) * time.Second
			}
			periodicJobs = append(periodicJobs, river.NewPeriodicJob(
				river.PeriodicInterval(interval),
				func() (river.JobArgs, *river.InsertOpts) {
					return IncrementalSyncArgs{TaskID: taskID}, activeUniqueInsertOpts()
				},
				&river.PeriodicJobOpts{ID: fmt.Sprintf("incremental-%d", taskID), RunOnStart: true},
			))
			slog.Info("[SCHED] periodic registered",
				"task_id", taskID,
				"id", fmt.Sprintf("incremental-%d", taskID),
				"interval", interval,
				"run_on_start", true,
			)
		}
	}
	slog.Info("[SCHED] periodic registration complete",
		"task_defs", len(defs),
		"periodic_jobs", len(periodicJobs),
	)

	workers := river.NewWorkers()
	river.AddWorker(workers, &fullSyncWorker{s: s})
	river.AddWorker(workers, &incrementalSyncWorker{s: s})
	river.AddWorker(workers, &incrementalSliceWorker{s: s})
	river.AddWorker(workers, &favoritesSyncWorker{s: s})

	return river.NewClient(riverpgxv5.New(s.db.Pool()), &river.Config{
		PeriodicJobs: periodicJobs,
		Queues: map[string]river.QueueConfig{
			RiverQueue: {MaxWorkers: 1},
		},
		Workers: workers,
	})
}

func uniqueInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       RiverQueue,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// activeUniqueInsertOpts dedupes kicks while a previous kick of the same args
// is still in any non-terminal state (including completed is intentionally
// excluded so periodic can fire again after a finished round).
func activeUniqueInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       RiverQueue,
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRetryable,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	}
}

func (s *Scheduler) runRiverManager(ctx context.Context, riverClient *river.Client[pgx.Tx]) {
	ticker := time.NewTicker(ManagerPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.egress != nil {
				s.egress.Reconcile(ctx)
			}
			s.handleRequestedActions(ctx, riverClient)
		}
	}
}

func (s *Scheduler) handleRequestedActions(ctx context.Context, riverClient *river.Client[pgx.Tx]) {
	if n, err := s.db.ClearTerminalTaskDefCurrentJobs(ctx); err != nil {
		slog.Error("clear terminal current jobs failed", "error", err)
	} else if n > 0 {
		slog.Info("cleared terminal current jobs", "count", n)
	}

	defs, err := s.db.ListTaskDefs(ctx)
	if err != nil {
		slog.Error("list task defs failed", "error", err)
		return
	}
	for _, def := range defs {
		if def.RequestedAction == nil {
			continue
		}
		slog.Info("[SCHED] processing requested action",
			"task_id", def.ID,
			"action", *def.RequestedAction,
			"source", def.Source,
			"strategy", def.Strategy,
		)
		switch *def.RequestedAction {
		case "start":
			s.enqueueTaskDef(ctx, riverClient, def)
		case "stop":
			s.cancelTaskDef(ctx, riverClient, def)
		case "retry":
			s.retryTaskDef(ctx, riverClient, def)
		default:
			slog.Warn("[SCHED] unknown requested action", "task_id", def.ID, "action", *def.RequestedAction)
		}
	}
}

func (s *Scheduler) enqueueTaskDef(ctx context.Context, riverClient *river.Client[pgx.Tx], def db.TaskDef) {
	var jobID int64
	var jobKind string
	var err error
	switch {
	case def.Source == "gallery_list" && def.Strategy == "full":
		res, insertErr := riverClient.Insert(ctx, FullSyncArgs{TaskID: def.ID}, uniqueInsertOpts())
		err = insertErr
		if res != nil && res.Job != nil {
			jobID = res.Job.ID
			jobKind = res.Job.Kind
		}
	case def.Source == "gallery_list" && def.Strategy == "incremental":
		res, insertErr := riverClient.Insert(ctx, IncrementalSyncArgs{TaskID: def.ID}, activeUniqueInsertOpts())
		err = insertErr
		if res != nil && res.Job != nil {
			jobID = res.Job.ID
			jobKind = res.Job.Kind
		}
	case def.Source == "favorites" && def.Strategy == "full":
		res, insertErr := riverClient.Insert(ctx, FavoritesSyncArgs{TaskID: def.ID}, uniqueInsertOpts())
		err = insertErr
		if res != nil && res.Job != nil {
			jobID = res.Job.ID
			jobKind = res.Job.Kind
		}
	default:
		err = fmt.Errorf("unknown task definition source=%q strategy=%q", def.Source, def.Strategy)
	}
	if err != nil {
		slog.Error("[SCHED] enqueue task failed", "task_id", def.ID, "source", def.Source, "strategy", def.Strategy, "error", err)
		return
	}
	if jobID == 0 {
		slog.Warn("[SCHED] enqueue task skipped as duplicate", "task_id", def.ID, "source", def.Source, "strategy", def.Strategy)
		_ = s.db.ClearTaskRequest(ctx, def.ID)
		return
	}
	slog.Info("[SCHED] enqueue task ok",
		"task_id", def.ID,
		"job_id", jobID,
		"kind", jobKind,
		"source", def.Source,
		"strategy", def.Strategy,
	)
	if err := s.db.MarkTaskDefQueued(ctx, def.ID, jobID); err != nil {
		slog.Error("[SCHED] mark task queued failed", "task_id", def.ID, "job_id", jobID, "error", err)
	}
	_ = s.db.InsertTaskEvent(ctx, def.ID, &jobID, "job.queued", "job queued", map[string]any{
		"kind": jobKind,
	})
}

func (s *Scheduler) cancelTaskDef(ctx context.Context, riverClient *river.Client[pgx.Tx], def db.TaskDef) {
	if def.CurrentJobID == nil {
		slog.Info("[SCHED] cancel: no active job, settle directly", "task_id", def.ID)
		_ = s.db.MarkTaskDefFinished(ctx, def.ID, 0, def.Checkpoint, 0, true, "")
		return
	}
	slog.Info("[SCHED] cancel river job", "task_id", def.ID, "job_id", *def.CurrentJobID)
	if _, err := riverClient.JobCancel(ctx, *def.CurrentJobID); err != nil {
		slog.Error("[SCHED] cancel river job failed", "task_id", def.ID, "job_id", *def.CurrentJobID, "error", err)
		return
	}
	jobID := *def.CurrentJobID
	_ = s.db.ClearTaskRequest(ctx, def.ID)
	_ = s.db.InsertTaskEvent(ctx, def.ID, &jobID, "job.cancel_requested", "job cancel requested", nil)
}

func (s *Scheduler) retryTaskDef(ctx context.Context, riverClient *river.Client[pgx.Tx], def db.TaskDef) {
	if def.LastJobID == nil {
		slog.Info("[SCHED] retry: no last job, enqueue fresh", "task_id", def.ID)
		s.enqueueTaskDef(ctx, riverClient, def)
		return
	}
	slog.Info("[SCHED] retry river job", "task_id", def.ID, "job_id", *def.LastJobID)
	if _, err := riverClient.JobRetry(ctx, *def.LastJobID); err != nil {
		slog.Error("[SCHED] retry river job failed", "task_id", def.ID, "job_id", *def.LastJobID, "error", err)
		return
	}
	jobID := *def.LastJobID
	_ = s.db.ClearTaskRequest(ctx, def.ID)
	_ = s.db.InsertTaskEvent(ctx, def.ID, &jobID, "job.retry_requested", "job retry requested", nil)
}

func (s *Scheduler) workFull(ctx context.Context, taskID int, jobID int64) error {
	slog.Info("[FULL ] entered", "task_id", taskID, "job_id", jobID)
	if err := s.db.MarkTaskDefQueued(ctx, taskID, jobID); err != nil {
		slog.Error("[FULL ] mark current failed", "task_id", taskID, "job_id", jobID, "error", err)
	}
	for {
		def, err := s.db.GetTaskDef(ctx, taskID)
		if err != nil {
			slog.Error("[FULL ] get def failed", "task_id", taskID, "job_id", jobID, "error", err)
			return err
		}
		if err := task.ValidateFullTask(def); err != nil {
			slog.Warn("[FULL ] validate failed", "task_id", taskID, "job_id", jobID, "error", err)
			_ = s.db.MarkTaskDefFinished(ctx, taskID, jobID, def.Checkpoint, 0, true, err.Error())
			return err
		}
		done, err := task.RunFullOnce(ctx, s.db, s.client, def, s.signals.GrouperTrigger)
		if err != nil {
			slog.Error("[FULL ] run error", "task_id", taskID, "job_id", jobID, "error", err)
			_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, def.Checkpoint, 0, true, err.Error())
			return err
		}
		if done {
			slog.Info("[FULL ] finished", "task_id", taskID, "job_id", jobID)
			return s.db.MarkTaskDefFinished(ctx, taskID, jobID, def.Checkpoint, 100, true, "")
		}
		select {
		case <-ctx.Done():
			slog.Warn("[FULL ] cancelled by ctx", "task_id", taskID, "job_id", jobID, "ctx_err", ctx.Err())
			_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, def.Checkpoint, 0, true, ctx.Err().Error())
			return ctx.Err()
		default:
		}
	}
}

// workIncrementalKick is the periodic/manual entry point. It does NOT do work
// itself — it just decides whether to start a new slice chain (fresh round) or
// resume an in-progress one. Slices are short jobs; the kick is short too.
func (s *Scheduler) workIncrementalKick(ctx context.Context, taskID int) error {
	slog.Info("[INCR ] kick entered", "task_id", taskID)
	def, err := s.db.GetTaskDef(ctx, taskID)
	if err != nil {
		slog.Error("[INCR ] kick get def failed", "task_id", taskID, "error", err)
		return err
	}
	if !def.Enabled {
		slog.Info("[INCR ] kick skip: def disabled", "task_id", taskID)
		return nil
	}
	if err := task.ValidateIncrementalTask(def); err != nil {
		slog.Warn("[INCR ] kick skip: validate failed", "task_id", taskID, "error", err)
		return err
	}

	// If a slice chain with a live run_id is already in flight, do nothing.
	if runID := checkpointRunID(def.Checkpoint); runID != "" && def.CurrentJobID != nil {
		slog.Info("[INCR ] kick skip: chain in flight",
			"task_id", taskID,
			"run_id", runID,
			"current_job_id", *def.CurrentJobID,
		)
		return nil
	}

	newRunID := strconv.FormatInt(time.Now().UnixNano(), 10)
	checkpoint := cloneStateMap(def.Checkpoint)
	// Fresh round: reset all per-round state.
	checkpoint["run_id"] = newRunID
	checkpoint["next_gid"] = nil
	checkpoint["scanned_count"] = float64(0)
	checkpoint["latest_gid"] = nil
	if err := s.db.UpdateTaskDefCheckpoint(ctx, taskID, checkpoint, 0, "", true); err != nil {
		slog.Error("[INCR ] kick persist new run_id failed", "task_id", taskID, "error", err)
		return err
	}
	slog.Info("[INCR ] kick starting new round", "task_id", taskID, "run_id", newRunID)

	res, err := s.riverClient.Insert(ctx, IncrementalSliceArgs{TaskID: taskID, RunID: newRunID}, sliceInsertOpts())
	if err != nil {
		slog.Error("[INCR ] kick enqueue first slice failed", "task_id", taskID, "run_id", newRunID, "error", err)
		return fmt.Errorf("enqueue first slice: %w", err)
	}
	if res != nil && res.Job != nil {
		slog.Info("[INCR ] kick first slice enqueued",
			"task_id", taskID,
			"run_id", newRunID,
			"slice_job_id", res.Job.ID,
		)
		_ = s.db.MarkTaskDefQueued(ctx, taskID, res.Job.ID)
		_ = s.db.InsertTaskEvent(ctx, taskID, &res.Job.ID, "round.started", "incremental round started", map[string]any{
			"run_id": newRunID,
		})
	}
	return nil
}

func (s *Scheduler) workIncrementalSlice(ctx context.Context, taskID int, runID string, jobID int64) error {
	slog.Info("[INCR ] slice entered", "task_id", taskID, "job_id", jobID, "run_id", runID)
	def, err := s.db.GetTaskDef(ctx, taskID)
	if err != nil {
		slog.Error("[INCR ] slice get def failed", "task_id", taskID, "job_id", jobID, "error", err)
		return err
	}
	if !def.Enabled {
		slog.Info("[INCR ] slice skip: def disabled", "task_id", taskID, "job_id", jobID)
		_ = s.db.ClearTaskDefCurrentJob(context.Background(), taskID, jobID)
		return nil
	}

	// Drop slices that belong to a stale round (cancelled, restarted, etc).
	if cur := checkpointRunID(def.Checkpoint); cur != runID {
		slog.Info("[INCR ] slice dropped: stale run_id",
			"task_id", taskID,
			"job_id", jobID,
			"slice_run", runID,
			"current_run", cur,
		)
		_ = s.db.ClearTaskDefCurrentJob(context.Background(), taskID, jobID)
		return nil
	}

	if err := s.db.MarkTaskDefQueued(ctx, taskID, jobID); err != nil {
		slog.Error("[INCR ] slice mark current failed", "task_id", taskID, "job_id", jobID, "error", err)
	}

	if err := task.ValidateIncrementalTask(def); err != nil {
		slog.Warn("[INCR ] slice validate failed", "task_id", taskID, "job_id", jobID, "error", err)
		_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, def.Checkpoint, 0, true, err.Error())
		return err
	}

	result, runErr := task.RunIncrementalSlice(ctx, s.db, s.client, def, s.signals.GrouperTrigger)
	if runErr != nil {
		// ctx cancelled or upstream DB failure — let River retry / mark cancelled.
		if ctx.Err() != nil {
			slog.Warn("[INCR ] slice cancelled by ctx", "task_id", taskID, "job_id", jobID, "run_id", runID, "ctx_err", ctx.Err())
			cp := cloneStateMap(result.Checkpoint)
			cp["run_id"] = nil
			_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, cp, result.Pct, true, "cancelled")
		} else {
			slog.Error("[INCR ] slice run error", "task_id", taskID, "job_id", jobID, "run_id", runID, "error", runErr)
		}
		return runErr
	}

	checkpoint := result.Checkpoint
	switch result.ExitReason {
	case "END", "WINDOW":
		roundNum := getCheckpointInt(checkpoint, "round")
		checkpoint["next_gid"] = nil
		checkpoint["scanned_count"] = float64(0)
		checkpoint["latest_gid"] = nil
		checkpoint["run_id"] = nil
		checkpoint["round"] = float64(roundNum + 1)
		_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, checkpoint, 100, true, "")
		_ = s.db.InsertTaskEvent(context.Background(), taskID, &jobID, "round.finished", "incremental round finished", map[string]any{
			"reason": result.ExitReason,
			"run_id": runID,
		})
		slog.Info("[INCR ] slice round finished",
			"task_id", taskID,
			"job_id", jobID,
			"run_id", runID,
			"reason", result.ExitReason,
			"next_round", roundNum+1,
		)
		return nil
	case "BANNED", "ERROR":
		// Pause the round so the next periodic kick starts fresh.
		checkpoint["run_id"] = nil
		_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, checkpoint, result.Pct, true, "exit: "+result.ExitReason)
		slog.Warn("[INCR ] slice round paused",
			"task_id", taskID,
			"job_id", jobID,
			"run_id", runID,
			"reason", result.ExitReason,
			"pct", result.Pct,
		)
		return nil
	default:
		// Persist cursor then chain the next slice.
		if err := s.db.UpdateTaskDefCheckpoint(context.Background(), taskID, checkpoint, result.Pct, "", false); err != nil {
			slog.Error("[INCR ] slice persist checkpoint failed", "task_id", taskID, "job_id", jobID, "error", err)
			return fmt.Errorf("persist slice checkpoint: %w", err)
		}
		res, err := s.riverClient.Insert(context.Background(), IncrementalSliceArgs{TaskID: taskID, RunID: runID}, sliceInsertOpts())
		if err != nil {
			slog.Error("[INCR ] slice enqueue next failed", "task_id", taskID, "job_id", jobID, "run_id", runID, "error", err)
			return fmt.Errorf("enqueue next slice: %w", err)
		}
		if res != nil && res.Job != nil {
			slog.Info("[INCR ] slice chained next",
				"task_id", taskID,
				"job_id", jobID,
				"next_job_id", res.Job.ID,
				"run_id", runID,
				"reason", result.ExitReason,
				"pct", result.Pct,
			)
			_ = s.db.MarkTaskDefQueued(context.Background(), taskID, res.Job.ID)
		}
		return nil
	}
}

func checkpointRunID(state map[string]any) string {
	v, ok := state["run_id"]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := v.(float64); ok {
		return strconv.FormatInt(int64(f), 10)
	}
	return ""
}

func getCheckpointInt(state map[string]any, key string) int {
	v, ok := state[key]
	if !ok {
		return 0
	}
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func cloneStateMap(state map[string]any) map[string]any {
	out := make(map[string]any, len(state)+4)
	for k, v := range state {
		out[k] = v
	}
	return out
}

func sliceInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       RiverQueue,
		MaxAttempts: SliceMaxAttempts,
	}
}

func (s *Scheduler) workFavorites(ctx context.Context, taskID int, jobID int64) error {
	slog.Info("[FAV  ] entered", "task_id", taskID, "job_id", jobID)
	if err := s.db.MarkTaskDefQueued(ctx, taskID, jobID); err != nil {
		slog.Error("[FAV  ] mark current failed", "task_id", taskID, "job_id", jobID, "error", err)
	}
	def, err := s.db.GetTaskDef(ctx, taskID)
	if err != nil {
		slog.Error("[FAV  ] get def failed", "task_id", taskID, "job_id", jobID, "error", err)
		return err
	}
	if !def.Enabled {
		slog.Info("[FAV  ] skip: def disabled", "task_id", taskID, "job_id", jobID)
		return nil
	}
	favSignals := &task.FavSignals{
		ProfileUpdate:  s.signals.ProfileUpdate,
		GrouperTrigger: s.signals.GrouperTrigger,
	}
	runErr := task.RunFavoritesOnce(ctx, s.db, s.client, s.cfg, def, favSignals)
	final, _ := s.db.GetTaskDef(context.Background(), taskID)
	checkpoint := def.Checkpoint
	pct := 0.0
	if final != nil {
		checkpoint = final.Checkpoint
		if v, ok := final.Progress["pct"].(float64); ok {
			pct = v
		}
	}
	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}
	_ = s.db.MarkTaskDefFinished(context.Background(), taskID, jobID, checkpoint, pct, true, errMsg)
	if runErr != nil {
		slog.Error("[FAV  ] finished with error", "task_id", taskID, "job_id", jobID, "pct", pct, "error", runErr)
	} else {
		slog.Info("[FAV  ] finished", "task_id", taskID, "job_id", jobID, "pct", pct)
	}
	return runErr
}
