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

// GetDefaultAddress returns the default address for the authenticated user
func GetDefaultAddress(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
	}
	return c.Status(200).JSON(fiber.Map{"address": user.DefaultAddress})
}

// SetDefaultAddress sets (or clears) the default address for the
// authenticated user. Empty payload — either via DELETE with no body,
// or a POST/PUT/PATCH carrying `{"address": ""}` — clears the field.
// The old code rejected empty as a 400, which made the "remove
// address" flow return an error even though clearing is a legitimate
// state.
func SetDefaultAddress(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
	}

	type AddressPayload struct {
		Address string `json:"address"`
	}
	var payload AddressPayload
	// DELETE requests typically have no body. Parsing then becomes a
	// no-op — `payload.Address` stays "" and the update below clears
	// the field. For POST/PUT/PATCH a malformed body is still a 400.
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
