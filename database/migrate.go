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
// numbered .sql files in `migsource/migrations/`, a tracked
// `goose_db_version` table, and an explicit `up` / `down` / `status` CLI.

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

// migrationsDir is the path inside the embedded FS (see migsource package)
// where the .sql files live. This is the embed-relative path, not the
// on-disk path. On disk the files are under migsource/migrations/.
const migrationsDir = "migrations"

// baselineVersion is the first migration version stamped as applied
// when cutting over from GORM AutoMigrate to goose. This constant is
// paired with assertions in stampBaseline and bootstrapIfPreGoose to
// enforce the invariant that it is always positive.
const baselineVersion int64 = 1

// assert panics when condition is false. Assertion failures indicate
// programmer error, not operating error. The only correct response
// to corrupt code is to crash.
func assert(ok bool, msg string) {
	if !ok {
		panic("database/migrate.go: assert: " + msg)
	}
}

// RunMigrations applies any pending goose migrations against the
// configured DB_URL. Called from `unipool-backend migrate`; never
// from the serve path.
func RunMigrations(migrationsFS fs.FS) error {
	assert(migrationsFS != nil, "migrationsFS must not be nil")

	sqlDB, err := openMigrateDB()
	if err != nil {
		return fmt.Errorf("opening migrate db: %w", err)
	}
	defer sqlDB.Close()
	assert(sqlDB != nil, "openMigrateDB must return non-nil db")

	goose.SetBaseFS(migrationsFS)

	err = goose.SetDialect("postgres")
	if err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	err = bootstrapIfPreGoose(context.Background(), sqlDB)
	if err != nil {
		return fmt.Errorf("bootstrapping: %w", err)
	}

	err = goose.Up(sqlDB, migrationsDir)
	if err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	log.Println("Migrations applied.")
	return nil
}

// MigrationStatus prints the current migration state to stdout. Used by
// `unipool-backend migrate-status` for ops visibility.
func MigrationStatus(migrationsFS fs.FS) error {
	assert(migrationsFS != nil, "migrationsFS must not be nil")

	sqlDB, err := openMigrateDB()
	if err != nil {
		return fmt.Errorf("opening migrate db: %w", err)
	}
	defer sqlDB.Close()
	assert(sqlDB != nil, "openMigrateDB must return non-nil db")

	goose.SetBaseFS(migrationsFS)

	err = goose.SetDialect("postgres")
	if err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	return goose.Status(sqlDB, migrationsDir)
}

// openMigrateDB opens a *sql.DB using pgx driver and verifies the
// connection with Ping.
func openMigrateDB() (*sql.DB, error) {
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DB_URL not set")
	}
	assert(len(dsn) > 0, "DB_URL must be non-empty after environment lookup")

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening DB: %w", err)
	}
	assert(sqlDB != nil, "sql.Open must return non-nil on success")

	err = sqlDB.Ping()
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("pinging DB: %w", err)
	}
	assert(sqlDB != nil, "sqlDB must still be non-nil after successful ping")

	return sqlDB, nil
}

