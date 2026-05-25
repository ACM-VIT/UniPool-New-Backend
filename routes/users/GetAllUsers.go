package users

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

func GetAllUsers(c *fiber.Ctx) error {
	// Fetch all users from the database
	var users []models.User

	if err := database.Database.Db.
		Select("id, name, email, profile_picture_url, contact_number, gender, yob, default_address, is_email_verified, institute_id, created_at, updated_at").
		Find(&users).Error; err != nil {
		log.Println("Error fetching users:", err)
		return &fiber.Error{Code: 500, Message: "Error fetching users"}
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"users": users,
	})
}
