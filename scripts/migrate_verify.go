//go:build ignore

// Targeted schema migration: adds the `institute_email` column on
// `users` and creates the `email_verifications` table. AutoMigrate
// is currently broken for existing tables (GORM tries to drop
// constraints that don't exist), so we apply the new shape with raw
// idempotent SQL.
//
//	go run scripts/migrate_verify.go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatalf("godotenv: %v", err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DB_URL")), &gorm.Config{})
	if err != nil {
		log.Fatalf("open: %v", err)
	}

	steps := []struct {
		name string
		sql  string
	}{
		{
			"add users.institute_email",
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS institute_email VARCHAR(120)`,
		},
		{
			"create email_verifications",
			`CREATE TABLE IF NOT EXISTS email_verifications (
				id          UUID PRIMARY KEY,
				created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
				deleted_at  TIMESTAMPTZ,
				user_id     UUID NOT NULL,
				email       VARCHAR(120) NOT NULL,
				code_hash   VARCHAR(64) NOT NULL,
				salt        VARCHAR(32) NOT NULL,
				expires_at  TIMESTAMPTZ NOT NULL,
				attempts    INT NOT NULL DEFAULT 0,
				consumed_at TIMESTAMPTZ
			)`,
		},
		{
			"index email_verifications.user_id",
			`CREATE INDEX IF NOT EXISTS idx_email_verifications_user_id ON email_verifications (user_id)`,
		},
		{
			"index email_verifications.email",
			`CREATE INDEX IF NOT EXISTS idx_email_verifications_email ON email_verifications (email)`,
		},
		{
			"index email_verifications.expires_at",
			`CREATE INDEX IF NOT EXISTS idx_email_verifications_expires_at ON email_verifications (expires_at)`,
		},
		{
			"index email_verifications.deleted_at",
			`CREATE INDEX IF NOT EXISTS idx_email_verifications_deleted_at ON email_verifications (deleted_at)`,
		},
	}

	for _, s := range steps {
		if err := db.Exec(s.sql).Error; err != nil {
			log.Fatalf("%s: %v", s.name, err)
		}
		fmt.Printf("✓ %s\n", s.name)
	}
}
