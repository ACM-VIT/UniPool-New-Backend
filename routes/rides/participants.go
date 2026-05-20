package rides

import (
	"errors"
	"log"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RemoveRideParticipant removes a passenger booking from a ride.
// Passengers can remove themselves; hosts can remove any passenger
// from rides they own. The host is not removable through this route.
func RemoveRideParticipant(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	rideID, err := uuid.Parse(c.Params("ride_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}
	participantID, err := uuid.Parse(c.Params("user_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid user id"})
	}

	tx := database.Database.Db.Begin()
	if tx.Error != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "transaction failed"})
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	var ride models.Ride
	if err := tx.Where("id = ?", rideID).First(&ride).Error; err != nil {
		tx.Rollback()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "ride not found"})
		}
		log.Printf("RemoveRideParticipant: ride lookup failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}

	if participantID == ride.HostUserID {
		tx.Rollback()
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "the host cannot be removed as a passenger"})
	}
	if user.ID != participantID && user.ID != ride.HostUserID {
		tx.Rollback()
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not allowed to remove this participant"})
	}

	var booking models.Booking
	if err := tx.
		Where("ride_id = ? AND passenger_id = ?", rideID, participantID).
		First(&booking).Error; err != nil {
		tx.Rollback()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "participant booking not found"})
		}
		log.Printf("RemoveRideParticipant: booking lookup failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}

	if err := tx.Delete(&booking).Error; err != nil {
		tx.Rollback()
		log.Printf("RemoveRideParticipant: delete failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "delete failed"})
	}

	if booking.RequestStatus == "accepted" {
		if err := tx.Model(&models.Ride{}).
			Where("id = ? AND booked_seats > 0", rideID).
			Update("booked_seats", gorm.Expr("booked_seats - 1")).Error; err != nil {
			tx.Rollback()
			log.Printf("RemoveRideParticipant: seat decrement failed: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "seat update failed"})
		}
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("RemoveRideParticipant: commit failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "commit failed"})
	}

	return c.JSON(fiber.Map{"success": true})
}
