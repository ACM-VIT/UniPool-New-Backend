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
		tx := database.Database.Db.Begin()

		// Delete user metadata
		if err := database.Database.Db.Where("user_id = ?", user.ID).Delete(&models.UserMetadata{}).Error; err != nil {
			log.Println("Error deleting user metadata:", err)
			tx.Rollback()
			return &fiber.Error{Code: 500, Message: "Error deleting user metadata"}
		}

		// Delete user
		if err := database.Database.Db.Delete(&user).Error; err != nil {
			log.Println("Error deleting user:", err)
			tx.Rollback()
			return &fiber.Error{Code: 500, Message: "Error deleting user"}
		}

		tx.Commit()

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"message": "User deleted successfully",
		})
	}



	// If user is not found in locals, return an error
	return &fiber.Error{Code: 400, Message: "User data not found in locals"}
}
