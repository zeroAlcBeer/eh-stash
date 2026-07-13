package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type TaskDef struct {
	ID                  int
	Name                string
	TaskKind            string
	Source              string
	Strategy            string
	Scope               map[string]any
	Enabled             bool
	Config              map[string]any
	Checkpoint          map[string]any
	Progress            map[string]any
	CurrentJobID        *int64
	LastJobID           *int64
	ScheduleKind        string
	ScheduleIntervalSec *int
	NextRunAt           *time.Time
	LastRunAt           *time.Time
	LastFinishedAt      *time.Time
	RequestedAction     *string
	RequestedAt         *time.Time
	LastError           *string
}

const taskDefSelectCols = `
	id, name, task_kind, source, strategy, scope,
	enabled, config, checkpoint, progress, current_job_id, last_job_id,
	schedule_kind, schedule_interval_sec, next_run_at, last_run_at,
	last_finished_at, requested_action, requested_at, last_error
`

func scanTaskDef(scan func(...any) error, t *TaskDef) error {
	var scopeJSON, cfgJSON, checkpointJSON, progressJSON []byte
	if err := scan(
		&t.ID, &t.Name, &t.TaskKind, &t.Source, &t.Strategy, &scopeJSON,
		&t.Enabled, &cfgJSON, &checkpointJSON, &progressJSON,
		&t.CurrentJobID, &t.LastJobID, &t.ScheduleKind,
		&t.ScheduleIntervalSec, &t.NextRunAt, &t.LastRunAt, &t.LastFinishedAt,
		&t.RequestedAction, &t.RequestedAt, &t.LastError,
	); err != nil {
		return err
	}
	t.Scope = map[string]any{}
	t.Config = map[string]any{}
	t.Checkpoint = map[string]any{}
	t.Progress = map[string]any{}
	_ = json.Unmarshal(scopeJSON, &t.Scope)
	_ = json.Unmarshal(cfgJSON, &t.Config)
	_ = json.Unmarshal(checkpointJSON, &t.Checkpoint)
	_ = json.Unmarshal(progressJSON, &t.Progress)
	return nil
}

