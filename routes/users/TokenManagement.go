package users

import (
	"context"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

type TokenRequest struct {
	Token    string `json:"token" validate:"required"`
	Platform string `json:"platform" validate:"required,oneof=ios android"`
	DeviceID string `json:"deviceId"`
}

// UpdateUserToken stores the active device token used for push delivery.
func UpdateUserToken(c *fiber.Ctx) error {
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"error": "Invalid user data",
		})
	}

	var tokenReq TokenRequest
	if err := c.BodyParser(&tokenReq); err != nil {
		log.Printf("Error parsing token request: %v", err)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	updates := map[string]interface{}{
		"fcm_token": tokenReq.Token,
		"platform":  tokenReq.Platform,
	}

	if tokenReq.DeviceID != "" {
		updates["device_id"] = tokenReq.DeviceID
	}

	if err := database.Database.Db.WithContext(ctx).Model(&user).Updates(updates).Error; err != nil {
		log.Printf("Error updating user token: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Failed to update token",
		})
	}

	log.Printf("Successfully updated FCM token for user %s", user.ID)
	return c.Status(200).JSON(fiber.Map{
		"message": "Token updated successfully",
	})
}

// RemoveUserToken clears push metadata on logout.
func RemoveUserToken(c *fiber.Ctx) error {
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"error": "Invalid user data",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Database.Db.WithContext(ctx).Model(&user).Updates(map[string]interface{}{
		"fcm_token": "",
		"platform":  "",
		"device_id": "",
	}).Error; err != nil {
		log.Printf("Error removing user token: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Failed to remove token",
		})
	}

	log.Printf("Successfully removed FCM token for user %s", user.ID)
	return c.Status(200).JSON(fiber.Map{
		"message": "Token removed successfully",
	})
}
