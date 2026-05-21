//go:build ignore

// Seeds `institutes` + `institute_domains` from JetBrains/swot
// (https://github.com/JetBrains/swot, MIT licensed) — the dataset
// JetBrains uses to decide who gets a student licence. We prefer it
// to flat lists (e.g. Hipo's) because swot enumerates STUDENT-
// specific subdomains alongside the institutional one — for VIT it
// has vit.ac.in (faculty) AND vitstudent.ac.in / vitalum.ac.in
// (students / alumni), which is exactly the distinction we care
// about when verifying who's allowed to ride student carpools.
//
// Structure of the data: each leaf `.txt` file under `lib/domains/`
// names a domain. The path components are the domain SEGMENTS in
// REVERSE — so `in/ac/vitstudent.txt` → "vitstudent.ac.in". The
// file content is the institution name on the first non-empty,
// non-special line.
//
// Usage:
//
//	# Clone or pull the repo somewhere on disk, then:
//	go run scripts/seed_swot.go -root=/tmp/swot/lib/domains [-wipe]
//
// `-wipe` deletes the existing institute_domains + institutes
// rows before seeding. Use it once when switching data sources.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Heuristic ccTLD → country mapping for the picker's display. Not
// exhaustive — the long tail just shows up countryless. The picker
// is searchable, so missing country isn't a discoverability hit.
var ccTLDCountry = map[string]string{
	"in": "India", "us": "United States", "uk": "United Kingdom",
	"ca": "Canada", "au": "Australia", "nz": "New Zealand",
	"de": "Germany", "fr": "France", "it": "Italy", "es": "Spain",
	"pt": "Portugal", "ch": "Switzerland", "at": "Austria",
	"nl": "Netherlands", "be": "Belgium", "lu": "Luxembourg",
	"se": "Sweden", "no": "Norway", "dk": "Denmark", "fi": "Finland",
	"is": "Iceland", "ie": "Ireland", "pl": "Poland", "cz": "Czech Republic",
	"sk": "Slovakia", "hu": "Hungary", "ro": "Romania", "bg": "Bulgaria",
	"gr": "Greece", "tr": "Turkey", "il": "Israel", "ru": "Russia",
	"ua": "Ukraine", "by": "Belarus", "lt": "Lithuania", "lv": "Latvia",
	"ee": "Estonia", "jp": "Japan", "kr": "South Korea", "cn": "China",
	"tw": "Taiwan", "hk": "Hong Kong", "sg": "Singapore", "my": "Malaysia",
	"id": "Indonesia", "th": "Thailand", "vn": "Vietnam", "ph": "Philippines",
	"pk": "Pakistan", "bd": "Bangladesh", "lk": "Sri Lanka", "np": "Nepal",
	"ae": "United Arab Emirates", "sa": "Saudi Arabia", "qa": "Qatar",
	"kw": "Kuwait", "om": "Oman", "bh": "Bahrain", "jo": "Jordan",
	"lb": "Lebanon", "eg": "Egypt", "ma": "Morocco", "tn": "Tunisia",
	"za": "South Africa", "ng": "Nigeria", "ke": "Kenya", "gh": "Ghana",
	"ug": "Uganda", "tz": "Tanzania", "rw": "Rwanda", "et": "Ethiopia",
	"br": "Brazil", "ar": "Argentina", "mx": "Mexico", "cl": "Chile",
	"co": "Colombia", "pe": "Peru", "uy": "Uruguay", "ve": "Venezuela",
	"ec": "Ecuador",
}

// Hard-blocklist of domains swot lists that we explicitly never
// want to surface in the picker — typically institutional / faculty
// domains that collide with a student-only counterpart we DO want.
//   - `vit.ac.in` is VIT's faculty domain; the student equivalent
//     is `vitstudent.ac.in`. Leaving the faculty domain in made it
//     the obvious auto-fill choice and silently locked students
//     out of verification.
var domainExclusions = map[string]bool{
	"vit.ac.in": true,
}

// Non-domain top-level segments in swot's tree that we want to
// keep treating as domains (city / region gTLDs). Everything else
// at the root is either a country code (handled by ccTLDCountry)
// or a real gTLD like `edu`, `org`, etc.
var nonCountryTLDs = map[string]string{
	"edu": "United States", // overwhelming majority — accept a few false positives
	"barcelona": "Spain", "paris": "France",
	"wales": "United Kingdom", "scot": "United Kingdom",
	"berlin": "Germany", "moscow": "Russia",
	"london": "United Kingdom",
}

type institute struct {
	name    string
	country string
	domains []string
}

