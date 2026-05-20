//go:build ignore

// Bulk-seeds the `institutes` + `institute_domains` tables from the
// open-source Hipo/university-domains-list dataset
// (https://github.com/Hipo/university-domains-list, MIT licensed,
// ~10K universities + their email domains across ~190 countries).
//
//	go run scripts/seed_universities.go [-country=India] [-source=URL]
//
// Idempotent — institute names are de-duplicated case-insensitively
// per country, and domains have a unique index so duplicates from
// the source list are silently skipped.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const defaultSource = "https://raw.githubusercontent.com/Hipo/university-domains-list/master/world_universities_and_domains.json"

type rawUniversity struct {
	Name      string   `json:"name"`
	Country   string   `json:"country"`
	AlphaTwo  string   `json:"alpha_two_code"`
	Domains   []string `json:"domains"`
	StateProv *string  `json:"state-province"`
	WebPages  []string `json:"web_pages"`
}

func main() {
	country := flag.String("country", "", "filter to a single country (e.g. India). Empty = seed all.")
	source := flag.String("source", defaultSource, "URL or local path to the JSON list")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Fatalf("godotenv: %v", err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DB_URL")), &gorm.Config{})
	if err != nil {
		log.Fatalf("open: %v", err)
	}

	raw, err := loadSource(*source)
	if err != nil {
		log.Fatalf("load source: %v", err)
	}
	log.Printf("loaded %d universities from %s", len(raw), *source)

	if *country != "" {
		filtered := raw[:0]
		for _, u := range raw {
			if strings.EqualFold(u.Country, *country) {
				filtered = append(filtered, u)
			}
		}
		raw = filtered
		log.Printf("after %q filter: %d universities", *country, len(raw))
	}

	// Bulk-insert institutes first (one row per university). Use
	// ON CONFLICT DO NOTHING via a NOT EXISTS guard since the table
	// doesn't have a uniqueness constraint on name.
	t0 := time.Now()
	instInserted, instMatched, err := upsertInstitutes(db, raw)
	if err != nil {
		log.Fatalf("upsert institutes: %v", err)
	}
	log.Printf("institutes: inserted=%d matched=%d (took %s)", instInserted, instMatched, time.Since(t0))

	// Map institute name → id so the domain inserts know what to
	// point at without a per-row lookup.
	idByName, err := mapInstituteIDsByName(db, raw)
	if err != nil {
		log.Fatalf("map ids: %v", err)
	}

	t1 := time.Now()
	domInserted, err := upsertDomains(db, raw, idByName)
	if err != nil {
		log.Fatalf("upsert domains: %v", err)
	}
	log.Printf("domains: inserted=%d (took %s)", domInserted, time.Since(t1))

	log.Printf("done in %s", time.Since(t0))
}

func loadSource(src string) ([]rawUniversity, error) {
	var data []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		resp, e := http.Get(src)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("source returned %d", resp.StatusCode)
		}
		data, err = io.ReadAll(resp.Body)
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		return nil, err
	}
	var out []rawUniversity
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// upsertInstitutes inserts every (name, country) tuple from `raw`
// that isn't already in the table. Returns counts of inserted vs.
// already-present rows.
//
// Batching keeps a single query under the CockroachDB cluster's
// per-statement size cap.
func upsertInstitutes(db *gorm.DB, raw []rawUniversity) (inserted, matched int, err error) {
	type batchRow struct{ name, country string }
	batch := make([]batchRow, 0, 500)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// One INSERT … SELECT round-trip per batch. NOT EXISTS gives
		// us idempotency without needing a unique constraint we
		// don't have on (name, country).
		args := make([]any, 0, len(batch)*2)
		values := make([]string, 0, len(batch))
		for i, b := range batch {
			// Explicit casts on the placeholders — CockroachDB's
			// planner can't infer the type from a bare $N inside a
			// VALUES row used in a derived table.
			values = append(values, fmt.Sprintf("(gen_random_uuid(), now(), now(), $%d::varchar, $%d::varchar)", i*2+1, i*2+2))
			args = append(args, b.name, b.country)
		}
		sql := fmt.Sprintf(`
			INSERT INTO institutes (id, created_at, updated_at, name, country)
			SELECT * FROM (VALUES %s) AS v(id, created_at, updated_at, name, country)
			WHERE NOT EXISTS (
				SELECT 1 FROM institutes i
				WHERE LOWER(i.name) = LOWER(v.name)
				  AND COALESCE(LOWER(i.country),'') = COALESCE(LOWER(v.country),'')
			)
		`, strings.Join(values, ","))
		res := db.Exec(sql, args...)
		if res.Error != nil {
			return res.Error
		}
		inserted += int(res.RowsAffected)
		matched += len(batch) - int(res.RowsAffected)
		batch = batch[:0]
		return nil
	}

	for _, u := range raw {
		name := strings.TrimSpace(u.Name)
		if name == "" {
			continue
		}
		batch = append(batch, batchRow{name: name, country: u.Country})
		if len(batch) >= 500 {
			if err := flush(); err != nil {
				return inserted, matched, err
			}
		}
	}
	if err := flush(); err != nil {
		return inserted, matched, err
	}
	return inserted, matched, nil
}

