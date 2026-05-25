package users

import (
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// Function to get details of any user by ID (public endpoint for getting passenger info)
func GetUserByID(c *fiber.Ctx) error {
	// Get user ID from URL params
	userID := c.Params("id")
	if userID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   true,
			"message": "User ID is required",
		})
	}

	// Find user in database
	var user models.User
	result := database.Database.Db.
		Select("id, name, email, profile_picture_url, gender, created_at").
		Where("id = ?", userID).
		Take(&user)
	if result.Error != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":   true,
			"message": "User not found",
		})
	}

	// Return public user information (no sensitive data)
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"id":                  user.ID,
		"name":                user.Name,
		"email":               user.Email,
		"profile_picture_url": user.ProfilePictureURL,
		"gender":              user.Gender,
		"created_at":          user.CreatedAt,
	})
}
