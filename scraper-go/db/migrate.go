package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5"
)

// RunMigrations applies the SQL files in dir (sorted by filename) that are not
// yet recorded in schema_migrations. Legacy deployments predate this runner:
// if schema_migrations is empty but the core schema already exists, every
// current file is recorded as applied without being executed (baseline), so
// only files added after this point actually run.
//
// River manages its own schema separately via rivermigrate.
func (d *DB) RunMigrations(ctx context.Context, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("list migrations in %s: %w", dir, err)
	}
	if len(files) == 0 {
		if _, statErr := os.Stat(dir); statErr != nil {
			return fmt.Errorf("migrations dir %s: %w", dir, statErr)
		}
		return fmt.Errorf("migrations dir %s contains no .sql files", dir)
	}
	sort.Strings(files)

	if _, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := d.pool.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return err
		}
		applied[f] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if len(applied) == 0 {
		var legacy bool
		if err := d.pool.QueryRow(ctx,
			`SELECT to_regclass('public.eh_galleries') IS NOT NULL`,
		).Scan(&legacy); err != nil {
			return fmt.Errorf("detect legacy schema: %w", err)
		}
		if legacy {
			return d.baselineMigrations(ctx, files)
		}
	}

	for _, path := range files {
		name := filepath.Base(path)
		if applied[name] {
			continue
		}
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := d.applyMigration(ctx, name, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		slog.Info("[MIGRT] applied", "file", name)
	}
	return nil
}

// baselineMigrations records every current file as applied without executing.
// Used exactly once, when the runner first meets a DB that was migrated by
// hand (or by postgres initdb.d) before schema_migrations existed.
func (d *DB) baselineMigrations(ctx context.Context, files []string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, path := range files {
		name := filepath.Base(path)
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
		); err != nil {
			return fmt.Errorf("baseline %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	slog.Warn("[MIGRT] existing schema detected with empty schema_migrations; "+
		"baselined current files WITHOUT executing them",
		"count", len(files))
	return nil
}

func (d *DB) applyMigration(ctx context.Context, name, sqlText string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Simple protocol: migration files contain multiple statements.
	if _, err := tx.Exec(ctx, sqlText, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
