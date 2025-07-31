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
		return c.Status(404).SendString("Booking not found")
	}

	// Update the booking status to "rejected"
	if err := tx.Model(&booking).Update("request_status", "rejected").Error; err != nil {
		log.Printf("Error updating booking status for ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(500).SendString("Error updating booking status")
	}

	// Retrieve the associated ride for notification
	var ride models.Ride
	if err := tx.First(&ride, booking.RideID).Error; err != nil {
		log.Printf("Error finding ride with ID %v: %v\n", booking.RideID, err)
		tx.Rollback()
		return c.Status(404).SendString("Ride not found")
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		log.Printf("Error committing transaction: %v\n", err)
		return c.Status(500).SendString("Error committing transaction")
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
	return c.Status(200).SendString("Booking rejected")
}
