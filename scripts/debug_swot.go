//go:build ignore

// Quick post-seed health check: counts + a few sanity rows.
//	go run scripts/debug_swot.go
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

	var instCount, domCount int64
	db.Raw(`SELECT count(*) FROM institutes`).Scan(&instCount)
	db.Raw(`SELECT count(*) FROM institute_domains`).Scan(&domCount)
	fmt.Printf("institutes=%d domains=%d\n", instCount, domCount)

	type row struct {
		ID     string
		Name   string
		Domain string
	}
	var vit []row
	db.Raw(`
		SELECT i.id::text AS id, i.name, d.domain
		FROM institutes i
		LEFT JOIN institute_domains d ON d.institute_id = i.id
		WHERE LOWER(i.name) LIKE '%vellore%'
		ORDER BY i.name, d.domain
		LIMIT 20
	`).Scan(&vit)
	fmt.Println("\n--- Vellore matches ---")
	for _, r := range vit {
		fmt.Printf("  %s  domain=%s\n", r.Name, r.Domain)
	}

	var harvard []row
	db.Raw(`
		SELECT i.id::text AS id, i.name, d.domain
		FROM institutes i
		LEFT JOIN institute_domains d ON d.institute_id = i.id
		WHERE LOWER(i.name) LIKE '%harvard%'
		ORDER BY i.name, d.domain
		LIMIT 20
	`).Scan(&harvard)
	fmt.Println("\n--- Harvard matches ---")
	for _, r := range harvard {
		fmt.Printf("  %s  domain=%s\n", r.Name, r.Domain)
	}
}
