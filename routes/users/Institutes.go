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
