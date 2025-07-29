
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

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"user": fiber.Map{
				"id": user.ID,
				"name": user.Name,
				"email": user.Email,
				"profile_picture_url": user.ProfilePictureURL,
				"contact_number": user.ContactNumber,
				"gender": user.Gender,
				"yob": user.YOB,
				"default_address": user.DefaultAddress,
				"created_at": user.CreatedAt,
				"updated_at": user.UpdatedAt,
				// ...other fields as needed...
				"total_hosted_rides": totalHostedRides,
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
