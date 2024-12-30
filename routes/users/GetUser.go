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

	// If user is not found in locals, return an error
	return &fiber.Error{Code: 400, Message: "User data not found in locals"}
}