func main() {
	root := flag.String("root", "/tmp/swot/lib/domains", "path to swot/lib/domains")
	wipe := flag.Bool("wipe", false, "delete existing institutes + institute_domains before seeding")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Fatalf("godotenv: %v", err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DB_URL")), &gorm.Config{})
	if err != nil {
		log.Fatalf("open: %v", err)
	}

	if _, err := os.Stat(*root); err != nil {
		log.Fatalf("root %q not found — clone JetBrains/swot first:\n  git clone --depth 1 https://github.com/JetBrains/swot.git /tmp/swot", *root)
	}

	t0 := time.Now()
	domains, err := walkSwot(*root)
	if err != nil {
		log.Fatalf("walk: %v", err)
	}
	log.Printf("walked %d swot entries in %s", len(domains), time.Since(t0))

	// Group domains by institution name. Same name across countries
	// gets collapsed into one institute row — close enough for the
	// picker, and avoids piling on duplicate "MIT" entries that all
	// resolve to the same school.
	//
	// Belt+braces on UTF-8 here: even though pickInstitutionName
	// sanitizes the file content, some swot files have characters
	// that look fine to Go but trip the Postgres wire protocol
	// (e.g. continuation bytes without a leading byte after a
	// trim). One last validation pass before we hit the DB.
	byName := map[string]*institute{}
	for _, d := range domains {
		name := strings.ToValidUTF8(d.name, "?")
		country := strings.ToValidUTF8(d.country, "")
		domain := strings.ToValidUTF8(d.domain, "")
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || domain == "" {
			continue
		}
		ent, ok := byName[key]
		if !ok {
			ent = &institute{name: name, country: country}
			byName[key] = ent
		}
		ent.domains = append(ent.domains, domain)
	}
	log.Printf("grouped into %d institutes", len(byName))

	if *wipe {
		log.Println("wiping existing institute_domains + institutes …")
		if err := db.Exec(`DELETE FROM institute_domains`).Error; err != nil {
			log.Fatalf("wipe institute_domains: %v", err)
		}
		if err := db.Exec(`DELETE FROM institutes`).Error; err != nil {
			log.Fatalf("wipe institutes: %v", err)
		}
	}

	institutes := make([]*institute, 0, len(byName))
	for _, v := range byName {
		institutes = append(institutes, v)
	}

	t1 := time.Now()
	instInserted, err := upsertInstitutes(db, institutes)
	if err != nil {
		log.Fatalf("upsert institutes: %v", err)
	}
	log.Printf("institutes: inserted=%d (took %s)", instInserted, time.Since(t1))

	idByName, err := mapInstituteIDsByName(db)
	if err != nil {
		log.Fatalf("map ids: %v", err)
	}

	t2 := time.Now()
	domInserted, err := upsertDomains(db, institutes, idByName)
	if err != nil {
		log.Fatalf("upsert domains: %v", err)
	}
	log.Printf("domains: inserted=%d (took %s)", domInserted, time.Since(t2))

	log.Printf("done in %s", time.Since(t0))
}

type domainRecord struct {
	domain  string
	name    string
	country string
}

// walkSwot collects one domainRecord per `.txt` file in the swot
// tree. The path REVERSED gives the FQDN; the first non-empty,
// non-special line of file content is the institution name.
func walkSwot(root string) ([]domainRecord, error) {
	var out []domainRecord
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".txt") {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		segs := strings.Split(rel, string(filepath.Separator))
		// strip ".txt" off the leaf
		leaf := strings.TrimSuffix(segs[len(segs)-1], ".txt")
		segs[len(segs)-1] = leaf
		// reverse the path components to assemble the FQDN
		for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
			segs[i], segs[j] = segs[j], segs[i]
		}
		fqdn := strings.ToLower(strings.Join(segs, "."))
		if fqdn == "" {
			return nil
		}
		// Skip the hard-blocklist (faculty domains we don't want
		// showing up in the picker — see `domainExclusions`).
		if domainExclusions[fqdn] {
			return nil
		}

		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		name := pickInstitutionName(string(raw))
		if name == "" {
			return nil
		}
		country := countryFor(segs)
		out = append(out, domainRecord{domain: fqdn, name: name, country: country})
		return nil
	})
	return out, err
}

// pickInstitutionName returns the first non-empty, non-special line
// from a swot data file. Special lines: starting with `.` (group
// markers) or containing `://` (website links bolted on at the end
// of the file).
//
// Truncated to 200 chars to fit the `institutes.name` column; the
// long tail of swot's data has occasional paragraph-length first
// lines (department descriptions, addresses) that aren't useful
// for the picker anyway.
func pickInstitutionName(content string) string {
	for _, line := range strings.Split(content, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, ".") {
			continue
		}
		if strings.Contains(s, "://") {
			continue
		}
		if !utf8.ValidString(s) {
			// Some swot files have Latin-1 / Windows-1252 bytes that
			// look fine in a text editor but bomb the Postgres wire
			// protocol. Replace each bad rune with the Unicode
			// replacement char so the row still seeds cleanly.
			s = strings.ToValidUTF8(s, "?")
		}
		if len(s) > 200 {
			s = strings.TrimSpace(s[:200])
		}
		return s
	}
	return ""
}