// bootstrapIfPreGoose detects a database that was previously managed by
// GORM AutoMigrate and stamps the baseline migration as applied without
// executing it. Fresh databases skip this entirely — goose.Up handles
// them normally.
//
// The two-step goose check is required because goose creates its
// tracking table lazily on any API call, including read-only
// migrate-status. Table existence alone does not indicate migration 1
// was applied. Only MAX(version_id) >= 1 is the definitive signal.
//
// Post-condition — schema state after returning nil:
//
//	┌─────────────────────┬──────────────────┬──────────────────────────┐
//	│ Entry condition     │ Action taken     │ Schema state on exit     │
//	├─────────────────────┼──────────────────┼──────────────────────────┤
//	│ Fresh DB            │ No-op            │ No application tables    │
//	│ (users table        │                  │ exist. goose.Up will     │
//	│  does not exist)    │                  │ create everything from   │
//	│                     │                  │ migration 1.             │
//	├─────────────────────┼──────────────────┼──────────────────────────┤
//	│ Pre-goose DB        │ goose_db_version │ goose_db_version exists  │
//	│ (users exists,      │ created,         │ with version 0 and 1     │
//	│  MAX(version_id)    │ version 0 + 1    │ stamped as applied.      │
//	│  < 1 or table       │ stamped.         │ goose.Up skips 1,        │
//	│  missing)           │                  │ starts from migration 2. │
//	├─────────────────────┼──────────────────┼──────────────────────────┤
//	│ Already tracked     │ No-op            │ Unchanged.               │
//	│ (MAX(version_id)    │                  │ goose.Up continues from  │
//	│  >= 1)              │                  │ the next pending.        │
//	└─────────────────────┴──────────────────┴──────────────────────────┘
//
// The post-stamp re-read assertion guarantees the write persisted, so
// the "Pre-goose DB" row is verified — not assumed.
func bootstrapIfPreGoose(ctx context.Context, db *sql.DB) error {
	assert(ctx != nil, "ctx must not be nil")
	assert(db != nil, "db must not be nil")

	schemaExists, err := checkSchemaExists(ctx, db)
	if err != nil {
		return fmt.Errorf("checking schema: %w", err)
	}

	// No application tables exist at all → this is a fresh database,
	// not a cutover from GORM AutoMigrate. goose.Up will create
	// everything including goose_db_version as part of migration 1.
	// Bootstrapping would interfere: stamping baseline as applied
	// when no tables exist would cause goose to skip migration 1,
	// leaving the schema empty.
	if !schemaExists {
		return nil
	}

	gooseTableExists, err := checkGooseTableExists(ctx, db)
	if err != nil {
		return fmt.Errorf("checking goose table: %w", err)
	}

	maxVersion := int64(0)
	if gooseTableExists {
		maxVersion, err = readMaxVersion(ctx, db)
		if err != nil {
			return fmt.Errorf("reading max version: %w", err)
		}
	}

	assert(maxVersion >= 0, "maxVersion must be non-negative after read")

	if maxVersion >= baselineVersion {
		return nil
	}

	err = stampBaseline(ctx, db)
	if err != nil {
		return fmt.Errorf("stamping baseline: %w", err)
	}

	verified, err := readMaxVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("verifying stamp: %w", err)
	}
	assert(verified >= baselineVersion, "stamp must persist version >= 1")

	return nil
}

// checkSchemaExists returns true if the users table exists in the
// public schema, indicating a pre-goose database.
func checkSchemaExists(ctx context.Context, db *sql.DB) (bool, error) {
	assert(ctx != nil, "ctx must not be nil")
	assert(db != nil, "db must not be nil")

	var exists bool
	err := db.QueryRowContext(ctx,
		`SELECT to_regclass('public.users') IS NOT NULL`,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("querying users table: %w", err)
	}
	assert(db != nil, "db must not be mutated by checkSchemaExists")
	return exists, nil
}

// checkGooseTableExists returns true if the goose tracking table
// exists in the public schema.
func checkGooseTableExists(ctx context.Context, db *sql.DB) (bool, error) {
	assert(ctx != nil, "ctx must not be nil")
	assert(db != nil, "db must not be nil")

	var exists bool
	err := db.QueryRowContext(ctx,
		`SELECT to_regclass('public.goose_db_version') IS NOT NULL`,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("querying goose_db_version: %w", err)
	}
	return exists, nil
}

// readMaxVersion returns the highest version_id in goose_db_version,
// or 0 if the table is empty or does not exist (caller should check
// existence first via checkGooseTableExists).
func readMaxVersion(ctx context.Context, db *sql.DB) (int64, error) {
	assert(ctx != nil, "ctx must not be nil")
	assert(db != nil, "db must not be nil")

	var version sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT MAX(version_id) FROM goose_db_version`,
	).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("querying max version: %w", err)
	}

	if !version.Valid {
		return 0, nil
	}
	assert(version.Int64 >= 0, "version_id must be non-negative")
	return version.Int64, nil
}

// stampBaseline creates the goose tracking table if needed, inserts
// the sentinel version 0, and marks baselineVersion as applied.
// Every statement is idempotent (CREATE IF NOT EXISTS, INSERT with
// WHERE NOT EXISTS / VALUES that tolerate re-execution).
func stampBaseline(ctx context.Context, db *sql.DB) error {
	assert(ctx != nil, "ctx must not be nil")
	assert(db != nil, "db must not be nil")

	log.Println("Pre-goose database detected: stamping baseline migration as applied.")

	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS goose_db_version (
			id         SERIAL PRIMARY KEY,
			version_id INT8 NOT NULL,
			is_applied BOOL NOT NULL,
			tstamp     TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("creating goose_db_version: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied)
		SELECT 0, true
		WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 0)
	`)
	if err != nil {
		return fmt.Errorf("inserting sentinel: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied)
		VALUES ($1, true)
	`, baselineVersion)
	if err != nil {
		return fmt.Errorf("stamping baseline %d: %w", baselineVersion, err)
	}

	assert(baselineVersion >= 1, "baselineVersion must be >= 1")
	return nil
}
