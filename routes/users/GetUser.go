
package users

import (
	"unipool-backend/models"
	"unipool-backend/database"
	"github.com/gofiber/fiber/v2"
)

// Function to get details of user.

func GetUser(c *fiber.Ctx) error {
	// Extract user info from locals
	if user, ok := c.Locals("user").(models.User); ok {
		var totalHostedRides int64
		database.Database.Db.Model(&models.Ride{}).Where("host_user_id = ?", user.ID).Count(&totalHostedRides)

		// Auth middleware selects a minimal column set for speed,
		// which means `upi_vpa`, `is_email_verified`, `institute_id`,
		// and `institute_email` aren't populated on `user` here.
		// Hydrate them from a single follow-up read so the profile
		// screen can render badges + the UPI row in one round-trip.
		var full models.User
		_ = database.Database.Db.
			Preload("Institute").
			Where("id = ?", user.ID).
			First(&full).Error

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"user": fiber.Map{
				"id":                  user.ID,
				"name":                user.Name,
				"email":               user.Email,
				"profile_picture_url": user.ProfilePictureURL,
				"contact_number":      user.ContactNumber,
				"gender":              user.Gender,
				"yob":                 user.YOB,
				"default_address":     user.DefaultAddress,
				"created_at":          user.CreatedAt,
				"updated_at":          user.UpdatedAt,
				"total_hosted_rides":  totalHostedRides,
				// Verification + payment surface for the personal-info
				// screen. Empty strings/false/nil when unset.
				"upi_vpa":           full.UPIVPA,
				"is_email_verified": full.IsEmailVerified,
				"institute_email":   full.InstituteEmail,
				"institute":         full.Institute,
				"institute_id":      full.InstituteID,
			},
		})
	}

	if newUser, ok := c.Locals("newuser").(map[string]interface{}); ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"newUser": newUser,
			"error":   false,
			"message": "User not found in database, signup required",
		})
	}

	// If user is not found in locals, return an error
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
		"error":   true,
		"message": "User data not found in locals",
	})
}
