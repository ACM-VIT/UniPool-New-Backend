package users

import (
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// GetUserByID returns the public profile fields needed for passenger info.
func GetUserByID(c *fiber.Ctx) error {
	userID := c.Params("id")
	if userID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   true,
			"message": "User ID is required",
		})
	}

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

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"id":                  user.ID,
		"name":                user.Name,
		"email":               user.Email,
		"profile_picture_url": user.ProfilePictureURL,
		"gender":              user.Gender,
		"created_at":          user.CreatedAt,
	})
}
