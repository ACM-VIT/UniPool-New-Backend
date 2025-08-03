package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
)

func RejectRoute(c *fiber.Ctx) error {
	bookingID := c.Params("bookingID")

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

	// Start a database transaction
	tx := database.Database.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Retrieve the booking
	var booking models.Booking
	if err := tx.First(&booking, "id = ?", bookingID).Error; err != nil {
		log.Printf("Error finding booking with ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(404).JSON(fiber.Map{
			"success": false,
			"error": "Booking not found",
			"booking_id": bookingID,
		})
	}

	var ride models.Ride
	if err := tx.First(&ride, booking.RideID).Error; err != nil {
		log.Printf("Error finding ride with ID %v: %v\n", booking.RideID, err)
		tx.Rollback()
		return c.Status(404).JSON(fiber.Map{
			"success": false,
			"error": "Ride not found",
			"booking_id": bookingID,
		})
	}

	if ride.HostUserID != user.ID {
		log.Printf("User %v is not authorized to reject bookings for ride %v (host: %v)\n", user.ID, ride.ID, ride.HostUserID)
		tx.Rollback()
		return c.Status(403).JSON(fiber.Map{
			"success": false,
			"error": "Only the ride host can reject booking requests",
			"booking_id": bookingID,
		})
	}

	// Update the booking status to "rejected"
	if err := tx.Model(&booking).Update("request_status", "rejected").Error; err != nil {
		log.Printf("Error updating booking status for ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(500).JSON(fiber.Map{
			"success": false,
			"error": "Error updating booking status",
			"booking_id": bookingID,
		})
	}


	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		log.Printf("Error committing transaction: %v\n", err)
		return c.Status(500).JSON(fiber.Map{
			"success": false,
			"error": "Error committing transaction",
			"booking_id": bookingID,
		})
	}

	// Send FCM notification to the passenger
	fcmService := services.GetFCMService()
	if fcmService != nil {
		rideRoute := ride.StartLocation + " to " + ride.EndLocation
		go func() {
			if err := fcmService.SendBookingRejectedNotification(booking.PassengerID, rideRoute, booking.ID); err != nil {
				log.Printf("Error sending booking rejected notification: %v", err)
			}
		}()
	}

	log.Printf("Booking with ID %v rejected successfully\n", bookingID)
	return c.Status(200).JSON(fiber.Map{
		"success": true,
		"message": "Booking rejected successfully",
		"booking_id": bookingID,
		"booking": booking,
	})
}
