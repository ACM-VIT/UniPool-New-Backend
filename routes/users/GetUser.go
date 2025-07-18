package users

import (
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// Function to get details of user.
func GetUser(c *fiber.Ctx) error {
	// Extract user info from locals
	if user, ok := c.Locals("user").(models.User); ok {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"user": user,
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
