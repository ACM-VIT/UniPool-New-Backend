package users

import (
	"log"
	"strings"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// SeedDefaultInstitutes keeps launch institutes and allowed student domains
// present. It is idempotent and runs during startup.
func SeedDefaultInstitutes() {
	// Domains that should never appear in student verification choices.
	legacyDomainExclusions := []string{"vit.ac.in"}
	for _, d := range legacyDomainExclusions {
		if err := database.Database.Db.
			Exec(`DELETE FROM institute_domains WHERE LOWER(domain) = ?`, strings.ToLower(d)).
			Error; err != nil {
			log.Printf("institutes seed: purge %q failed: %v", d, err)
		}
	}

	type seed struct {
		Name    string
		Country string
		Domains []string
	}
	seeds := []seed{
		{
			Name:    "Vellore Institute of Technology",
			Country: "India",
			// Faculty domains stay excluded; verification is student-only.
			Domains: []string{"vitstudent.ac.in", "vitap.ac.in", "vitbhopal.ac.in"},
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
			err := database.Database.Db.Where("domain = ?", d).First(&existing).Error
			if err == nil {
				// Re-link domains that exist but point at a stale institute row.
				if existing.InstituteID != inst.ID {
					if upErr := database.Database.Db.
						Model(&existing).
						Update("institute_id", inst.ID).Error; upErr != nil {
						log.Printf("institutes seed: relink %q -> %s failed: %v", d, s.Name, upErr)
					}
				}
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

// ListInstitutes returns the public institute catalog used by profile and ride cards.
func ListInstitutes(c *fiber.Ctx) error {
	var list []models.Institute
	if err := database.Database.Db.Order("name asc").Find(&list).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to fetch institutes",
		})
	}
	return c.JSON(fiber.Map{"institutes": list})
}

// SearchInstitutes powers the verification-flow university picker. It returns
// a compact typeahead result set with domains included for email validation.
func SearchInstitutes(c *fiber.Ctx) error {
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if q == "" {
		return c.JSON(fiber.Map{"institutes": []any{}})
	}

	// Match by name, acronym, or email domain while keeping each institute
	// de-duplicated through EXISTS joins.
	pattern := "%" + q + "%"
	prefixPattern := q + "%"
	// Strip punctuation before treating short queries as acronyms.
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

	// Relevance favors acronym/domain prefix matches, then name matches.
	// Shorter names win ties, and the limit keeps typeahead results compact.
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
				-- Prefer institutes whose student domain starts with the query.
				CASE
					WHEN EXISTS (
					  SELECT 1 FROM institute_domains d
					   WHERE d.institute_id = i.id
					     AND LOWER(d.domain) LIKE ?
					) THEN 0
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
				-- Same domain-prefix tiebreaker as the acronym branch.
				CASE
					WHEN EXISTS (
					  SELECT 1 FROM institute_domains d
					   WHERE d.institute_id = i.id
					     AND LOWER(d.domain) LIKE ?
					) THEN 0
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

	// One follow-up query for all domains across matched institutes,
	// then bucket them by institute id. Avoids N+1.
	ids := make([]any, len(institutes))
	for i, inst := range institutes {
		ids[i] = inst.ID
	}
	var domains []models.InstituteDomain
	if err := database.Database.Db.
		Where("institute_id IN ?", ids).
		// Put student-facing domains first because the picker uses index 0.
		Order(`
			CASE
				WHEN LOWER(domain) LIKE '%student%' THEN 0
				WHEN LOWER(domain) LIKE '%alum%'    THEN 2
				ELSE 1
			END ASC,
			domain ASC
		`).
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
