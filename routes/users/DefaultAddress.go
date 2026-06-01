package users

import (
	"context"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/middleware"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// GetDefaultAddress returns the authenticated user's default address.
func GetDefaultAddress(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
	}
	return c.Status(200).JSON(fiber.Map{"address": user.DefaultAddress})
}

// SetDefaultAddress sets or clears the authenticated user's default address.
// Empty DELETE or empty address payload clears the field.
func SetDefaultAddress(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
	}

	type AddressPayload struct {
		Address string `json:"address"`
	}
	var payload AddressPayload
	// DELETE typically has no body; an empty payload means clear the field.
	if err := c.BodyParser(&payload); err != nil && c.Method() != "DELETE" {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid JSON body"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Database.Db.WithContext(ctx).Model(&models.User{}).Where("id = ?", user.ID).Update("default_address", payload.Address).Error; err != nil {
		log.Printf("Error updating address: %v\n", err)
		return c.Status(500).JSON(fiber.Map{"error": "Failed to update address"})
	}
	middleware.InvalidateAuthUserCacheByEmail(user.Email)

	return c.Status(200).JSON(fiber.Map{"status": "OK"})
}
