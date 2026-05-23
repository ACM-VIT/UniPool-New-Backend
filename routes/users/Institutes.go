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
		Domain  string
	}
	seeds := []seed{
		{
			Name:    "Vellore Institute of Technology",
			Country: "India",
			Domain:  "vitstudent.ac.in",
		},
	}

	for _, s := range seeds {
		var inst models.Institute
		err := database.Database.Db.Where("name = ?", s.Name).First(&inst).Error
		if err != nil {
			inst = models.Institute{
				Name:    s.Name,
				Country: s.Country,
				Domain:  &s.Domain,
			}
			if err := database.Database.Db.Create(&inst).Error; err != nil {
				log.Printf("institutes seed: create %q failed: %v", s.Name, err)
			}
			// bulk-upsert style: already exists → update domain if stale
		} else if inst.Domain == nil || *inst.Domain != s.Domain {
			if upErr := database.Database.Db.Model(&inst).Update("domain", s.Domain).Error; upErr != nil {
				log.Printf("institutes seed: update domain %q -> %s failed: %v", s.Name, s.Domain, upErr)
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
// the user is typing, so the limit is small (15) and we shortcut
// empty queries.
//
// Response shape:
//
//	{
//	  "institutes": [
//	    {"id":"…", "name":"Vellore Institute of Technology",
//	     "country":"India", "domain":"vitstudent.ac.in"}
//	    , …
//	  ]
//	}
//
// The domain is returned inline so the frontend can pre-fill an
// `@<domain>` placeholder on the email input.
func SearchInstitutes(c *fiber.Ctx) error {
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if q == "" {
		return c.JSON(fiber.Map{"institutes": []any{}})
	}

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

	var institutes []models.Institute
	var sql string
	var args []any
	if acronym != "" {
		sql = `
			SELECT * FROM institutes i
			WHERE LOWER(i.name) LIKE ?
			   OR REGEXP_REPLACE(i.name, '[^A-Z]', '', 'g') LIKE ?
			   OR LOWER(i.domain) LIKE ?
			ORDER BY
				CASE
					WHEN REGEXP_REPLACE(i.name, '[^A-Z]', '', 'g') = ? THEN 1
					WHEN REGEXP_REPLACE(i.name, '[^A-Z]', '', 'g') LIKE ? THEN 2
					WHEN LOWER(i.name) LIKE ? THEN 3
					WHEN LOWER(i.domain) LIKE ? THEN 4
					WHEN LOWER(i.name) LIKE ? THEN 5
					ELSE 6
				END ASC,
				CASE
					WHEN LOWER(i.domain) LIKE ? THEN 0
					ELSE 1
				END ASC,
				LENGTH(i.name) ASC,
				i.name ASC
			LIMIT 15`
		args = []any{
			pattern, acronymPattern, pattern,
			acronym, acronymPattern, prefixPattern, prefixPattern, pattern,
			prefixPattern,
		}
	} else {
		sql = `
			SELECT * FROM institutes i
			WHERE LOWER(i.name) LIKE ?
			   OR LOWER(i.domain) LIKE ?
			ORDER BY
				CASE
					WHEN LOWER(i.name) LIKE ? THEN 1
					WHEN LOWER(i.domain) LIKE ? THEN 2
					WHEN LOWER(i.name) LIKE ? THEN 3
					ELSE 4
				END ASC,
				CASE
					WHEN LOWER(i.domain) LIKE ? THEN 0
					ELSE 1
				END ASC,
				LENGTH(i.name) ASC,
				i.name ASC
			LIMIT 15`
		args = []any{
			pattern, pattern,
			prefixPattern, prefixPattern, pattern,
			prefixPattern,
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

	type result struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Country string  `json:"country,omitempty"`
		Domain  *string `json:"domain,omitempty"`
	}
	out := make([]result, 0, len(institutes))
	for _, inst := range institutes {
		out = append(out, result{
			ID:      inst.ID.String(),
			Name:    inst.Name,
			Country: inst.Country,
			Domain:  inst.Domain,
		})
	}
	return c.JSON(fiber.Map{"institutes": out})
}
