package rides

import (
	"context"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type rideSettingsState struct {
	Settings  models.RideSettings `gorm:"column:settings"`
	CanAccess bool                `gorm:"column:can_access"`
	Muted     bool                `gorm:"column:muted"`
}

func loadRideSettingsState(ctx context.Context, rideID uuid.UUID, userID uuid.UUID) (rideSettingsState, bool, error) {
	var state rideSettingsState
	tx := database.Database.Db.WithContext(ctx).Raw(`
		SELECT
			r.settings AS settings,
			(r.host_user_id = ? OR b.id IS NOT NULL) AS can_access,
			NOT COALESCE(rp.enabled, gp.enabled, TRUE) AS muted
		  FROM rides r
		  LEFT JOIN bookings b
		    ON b.ride_id = r.id
		   AND b.passenger_id = ?
		   AND b.request_status = 'accepted'
		  LEFT JOIN notification_preferences rp
		    ON rp.user_id = ?
		   AND rp.category = ?
		   AND rp.ride_id = r.id
		  LEFT JOIN notification_preferences gp
		    ON gp.user_id = ?
		   AND gp.category = ?
		   AND gp.ride_id IS NULL
		 WHERE r.id = ?
		 LIMIT 1
	`, userID, userID, userID, helpers.NotifChatMessages, userID, helpers.NotifChatMessages, rideID).Scan(&state)
	return state, tx.RowsAffected > 0, tx.Error
}

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	state, found, err := loadRideSettingsState(ctx, rideUUID, user.ID)
	if err != nil {
		log.Printf("Error loading ride settings for %s: %v", rideID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to load ride settings",
		})
	}
	if !found {
		log.Printf("Ride settings lookup found no ride %s", rideID)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	if !state.CanAccess {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "You are not authorized to view settings for this ride",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"settings": state.Settings,
		"muted":    state.Muted,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	state, found, err := loadRideSettingsState(ctx, rideUUID, user.ID)
	if err != nil {
		log.Printf("Error loading ride settings for %s: %v", rideID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to load ride settings",
		})
	}
	if !found {
		log.Printf("Ride settings lookup found no ride %s", rideID)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	if !state.CanAccess {
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

	updatedSettings := state.Settings

	if requestBody.ChatName != nil {
		updatedSettings.ChatName = *requestBody.ChatName
	}

	if requestBody.NotificationsMuted != nil {
		updatedSettings.NotificationsMuted = *requestBody.NotificationsMuted
	}

	if err := database.Database.Db.WithContext(ctx).
		Model(&models.Ride{}).
		Where("id = ?", rideUUID).
		Update("settings", updatedSettings).Error; err != nil {
		log.Printf("Error updating ride settings for %s: %v", rideID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to update ride settings",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":  "Ride settings updated successfully",
		"settings": updatedSettings,
		"muted":    state.Muted,
	})
}
