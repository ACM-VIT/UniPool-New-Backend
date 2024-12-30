package routes

import (
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

func CreateMessage(c *fiber.Ctx) error {
	var message models.Message

	if err := c.BodyParser(&message); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Unable to parse message data"})
	}

	// Validate message data using helper
	if err := helpers.ValidateMessages(message); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	// Extract the user ID from locals (set by the authentication middleware)
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "User not authenticated"})
	}

	// Set the sender ID from the authenticated user
	message.SenderID = user.ID

	if err := database.Database.Db.Create(&message).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to create message"})
	}

	return c.Status(201).JSON(fiber.Map{"status": "Message created successfully", "message": message})
}

func GetMessageByID(c *fiber.Ctx) error {
	messageID := c.Params("id")
	var message models.Message

	// Find message in DB
	if err := database.Database.Db.First(&message, "id = ?", messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).JSON(fiber.Map{"error": "Message not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": "Database error"})
	}

	return c.Status(200).JSON(fiber.Map{"message": message})
}

func UpdateMessage(c *fiber.Ctx) error {
	messageID := c.Params("id")
	var message models.Message

	// Find existing message in DB
	if err := database.Database.Db.First(&message, "id = ?", messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).JSON(fiber.Map{"error": "Message not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": "Database error"})
	}

	// Extract the user ID from locals (set by the authentication middleware)
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "User not authenticated"})
	}

	// Check if the message sender matches the authenticated user
	if message.SenderID != user.ID {
		return c.Status(403).JSON(fiber.Map{"error": "You can only update your own messages"})
	}

	// Parse updated data
	if err := c.BodyParser(&message); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Unable to parse message data"})
	}

	// Validate updated message data using helper
	if err := helpers.ValidateMessages(message); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	// Save updated message in DB
	if err := database.Database.Db.Save(&message).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to update message"})
	}

	return c.Status(200).JSON(fiber.Map{"status": "Message updated successfully", "message": message})
}

func DeleteMessage(c *fiber.Ctx) error {
	messageID := c.Params("id")
	var message models.Message

	// Find the message in the DB
	if err := database.Database.Db.First(&message, "id = ?", messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).JSON(fiber.Map{"error": "Message not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": "Database error"})
	}

	// Extract the user ID from locals (set by the authentication middleware)
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "User not authenticated"})
	}

	// Check if the message sender matches the authenticated user
	if message.SenderID != user.ID {
		return c.Status(403).JSON(fiber.Map{"error": "You can only delete your own messages"})
	}

	// Delete the message
	if err := database.Database.Db.Delete(&models.Message{}, "id = ?", messageID).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to delete message"})
	}

	return c.Status(200).JSON(fiber.Map{"message": "Message deleted successfully"})
}
