package users

import (
	"log"
	"strings"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// SeedDefaultInstitutes guarantees the launch institutes exist. Called
// from main on startup (idempotent — uses ON-CONFLICT semantics via
// "WHERE NOT EXISTS"). New institutes are added by editing the slice
// below + redeploying. Long-term, admins create them via a dashboard.
func SeedDefaultInstitutes() {
	type seed struct {
		Name    string
		Country string
		Domains []string
	}
	seeds := []seed{
		{
			Name:    "Vellore Institute of Technology",
			Country: "India",
			Domains: []string{"vit.ac.in", "vitstudent.ac.in", "vitap.ac.in", "vitbhopal.ac.in"},
		},
	}

	for _, s := range seeds {
		var inst models.Institute
		err := database.Database.Db.Where("name = ?", s.Name).First(&inst).Error
		if err != nil {
			inst = models.Institute{Name: s.Name, Country: s.Country}
			if err := database.Database.Db.Create(&inst).Error; err != nil {
				log.Printf("institutes seed: create %q failed: %v", s.Name, err)
				continue
			}
		}
		for _, raw := range s.Domains {
			d := strings.ToLower(strings.TrimSpace(raw))
			if d == "" {
				continue
			}
			var existing models.InstituteDomain
			if err := database.Database.Db.Where("domain = ?", d).First(&existing).Error; err == nil {
				continue
			}
			if err := database.Database.Db.Create(&models.InstituteDomain{
				Domain:      d,
				InstituteID: inst.ID,
			}).Error; err != nil {
				log.Printf("institutes seed: domain %q failed: %v", d, err)
			}
		}
	}
}

// ListInstitutes returns the small public catalogue — used by the
// frontend to display institute names on profile / ride cards. No
// auth required; the data is non-sensitive.
func ListInstitutes(c *fiber.Ctx) error {
	var list []models.Institute
	if err := database.Database.Db.Order("name asc").Find(&list).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to fetch institutes",
		})
	}
	return c.JSON(fiber.Map{"institutes": list})
}

