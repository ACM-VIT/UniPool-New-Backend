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

	// Substring match against the lowercased name covers natural
	// queries ("vellore", "indian institute"). For short queries
	// we ALSO match against the institute's acronym — the string
	// formed by every uppercase ASCII letter in the name. That
	// makes "VIT" find "Vellore Institute of Technology", "IIT"
	// find every IIT campus, "BITS" find "Birla Institute of
	// Technology and Science", etc. without the user needing to
	// know the full name.
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

	// Relevance ordering — exact acronym match wins, then acronym
	// prefix, then name-starts-with, then any substring match.
	// Within a tier, shorter names rank first so canonical entries
	// like "Vellore Institute of Technology" beat the longer
	// "Vellore Institute of Technology, Vellore" variant.
	//
	// Hand-written SQL because GORM's chained `Order` doesn't bind
	// arguments — the CASE expression needs `?` placeholders.
	var institutes []models.Institute
	var sql string
	var args []any
	if acronym != "" {
		sql = `
			SELECT * FROM institutes
			WHERE LOWER(name) LIKE ?
			   OR REGEXP_REPLACE(name, '[^A-Z]', '', 'g') LIKE ?
			ORDER BY
				CASE
					WHEN REGEXP_REPLACE(name, '[^A-Z]', '', 'g') = ? THEN 1
					WHEN REGEXP_REPLACE(name, '[^A-Z]', '', 'g') LIKE ? THEN 2
					WHEN LOWER(name) LIKE ? THEN 3
					ELSE 4
				END ASC,
				LENGTH(name) ASC,
				name ASC
			LIMIT 20`
		args = []any{pattern, acronymPattern, acronym, acronymPattern, prefixPattern}
	} else {
		sql = `
			SELECT * FROM institutes
			WHERE LOWER(name) LIKE ?
			ORDER BY
				CASE WHEN LOWER(name) LIKE ? THEN 1 ELSE 2 END ASC,
				LENGTH(name) ASC,
				name ASC
			LIMIT 20`
		args = []any{pattern, prefixPattern}
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