// countryFor maps the rightmost path segment(s) to a country name.
// `segs` here is the REVERSED list — so segs[len-1] is the TLD.
func countryFor(segs []string) string {
	if len(segs) == 0 {
		return ""
	}
	tld := segs[len(segs)-1]
	if c, ok := ccTLDCountry[tld]; ok {
		return c
	}
	if c, ok := nonCountryTLDs[tld]; ok {
		return c
	}
	return ""
}

func upsertInstitutes(db *gorm.DB, institutes []*institute) (int, error) {
	// 200-row batches keep each statement well under CockroachDB
	// Serverless' implicit transaction byte budget. Bigger batches
	// were getting truncated silently on the wire.
	const batchSize = 200
	inserted := 0
	for i := 0; i < len(institutes); i += batchSize {
		end := i + batchSize
		if end > len(institutes) {
			end = len(institutes)
		}
		batch := institutes[i:end]
		args := make([]any, 0, len(batch)*2)
		values := make([]string, 0, len(batch))
		for k, b := range batch {
			values = append(values, fmt.Sprintf("(gen_random_uuid(), now(), now(), $%d::varchar, $%d::varchar)", k*2+1, k*2+2))
			args = append(args, b.name, b.country)
		}
		sql := fmt.Sprintf(`
			INSERT INTO institutes (id, created_at, updated_at, name, country)
			SELECT * FROM (VALUES %s) AS v(id, created_at, updated_at, name, country)
			WHERE NOT EXISTS (
				SELECT 1 FROM institutes i
				WHERE LOWER(i.name) = LOWER(v.name)
			)
		`, strings.Join(values, ","))
		res := db.Exec(sql, args...)
		if res.Error != nil {
			log.Printf("upsertInstitutes batch %d-%d failed: %v", i, end, res.Error)
			return inserted, res.Error
		}
		inserted += int(res.RowsAffected)
		if (i/batchSize)%10 == 0 {
			log.Printf("  …institutes: %d/%d", end, len(institutes))
		}
	}
	return inserted, nil
}

// mapInstituteIDsByName returns a `lowercase(name)` → `id` map for
// every row in the institutes table. Pulls once; small enough at
// ~10K rows to fit in memory comfortably.
func mapInstituteIDsByName(db *gorm.DB) (map[string]string, error) {
	type row struct {
		ID   string
		Name string
	}
	var rows []row
	if err := db.Raw(`SELECT id::text AS id, name FROM institutes`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(strings.TrimSpace(r.Name))] = r.ID
	}
	return out, nil
}

func upsertDomains(db *gorm.DB, institutes []*institute, idByName map[string]string) (int, error) {
	type entry struct {
		instituteID string
		domain      string
	}
	pending := make([]entry, 0)
	seen := map[string]struct{}{}
	for _, inst := range institutes {
		id, ok := idByName[strings.ToLower(strings.TrimSpace(inst.name))]
		if !ok {
			continue
		}
		for _, d := range inst.domains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if _, dup := seen[d]; dup {
				continue
			}
			seen[d] = struct{}{}
			pending = append(pending, entry{instituteID: id, domain: d})
		}
	}

	const batchSize = 200
	inserted := 0
	log.Printf("upsertDomains: %d pending rows", len(pending))
	for i := 0; i < len(pending); i += batchSize {
		end := i + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[i:end]
		args := make([]any, 0, len(batch)*2)
		values := make([]string, 0, len(batch))
		for k, b := range batch {
			values = append(values, fmt.Sprintf("(gen_random_uuid(), now(), now(), $%d::uuid, $%d::varchar)", k*2+1, k*2+2))
			args = append(args, b.instituteID, b.domain)
		}
		sql := fmt.Sprintf(`
			INSERT INTO institute_domains (id, created_at, updated_at, institute_id, domain)
			VALUES %s
			ON CONFLICT (domain) DO NOTHING
		`, strings.Join(values, ","))
		res := db.Exec(sql, args...)
		if res.Error != nil {
			log.Printf("upsertDomains batch %d-%d failed: %v", i, end, res.Error)
			return inserted, res.Error
		}
		inserted += int(res.RowsAffected)
		if (i/batchSize)%10 == 0 {
			log.Printf("  …domains: %d/%d", end, len(pending))
		}
	}
	return inserted, nil
}