// SearchInstitutes powers the university picker in the verify flow.
// `q` is the typeahead query; we want short, fast responses while
// the user is typing, so the limit is small (20) and we shortcut
// empty queries.
//
// Response shape:
//
//	{
//	  "institutes": [
//	    {"id":"…", "name":"Vellore Institute of Technology",
//	     "country":"India",
//	     "domains":["vit.ac.in","vitstudent.ac.in", …]}
//	    , …
//	  ]
//	}
//
// Domains are returned inline so the frontend can:
//
//   1. Pre-fill an `@<domain>` placeholder on the email input
//   2. Validate the entered email before hitting /verify/start
//      (saving a server round-trip on obvious mismatches)
func SearchInstitutes(c *fiber.Ctx) error {
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if q == "" {
		return c.JSON(fiber.Map{"institutes": []any{}})
	}

	// Match against three different fields so users can find their
	// institute by whatever they remember:
	//   1) Natural-name substring ("vellore", "indian institute")
	//   2) Acronym from uppercase letters in the name ("VIT", "IIT-D")
	//   3) Email domain prefix/substring ("vitstudent" → VIT, "iitb"
	//      → IIT Bombay). Domains are stored in institute_domains —
	//      we join via EXISTS so each institute appears once even if
	//      multiple domains match.
	pattern := "%" + q + "%"
	prefixPattern := q + "%"
	// Strip non-letters from the raw query before treating it as an
	// acronym attempt — handles "I.I.T.", "IIT-D", "vit ", etc.
	var letters []rune
	for _, r := range strings.ToUpper(q) {
		if r >= 'A' && r <= 'Z' {
			letters = append(letters, r)
		}
	}
	acronym := ""
	acronymPattern := ""
	if len(letters) >= 2 && len(letters) <= 7 {
		acronym = string(letters)
		acronymPattern = acronym + "%"
	}

	// Relevance ordering — tightest match wins:
	//   1) exact acronym match
	//   2) acronym prefix
	//   3) name starts with query
	//   4) ANY domain starts with query
	//   5) name contains query
	//   6) (default) — must be a domain substring match
	// Within a tier, shorter names rank first so canonical entries
	// like "Vellore Institute of Technology" beat the longer
	// "Vellore Institute of Technology, Vellore" variant.
	//
	// Limited to 15 — 20 was scrollable noise, 15 fits the visible
	// list comfortably while still covering acronym-ambiguous queries
	// (e.g. "IIT" matches every campus).
	var institutes []models.Institute
	var sql string
	var args []any
	if acronym != "" {
		sql = `
			SELECT * FROM institutes i
			WHERE LOWER(i.name) LIKE ?
			   OR REGEXP_REPLACE(i.name, '[^A-Z]', '', 'g') LIKE ?
			   OR EXISTS (
			     SELECT 1 FROM institute_domains d
			      WHERE d.institute_id = i.id
			        AND LOWER(d.domain) LIKE ?
			   )
			ORDER BY
				CASE
					WHEN REGEXP_REPLACE(i.name, '[^A-Z]', '', 'g') = ? THEN 1
					WHEN REGEXP_REPLACE(i.name, '[^A-Z]', '', 'g') LIKE ? THEN 2
					WHEN LOWER(i.name) LIKE ? THEN 3
					WHEN EXISTS (
					  SELECT 1 FROM institute_domains d
					   WHERE d.institute_id = i.id
					     AND LOWER(d.domain) LIKE ?
					) THEN 4
					WHEN LOWER(i.name) LIKE ? THEN 5
					ELSE 6
				END ASC,
				LENGTH(i.name) ASC,
				i.name ASC
			LIMIT 15`
		args = []any{
			pattern, acronymPattern, pattern,
			acronym, acronymPattern, prefixPattern, prefixPattern, pattern,
		}
	} else {
		sql = `
			SELECT * FROM institutes i
			WHERE LOWER(i.name) LIKE ?
			   OR EXISTS (
			     SELECT 1 FROM institute_domains d
			      WHERE d.institute_id = i.id
			        AND LOWER(d.domain) LIKE ?
			   )
			ORDER BY
				CASE
					WHEN LOWER(i.name) LIKE ? THEN 1
					WHEN EXISTS (
					  SELECT 1 FROM institute_domains d
					   WHERE d.institute_id = i.id
					     AND LOWER(d.domain) LIKE ?
					) THEN 2
					WHEN LOWER(i.name) LIKE ? THEN 3
					ELSE 4
				END ASC,
				LENGTH(i.name) ASC,
				i.name ASC
			LIMIT 15`
		args = []any{
			pattern, pattern,
			prefixPattern, prefixPattern, pattern,
		}
	}
	if err := database.Database.Db.Raw(sql, args...).Scan(&institutes).Error; err != nil {
		log.Printf("SearchInstitutes: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "search failed",
		})
	}

	if len(institutes) == 0 {
		return c.JSON(fiber.Map{"institutes": []any{}})
	}

	// One follow-up query for all domains across matched institutes,
	// then bucket them by institute id. Avoids N+1.
	ids := make([]any, len(institutes))
	for i, inst := range institutes {
		ids[i] = inst.ID
	}
	var domains []models.InstituteDomain
	if err := database.Database.Db.
		Where("institute_id IN ?", ids).
		Find(&domains).Error; err != nil {
		log.Printf("SearchInstitutes: domains: %v", err)
		// Domains-less fallback rather than 500.
		domains = nil
	}
	domainsByInst := map[string][]string{}
	for _, d := range domains {
		key := d.InstituteID.String()
		domainsByInst[key] = append(domainsByInst[key], d.Domain)
	}

	type result struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Country string   `json:"country,omitempty"`
		Domains []string `json:"domains"`
	}
	out := make([]result, 0, len(institutes))
	for _, inst := range institutes {
		key := inst.ID.String()
		out = append(out, result{
			ID:      key,
			Name:    inst.Name,
			Country: inst.Country,
			Domains: domainsByInst[key],
		})
	}
	return c.JSON(fiber.Map{"institutes": out})
}
