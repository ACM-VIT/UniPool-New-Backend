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

	// Fetch all users from the database
	if err := database.Database.Db.Find(&users).Error; err != nil {
		log.Println("Error fetching users:", err)
		return &fiber.Error{Code: 500, Message: "Error fetching users"}
	}
	
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"users": users,
	})
}