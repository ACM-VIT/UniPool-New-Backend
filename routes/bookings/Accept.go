package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
)

func AcceptRoute(c *fiber.Ctx) error {
	bookingID := c.Params("bookingID")

	// Start a database transaction
	tx := database.Database.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Retrieve the booking
	// Ensure the bookingID is being treated as UUID in the query
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

	// Update the booking status to "accepted"
	if err := tx.Model(&booking).Update("request_status", "accepted").Error; err != nil {
		log.Printf("Error updating booking status for ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(500).JSON(fiber.Map{
			"success": false,
			"error": "Error updating booking status",
			"booking_id": bookingID,
		})
	}

	// Retrieve the associated ride
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

	// Check if there are available seats for the ride
	if ride.BookedSeats >= ride.TotalSeats {
		log.Printf("No available seats for ride with ID %v\n", booking.RideID)
		tx.Rollback()
		return c.Status(400).JSON(fiber.Map{
			"success": false,
			"error": "No available seats for this ride",
			"booking_id": bookingID,
		})
	}

	// Increment the booked seats count
	if err := tx.Model(&ride).Update("booked_seats", ride.BookedSeats+1).Error; err != nil {
		log.Printf("Error updating booked seats for ride with ID %v: %v\n", ride.ID, err)
		tx.Rollback()
		return c.Status(500).JSON(fiber.Map{
			"success": false,
			"error": "Error updating booked seats for ride",
			"booking_id": bookingID,
		})
	}

	// Commit the transaction after all successful updates
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
			if err := fcmService.SendBookingAcceptedNotification(booking.PassengerID, rideRoute, booking.ID); err != nil {
				log.Printf("Error sending booking accepted notification: %v", err)
			}
		}()
	}

	log.Printf("Booking with ID %v accepted successfully\n", bookingID)
	return c.Status(200).JSON(fiber.Map{
		"success": true,
		"message": "Booking accepted successfully",
		"booking_id": bookingID,
		"booking": booking,
	})
}
