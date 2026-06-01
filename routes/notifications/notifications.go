package notifications

import (
	"log"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type SendNotificationRequest struct {
	TargetToken  string            `json:"targetToken"`
	Notification NotificationData  `json:"notification"`
	Data         map[string]string `json:"data"`
	Platform     string            `json:"platform"`
}

type NotificationData struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type SendToUserRequest struct {
	UserID string            `json:"userId" validate:"required"`
	Title  string            `json:"title" validate:"required"`
	Body   string            `json:"body" validate:"required"`
	Data   map[string]string `json:"data"`
}

// SendNotification is a diagnostic endpoint for sending a push-shaped payload.
func SendNotification(c *fiber.Ctx) error {
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	var req SendNotificationRequest
	if err := c.BodyParser(&req); err != nil {
		log.Printf("Error parsing notification request: %v", err)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	fcmService := services.GetFCMService()
	if fcmService == nil {
		return c.Status(500).JSON(fiber.Map{
			"error": "FCM service not initialized",
		})
	}

	// The FCM service currently resolves tokens by user ID, so diagnostics use
	// a temporary ID and still exercise the same send path.
	tempUserID := uuid.New()
	if err := fcmService.SendNotification(tempUserID, req.Notification.Title, req.Notification.Body, req.Data); err != nil {
		log.Printf("Error sending notification: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Failed to send notification",
		})
	}

	return c.Status(200).JSON(fiber.Map{
		"message": "Notification sent successfully",
	})
}

// SendNotificationToUser sends a push notification to a specific user ID.
func SendNotificationToUser(c *fiber.Ctx) error {
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	var req SendToUserRequest
	if err := c.BodyParser(&req); err != nil {
		log.Printf("Error parsing notification request: %v", err)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	targetUserID, err := uuid.Parse(req.UserID)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid user ID",
		})
	}

	fcmService := services.GetFCMService()
	if fcmService == nil {
		return c.Status(500).JSON(fiber.Map{
			"error": "FCM service not initialized",
		})
	}

	if err := fcmService.SendNotification(targetUserID, req.Title, req.Body, req.Data); err != nil {
		log.Printf("Error sending notification to user %s: %v", targetUserID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Failed to send notification",
		})
	}

	return c.Status(200).JSON(fiber.Map{
		"message": "Notification sent successfully",
	})
}
