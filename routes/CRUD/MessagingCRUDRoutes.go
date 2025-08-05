package CRUD

import (
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MessageResponse struct {
	ID        uuid.UUID `json:"id"`
	RideID    uuid.UUID `json:"ride_id"`
	SenderID  uuid.UUID `json:"sender_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// isUserPartOfRide checks if a user is either the host or a passenger of a ride
func isUserPartOfRide(db *gorm.DB, rideID uuid.UUID, userID uuid.UUID) (bool, error) {
	var ride models.Ride

	// First check if user is the host
	result := db.First(&ride, rideID)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, result.Error
	}

	if ride.HostUserID == userID {
		return true, nil
	}

	// If not host, check if user is a passenger
	// This assumes passengers are tracked in the Ride table
	var passengerRide models.Ride
	result = db.Where("id = ? AND host_user_id <> ? AND booked_seats > 0", rideID, userID).
		First(&passengerRide)

	return result.Error != gorm.ErrRecordNotFound, nil
}

func CreateMessage(c *fiber.Ctx) error {
	var message models.Message

	// Get user from context
	user := c.Locals("user").(models.User)

	// Parse request body
	if err := c.BodyParser(&message); err != nil {
		log.Printf("Error parsing JSON: %v\n", err)
		return c.Status(400).SendString("Error parsing JSON")
	}

	// Validate RideID
	if message.RideID == nil || *message.RideID == uuid.Nil {
		return c.Status(400).SendString("Invalid RideID")
	}

	// Check if user is part of the ride
	isPartOfRide, err := isUserPartOfRide(database.Database.Db, *message.RideID, user.ID)
	if err != nil {
		log.Printf("Error checking ride participation: %v\n", err)
		return c.Status(500).SendString("Error checking ride participation")
	}

	if !isPartOfRide {
		return c.Status(403).SendString("You must be a participant of this ride to send messages")
	}

	// Set sender ID
	message.SenderID = user.ID

	// Create message
	if err := database.Database.Db.Create(&message).Error; err != nil {
		log.Printf("Error creating message: %v\n", err)
		return c.Status(500).SendString("Error creating message")
	}

	// Load sender information for response
	if err := database.Database.Db.Preload("Sender").First(&message, message.ID).Error; err != nil {
		log.Printf("Error loading message relationships: %v\n", err)
		return c.Status(500).SendString("Error loading message data")
	}

	return c.Status(201).JSON(message)
}

func GetRideMessages(c *fiber.Ctx) error {
	rideIDParam := c.Params("id")
	user := c.Locals("user").(models.User)

	rideID, err := uuid.Parse(rideIDParam)
	if err != nil {
		log.Printf("Invalid RideID UUID: %v\n", err)
		return c.Status(400).SendString("Invalid RideID format")
	}

	// Verify the ride exists
	var ride models.Ride
	if err := database.Database.Db.First(&ride, rideID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).SendString("Ride not found")
		}
		return c.Status(500).SendString("Error finding ride")
	}

	// Check if user is host or passenger
	isHost := ride.HostUserID == user.ID
	var passengerRide models.Ride
	result := database.Database.Db.Where("id = ? AND host_user_id <> ? AND booked_seats > 0", rideID, user.ID).
		First(&passengerRide)
	isPassenger := result.Error == nil

	if !isHost && !isPassenger {
		return c.Status(403).SendString("You must be a participant of this ride to view messages")
	}

	// Get all messages with sender information
	var messages []models.Message
	if err := database.Database.Db.Where("ride_id = ?", rideID).
		Preload("Sender").
		Order("created_at asc").
		Find(&messages).Error; err != nil {
		log.Printf("Error fetching messages: %v\n", err)
		return c.Status(500).SendString("Error fetching messages")
	}

	return c.Status(200).JSON(messages)
}

// GetSpecificMessage gets a specific message by ID
func GetSpecificMessage(c *fiber.Ctx) error {
	messageIDParam := c.Params("id")
	user := c.Locals("user").(models.User)

	messageID, err := uuid.Parse(messageIDParam)
	if err != nil {
		log.Printf("Invalid MessageID UUID: %v\n", err)
		return c.Status(400).SendString("Invalid MessageID format")
	}

	// Get message with sender information
	var message models.Message
	if err := database.Database.Db.Preload("Sender").First(&message, messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).SendString("Message not found")
		}
		return c.Status(500).SendString("Error finding message")
	}

	// Get the ride to check if user is participant
	var ride models.Ride
	if err := database.Database.Db.First(&ride, message.RideID).Error; err != nil {
		return c.Status(500).SendString("Error finding associated ride")
	}

	// Check if user is host or passenger
	isHost := ride.HostUserID == user.ID
	var passengerRide models.Ride
	result := database.Database.Db.Where("id = ? AND host_user_id <> ? AND booked_seats > 0", message.RideID, user.ID).
		First(&passengerRide)
	isPassenger := result.Error == nil

	if !isHost && !isPassenger {
		return c.Status(403).SendString("You must be a participant of this ride to view this message")
	}

	return c.Status(200).JSON(message)
}

func UpdateMessage(c *fiber.Ctx) error {
	messageIDParam := c.Params("id")
	user := c.Locals("user").(models.User)

	messageID, err := uuid.Parse(messageIDParam)
	if err != nil {
		return c.Status(400).SendString("Invalid MessageID format")
	}

	var message models.Message
	if err := database.Database.Db.First(&message, messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).SendString("Message not found")
		}
		return c.Status(500).SendString("Error finding message")
	}

	// Verify ownership
	if message.SenderID != user.ID {
		return c.Status(403).SendString("You can only update your own messages")
	}

	// Parse update body
	var updateData struct {
		Content string `json:"content"`
	}
	if err := c.BodyParser(&updateData); err != nil {
		return c.Status(400).SendString("Invalid request body")
	}

	message.Content = updateData.Content

	if err := database.Database.Db.Save(&message).Error; err != nil {
		log.Printf("Error updating message: %v\n", err)
		return c.Status(500).SendString("Error updating message")
	}

	// Reload message with sender information
	if err := database.Database.Db.Preload("Sender").First(&message, messageID).Error; err != nil {
		return c.Status(500).SendString("Error loading updated message")
	}

	return c.Status(200).JSON(message)
}

func DeleteMessage(c *fiber.Ctx) error {
	messageIDParam := c.Params("id")
	user := c.Locals("user").(models.User)

	messageID, err := uuid.Parse(messageIDParam)
	if err != nil {
		return c.Status(400).SendString("Invalid MessageID format")
	}

	var message models.Message
	if err := database.Database.Db.First(&message, messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).SendString("Message not found")
		}
		return c.Status(500).SendString("Error finding message")
	}

	// Verify ownership
	if message.SenderID != user.ID {
		return c.Status(403).SendString("You can only delete your own messages")
	}

	if err := database.Database.Db.Delete(&message).Error; err != nil {
		log.Printf("Error deleting message: %v\n", err)
		return c.Status(500).SendString("Error deleting message")
	}

	return c.Status(200).SendString("Message deleted successfully")
}
