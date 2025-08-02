package rides

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func GetRideSettings(c *fiber.Ctx) error {
	rideID := c.Params("ride_id")
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid ride ID",
		})
	}

	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	var ride models.Ride
	if err := database.Database.Db.First(&ride, rideUUID).Error; err != nil {
		log.Printf("Error finding ride %s: %v", rideID, err)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	isHost := ride.HostUserID == user.ID
	isPassenger := false
	
	if !isHost {
		var booking models.Booking
		err := database.Database.Db.Where("ride_id = ? AND passenger_id = ? AND request_status = ?", 
			rideUUID, user.ID, "accepted").First(&booking).Error
		isPassenger = (err == nil)
	}

	if !isHost && !isPassenger {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "You are not authorized to view settings for this ride",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"settings": ride.Settings,
	})
}

func UpdateRideSettings(c *fiber.Ctx) error {
	rideID := c.Params("ride_id")
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid ride ID",
		})
	}

	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	var ride models.Ride
	if err := database.Database.Db.First(&ride, rideUUID).Error; err != nil {
		log.Printf("Error finding ride %s: %v", rideID, err)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	isHost := ride.HostUserID == user.ID
	isPassenger := false
	
	if !isHost {
		var booking models.Booking
		err := database.Database.Db.Where("ride_id = ? AND passenger_id = ? AND request_status = ?", 
			rideUUID, user.ID, "accepted").First(&booking).Error
		isPassenger = (err == nil)
	}

	if !isHost && !isPassenger {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "You are not authorized to update settings for this ride",
		})
	}

	var requestBody struct {
		ChatName           *string `json:"chat_name,omitempty"`
		NotificationsMuted *bool   `json:"notifications_muted,omitempty"`
	}

	if err := c.BodyParser(&requestBody); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	updatedSettings := ride.Settings

	if requestBody.ChatName != nil {
		updatedSettings.ChatName = *requestBody.ChatName
	}

	if requestBody.NotificationsMuted != nil {
		updatedSettings.NotificationsMuted = *requestBody.NotificationsMuted
	}

	if err := database.Database.Db.Model(&ride).Update("settings", updatedSettings).Error; err != nil {
		log.Printf("Error updating ride settings for %s: %v", rideID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to update ride settings",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message": "Ride settings updated successfully",
		"settings": updatedSettings,
	})
}