func (d *DB) ListTaskDefs(ctx context.Context) ([]TaskDef, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+taskDefSelectCols+` FROM sync_task_defs ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var defs []TaskDef
	for rows.Next() {
		var t TaskDef
		if err := scanTaskDef(rows.Scan, &t); err != nil {
			return nil, err
		}
		defs = append(defs, t)
	}
	return defs, rows.Err()
}

func (d *DB) GetTaskDef(ctx context.Context, taskID int) (*TaskDef, error) {
	var t TaskDef
	row := d.pool.QueryRow(ctx, `SELECT `+taskDefSelectCols+` FROM sync_task_defs WHERE id = $1`, taskID)
	if err := scanTaskDef(row.Scan, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTaskDefCheckpoint writes a mid-run progress snapshot. Used by workers
// between slices to persist cursor/scanned_count/etc and the % display value.
// errMsg is written into last_error as-is (empty string clears it).
func (d *DB) UpdateTaskDefCheckpoint(
	ctx context.Context,
	taskID int,
	checkpoint map[string]any,
	pct float64,
	errMsg string,
	touchRunTime bool,
) error {
	if checkpoint == nil {
		checkpoint = map[string]any{}
	}
	checkpointJSON, _ := json.Marshal(checkpoint)
	progressJSON, _ := json.Marshal(map[string]any{"pct": pct})

	var lastError any
	if errMsg != "" {
		lastError = errMsg
	}

	runTimeExpr := "last_run_at"
	if touchRunTime {
		runTimeExpr = "NOW()"
	}

	sql := fmt.Sprintf(`
		UPDATE sync_task_defs
		SET checkpoint = $1::jsonb,
		    progress = $2::jsonb,
		    last_error = $3,
		    last_run_at = %s,
		    updated_at = NOW()
		WHERE id = $4
	`, runTimeExpr)
	_, err := d.pool.Exec(ctx, sql, string(checkpointJSON), string(progressJSON), lastError, taskID)
	return err
}

// MarkTaskDefFinished is called once at the end of a River job. terminal=true
// means the work unit is complete (worker is exiting): clears current_job_id,
// stamps last_finished_at, and disables manual-schedule defs so they don't
// auto-restart. terminal=false is for mid-run snapshots that don't fit
// UpdateTaskDefCheckpoint (kept for symmetry; currently unused).
func (d *DB) MarkTaskDefFinished(
	ctx context.Context,
	taskID int,
	jobID int64,
	checkpoint map[string]any,
	pct float64,
	terminal bool,
	errMsg string,
) error {
	if checkpoint == nil {
		checkpoint = map[string]any{}
	}
	checkpointJSON, _ := json.Marshal(checkpoint)
	progressJSON, _ := json.Marshal(map[string]any{"pct": pct})

	var lastError any
	if errMsg != "" {
		lastError = errMsg
	}

	_, err := d.pool.Exec(ctx, `
		UPDATE sync_task_defs
		SET enabled = CASE
		        WHEN schedule_kind = 'manual' AND $5 THEN FALSE
		        ELSE enabled
		    END,
		    checkpoint = $1::jsonb,
		    progress = $2::jsonb,
		    current_job_id = CASE WHEN $5::boolean THEN NULL::bigint ELSE $3::bigint END,
		    last_job_id = $3::bigint,
		    last_run_at = NOW(),
		    last_finished_at = CASE WHEN $5 THEN NOW() ELSE last_finished_at END,
		    requested_action = NULL,
		    requested_at = NULL,
		    last_error = $4,
		    updated_at = NOW()
		WHERE id = $6
	`, string(checkpointJSON), string(progressJSON), jobID, lastError, terminal, taskID)
	if err != nil {
		return err
	}
	var eventJobID *int64
	if jobID > 0 {
		eventJobID = &jobID
	}
	return d.InsertTaskEvent(ctx, taskID, eventJobID, "task.updated", "task updated", map[string]any{
		"terminal": terminal,
		"pct":      pct,
	})
}

func (d *DB) MarkTaskDefQueued(ctx context.Context, taskID int, jobID int64) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE sync_task_defs
		SET current_job_id = $1,
		    last_job_id = $1,
		    requested_action = NULL,
		    requested_at = NULL,
		    updated_at = NOW()
		WHERE id = $2
	`, jobID, taskID)
	return err
}

func (d *DB) ClearTaskDefCurrentJob(ctx context.Context, taskID int, jobID int64) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE sync_task_defs
		SET current_job_id = NULL,
		    updated_at = NOW()
		WHERE id = $1
		  AND current_job_id = $2
	`, taskID, jobID)
	return err
}

func (d *DB) ClearTerminalTaskDefCurrentJobs(ctx context.Context) (int64, error) {
	tag, err := d.pool.Exec(ctx, `
		UPDATE sync_task_defs d
		SET current_job_id = NULL,
		    updated_at = NOW()
		FROM river_job j
		WHERE d.current_job_id = j.id
		  AND j.state IN ('cancelled', 'completed', 'discarded')
	`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (d *DB) ClearTaskRequest(ctx context.Context, taskID int) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE sync_task_defs
		SET requested_action = NULL,
		    requested_at = NULL,
		    updated_at = NOW()
		WHERE id = $1
	`, taskID)
	return err
}

func (d *DB) InsertTaskEvent(ctx context.Context, taskID int, jobID *int64, eventType, message string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, _ := json.Marshal(payload)
	_, err := d.pool.Exec(ctx, `
		INSERT INTO sync_task_events (task_id, job_id, event_type, message, payload)
		VALUES ($1, $2, $3, $4, $5::jsonb)
	`, taskID, jobID, eventType, message, string(payloadJSON))
	return err
}
