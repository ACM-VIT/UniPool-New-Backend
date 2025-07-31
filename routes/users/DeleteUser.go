package users

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// Function to delete user.
func DeleteUser(c *fiber.Ctx) error {

	// Extract user info from locals
	if user, ok := c.Locals("user").(models.User); ok {

		// Delete user
		if err := database.Database.Db.Delete(&user).Error; err != nil {
			log.Println("Error deleting user:", err)
			return &fiber.Error{Code: 500, Message: "Error deleting user"}
		}

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"message": "User deleted successfully",
		})
	}

	// If user is not found in locals, return an error
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
		"error":   true,
		"message": "User data not found in locals",
	})
}
