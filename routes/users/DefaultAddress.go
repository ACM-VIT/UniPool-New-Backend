package users

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"
	"github.com/gofiber/fiber/v2"
)

// GetDefaultAddress returns the default address for the authenticated user
func GetDefaultAddress(c *fiber.Ctx) error {
	userID := c.Locals("user_id")
	if userID == nil {
		return c.Status(401).SendString("Unauthorized")
	}

	var user models.User
	if err := database.Database.Db.First(&user, "id = ?", userID).Error; err != nil {
		log.Printf("Error fetching user: %v\n", err)
		return c.Status(404).SendString("User not found")
	}

	return c.Status(200).JSON(fiber.Map{"address": user.DefaultAddress})
}

// SetDefaultAddress sets the default address for the authenticated user
func SetDefaultAddress(c *fiber.Ctx) error {
	userID := c.Locals("user_id")
	if userID == nil {
		return c.Status(401).SendString("Unauthorized")
	}

	type AddressPayload struct {
		Address string `json:"address"`
	}
	var payload AddressPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).SendString("Invalid JSON body")
	}

	if payload.Address == "" {
		return c.Status(400).SendString("Address cannot be empty")
	}

	if err := database.Database.Db.Model(&models.User{}).Where("id = ?", userID).Update("default_address", payload.Address).Error; err != nil {
		log.Printf("Error updating address: %v\n", err)
		return c.Status(500).SendString("Failed to update address")
	}

	return c.SendStatus(200)
}
