package bookings

import (
	"errors"
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DeleteBooking deletes an existing booking
func DeleteBooking(c *fiber.Ctx) error {
	idParam := c.Params("id")

	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"success": false,
			"error": "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"success": false,
			"error": "Invalid user data",
		})
	}

	bookingID, err := uuid.Parse(idParam)
	if err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid booking ID format"}
	}

	var booking models.Booking
	err = database.Database.Db.First(&booking, "id = ?", bookingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).JSON(fiber.Map{
				"success": false,
				"error": "Booking not found",
				"booking_id": bookingID.String(),
			})
		}
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
	}

	var ride models.Ride
	if err := database.Database.Db.First(&ride, booking.RideID).Error; err != nil {
		log.Printf("Error finding ride with ID %v: %v\n", booking.RideID, err)
		return c.Status(404).JSON(fiber.Map{
			"success": false,
			"error": "Ride not found",
			"booking_id": bookingID.String(),
		})
	}

	if ride.HostUserID != user.ID && booking.PassengerID != user.ID {
		log.Printf("User %v is not authorized to delete booking %v (host: %v, passenger: %v)\n", user.ID, booking.ID, ride.HostUserID, booking.PassengerID)
		return c.Status(403).JSON(fiber.Map{
			"success": false,
			"error": "Only the ride host or the passenger can delete this booking",
			"booking_id": bookingID.String(),
		})
	}

	// Perform the deletion
	if err := database.Database.Db.Delete(&booking).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}

	log.Printf("Booking with id %v deleted\n", booking.ID)
	return c.Status(200).JSON(fiber.Map{
		"success": true,
		"message": "Booking deleted successfully",
		"booking_id": booking.ID.String(),
	})
}
