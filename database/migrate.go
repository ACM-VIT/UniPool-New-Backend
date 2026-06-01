package database

// Schema migrations powered by goose. Migrations are run explicitly via the
// CLI, not automatically on service startup.

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

// migrationsDir is the path inside the embedded FS where SQL files live.
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

	// Stamp the baseline for pre-goose databases that already have the app schema.
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

// bootstrapIfPreGoose stamps the baseline migration for databases that already
// have the app schema but no applied goose baseline.
func bootstrapIfPreGoose(ctx context.Context, db *sql.DB) error {
	// Fresh databases should run the baseline normally.
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.users') IS NOT NULL`).Scan(&schemaExists); err != nil {
		return fmt.Errorf("checking users table existence: %w", err)
	}
	if !schemaExists {
		return nil
	}

	// Stamp version 1 only when goose has not already tracked the baseline.
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
