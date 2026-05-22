package database

// Schema migrations powered by goose.
//
// Design intent: migrations are NOT run automatically on service
// startup. The service binary opens the DB connection and assumes the
// schema matches the model definitions; if it doesn't, that's a deploy
// mistake and the operator should run `unipool-backend migrate` before
// bringing the new code online.
//
// The previous setup ran GORM's AutoMigrate on every boot, which
// (a) executed unreviewed DDL against a live production DB on every
// service restart, (b) was non-idempotent in subtle ways (GORM kept
// emitting DROP CONSTRAINT for legacy-named uniques that had already
// been dropped), and (c) gave us nothing in terms of versioning,
// rollback, or migration history. goose replaces all of that with
// numbered .sql files in `migrations/`, a tracked `goose_db_version`
// table, and an explicit `up` / `down` / `status` CLI.

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationsDir is the path inside the embedded FS where the .sql files
// live. main.go owns the embed directive and hands us the FS so the
// embed pattern can be relative to the project root.
const migrationsDir = "migrations"

// RunMigrations applies any pending goose migrations against the
// configured DB_URL. Called from `unipool-backend migrate`; never
// from the serve path.
func RunMigrations(migrationsFS fs.FS) error {
	sqlDB, err := openMigrateDB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	// Bootstrap a pre-goose DB. If goose's bookkeeping table is missing
	// but the application schema clearly already exists, mark the
	// baseline migration as applied without actually running it. This
	// is what lets us roll goose out against the existing production
	// DB without re-creating tables.
	if err := bootstrapIfPreGoose(context.Background(), sqlDB); err != nil {
		return fmt.Errorf("bootstrapping goose state: %w", err)
	}

	if err := goose.Up(sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	log.Println("Migrations applied.")
	return nil
}

// MigrationStatus prints the current migration state to stdout. Used by
// `unipool-backend migrate-status` for ops visibility.
func MigrationStatus(migrationsFS fs.FS) error {
	sqlDB, err := openMigrateDB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	return goose.Status(sqlDB, migrationsDir)
}

func openMigrateDB() (*sql.DB, error) {
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DB_URL not set")
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening DB: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("pinging DB: %w", err)
	}

	return sqlDB, nil
}

// bootstrapIfPreGoose detects the "we're rolling goose out against a
// database that was previously schema-managed by GORM AutoMigrate"
// case, and stamps the baseline migration as applied without running
// it.
//
// The check is "does the application schema already exist, and does
// goose not yet know about it?" rather than "does the goose tracking
// table exist?" — because goose creates `goose_db_version` lazily on
// its first read (e.g. `migrate-status`) before any migration has
// actually been applied. Keying off the tracking table's existence
// would miss the case where someone ran `migrate-status` first and
// then `migrate`, which is exactly what an operator does the first
// time they want to know what's pending.
func bootstrapIfPreGoose(ctx context.Context, db *sql.DB) error {
	// Has the application schema been created by some prior tool
	// (i.e. GORM AutoMigrate before we cut over)? If not, this is a
	// fresh database and goose.Up should run the baseline normally.
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.users') IS NOT NULL`).Scan(&schemaExists); err != nil {
		return fmt.Errorf("checking users table existence: %w", err)
	}
	if !schemaExists {
		return nil
	}

	// Does goose already think the baseline is applied? Two cases:
	//   1. goose_db_version doesn't exist at all → definitely not applied
	//   2. it exists but max(version_id) < 1 → still not applied
	// In either case, we need to stamp version 1. Otherwise it's
	// already tracked and we're done.
	var gooseTableExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&gooseTableExists); err != nil {
		return fmt.Errorf("checking goose_db_version existence: %w", err)
	}

	if gooseTableExists {
		var maxVersion sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version`).Scan(&maxVersion); err != nil {
			return fmt.Errorf("reading goose_db_version: %w", err)
		}
		if maxVersion.Valid && maxVersion.Int64 >= 1 {
			return nil
		}
	}

	log.Println("Pre-goose database detected: stamping baseline migration 00001 as applied without running it.")

	if !gooseTableExists {
		// Create the tracking table by hand so we control exactly what
		// goes into it. Schema mirrors what goose's own initializer
		// produces.
		if _, err := db.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS goose_db_version (
				id SERIAL PRIMARY KEY,
				version_id INT8 NOT NULL,
				is_applied BOOL NOT NULL,
				tstamp TIMESTAMPTZ NOT NULL DEFAULT now()
			)
		`); err != nil {
			return fmt.Errorf("creating goose_db_version: %w", err)
		}
	}

	// Sentinel row at version 0 (goose looks for it on init) only if
	// it's missing; subsequent calls to goose.Up will see version 1 as
	// the high-water mark and treat the baseline as applied.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied)
		SELECT 0, true
		WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 0)
	`); err != nil {
		return fmt.Errorf("inserting goose sentinel row: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, true)`); err != nil {
		return fmt.Errorf("stamping baseline as applied: %w", err)
	}

	return nil
}