// mapInstituteIDsByName loads (name, country) → id for the slice of
// institutes we just touched. Lowercased keys to match the inserts.
func mapInstituteIDsByName(db *gorm.DB, raw []rawUniversity) (map[string]string, error) {
	wanted := map[string]struct{}{}
	for _, u := range raw {
		key := strings.ToLower(strings.TrimSpace(u.Name)) + "|" + strings.ToLower(u.Country)
		wanted[key] = struct{}{}
	}
	type row struct {
		ID      string
		Name    string
		Country string
	}
	out := map[string]string{}

	// Fetch in chunks — single SELECT on a 10K table is fine, but
	// using a `LOWER(name) IN (…)` is verbose and bumps into prepared
	// statement parameter limits. Instead, just SELECT all institutes
	// once and filter client-side.
	var rows []row
	if err := db.Raw(`SELECT id::text AS id, name, COALESCE(country,'') AS country FROM institutes`).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		key := strings.ToLower(strings.TrimSpace(r.Name)) + "|" + strings.ToLower(r.Country)
		if _, want := wanted[key]; want {
			out[key] = r.ID
		}
	}
	return out, nil
}

// upsertDomains inserts every (institute_id, domain) tuple. Domains
// have a unique index already, so a vanilla INSERT … ON CONFLICT
// DO NOTHING handles dedup naturally.
func upsertDomains(db *gorm.DB, raw []rawUniversity, idByName map[string]string) (int, error) {
	type batchRow struct {
		instituteID string
		domain      string
	}
	batch := make([]batchRow, 0, 500)
	inserted := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := make([]any, 0, len(batch)*2)
		values := make([]string, 0, len(batch))
		for i, b := range batch {
			values = append(values, fmt.Sprintf("(gen_random_uuid(), now(), now(), $%d::uuid, $%d)", i*2+1, i*2+2))
			args = append(args, b.instituteID, b.domain)
		}
		sql := fmt.Sprintf(`
			INSERT INTO institute_domains (id, created_at, updated_at, institute_id, domain)
			VALUES %s
			ON CONFLICT (domain) DO NOTHING
		`, strings.Join(values, ","))
		res := db.Exec(sql, args...)
		if res.Error != nil {
			return res.Error
		}
		inserted += int(res.RowsAffected)
		batch = batch[:0]
		return nil
	}

	seen := map[string]struct{}{}
	for _, u := range raw {
		key := strings.ToLower(strings.TrimSpace(u.Name)) + "|" + strings.ToLower(u.Country)
		id, ok := idByName[key]
		if !ok {
			continue
		}
		for _, d := range u.Domains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if _, dup := seen[d]; dup {
				continue
			}
			seen[d] = struct{}{}
			batch = append(batch, batchRow{instituteID: id, domain: d})
			if len(batch) >= 500 {
				if err := flush(); err != nil {
					return inserted, err
				}
			}
		}
	}
	return inserted, flush()
}
