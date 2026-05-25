package users

import (
	"context"
	"log"
	"strings"
	"time"

	"unipool-backend/database"
	"unipool-backend/middleware"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// UpdateProfile is the narrow self-edit surface — only fields the
// user can change about themselves. Name/email come from Firebase
// identity, verification status from email-domain matching, etc.;
// those are NOT writable here.
//
// Body (all optional, only set fields are updated):
//
//	{
//	  "upi_vpa":        "yash@upi",     // optional, max 120 chars
//	  "contact_number": "9999999999",  // optional, numeric
//	}
//
// Empty string for `upi_vpa` clears the field — used when a host
// decides they don't want UPI deeplinks fired against their VPA
// any more.
func UpdateProfile(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "unauthorized",
		})
	}

	type payload struct {
		UPIVPA        *string `json:"upi_vpa,omitempty"`
		ContactNumber *string `json:"contact_number,omitempty"`
	}
	var body payload
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid body",
		})
	}

	// Build the update map dynamically — only touch the columns the
	// caller actually passed. A `nil` pointer means "leave it alone";
	// a non-nil empty string means "clear it".
	updates := map[string]any{}

	if body.UPIVPA != nil {
		vpa := strings.TrimSpace(*body.UPIVPA)
		if len(vpa) > 120 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "upi_vpa must be at most 120 characters",
			})
		}
		// Loose sanity check — a UPI VPA looks like `name@bank`. We
		// don't validate the bank handle (too churny to maintain);
		// just reject obvious junk so users don't save garbage.
		if vpa != "" && !strings.Contains(vpa, "@") {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "upi_vpa must be in the form name@handle",
			})
		}
		updates["upi_vpa"] = vpa
	}
	if body.ContactNumber != nil {
		num := strings.TrimSpace(*body.ContactNumber)
		if num != "" {
			// Strip common separators before the numeric check so
			// "+91 99999 99999" reads as valid.
			normalized := strings.NewReplacer(" ", "", "-", "", "+", "").Replace(num)
			for _, r := range normalized {
				if r < '0' || r > '9' {
					return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
						"error": "contact_number must be numeric",
					})
				}
			}
		}
		updates["contact_number"] = num
	}

	if len(updates) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "no editable fields in body",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Database.Db.WithContext(ctx).
		Model(&models.User{}).
		Where("id = ?", user.ID).
		Updates(updates).Error; err != nil {
		log.Printf("UpdateProfile: failed for user %s: %v", user.ID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to update profile",
		})
	}
	middleware.InvalidateAuthUserCacheByEmail(user.Email)

	// Return the post-update user row so the client can update its
	// cached state without a round-trip. Preloads Institute so the
	// personal-info screen keeps showing the verified-school name
	// after a UPI / contact edit, instead of losing it on save.
	var fresh models.User
	if err := database.Database.Db.
		Preload("Institute").
		Where("id = ?", user.ID).
		First(&fresh).Error; err == nil {
		return c.JSON(fiber.Map{"user": fresh})
	}
	return c.JSON(fiber.Map{"status": "OK"})
}
