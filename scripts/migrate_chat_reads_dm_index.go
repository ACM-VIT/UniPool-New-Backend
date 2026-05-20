//go:build ignore

// Targeted schema migration: adds a unique index on chat_reads
// (user_id, dm_room_id) so the DM mark-read endpoint can use a
// straightforward `ON CONFLICT` upsert. Without it, MarkDMRead
// falls back to a UPDATE-then-INSERT pattern that races under
// concurrent calls.
//
// Partial index — only enforces uniqueness when dm_room_id IS NOT
// NULL, so existing ride-only rows (which already share the
// idx_chat_reads_user_ride uniqueness on user_id+ride_id) aren't
// double-constrained.
//
//	go run scripts/migrate_chat_reads_dm_index.go
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
			"unique (user_id, dm_room_id) where not null",
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_reads_user_dm
			   ON chat_reads (user_id, dm_room_id)
			   WHERE dm_room_id IS NOT NULL`,
		},
	}

	for _, s := range steps {
		if err := db.Exec(s.sql).Error; err != nil {
			log.Fatalf("%s: %v", s.name, err)
		}
		fmt.Printf("✓ %s\n", s.name)
	}
}
