package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

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
		return c.Status(404).SendString("Booking not found")
	}

	// Update the booking status to "accepted"
	if err := tx.Model(&booking).Update("request_status", "accepted").Error; err != nil {
		log.Printf("Error updating booking status for ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(500).SendString("Error updating booking status")
	}

	// Retrieve the associated ride
	var ride models.Ride
	if err := tx.First(&ride, booking.RideID).Error; err != nil {
		log.Printf("Error finding ride with ID %v: %v\n", booking.RideID, err)
		tx.Rollback()
		return c.Status(404).SendString("Ride not found")
	}

	// Check if there are available seats for the ride
	if ride.BookedSeats >= ride.TotalSeats {
		log.Printf("No available seats for ride with ID %v\n", booking.RideID)
		tx.Rollback()
		return c.Status(400).SendString("No available seats for this ride")
	}

	// Increment the booked seats count
	if err := tx.Model(&ride).Update("booked_seats", ride.BookedSeats+1).Error; err != nil {
		log.Printf("Error updating booked seats for ride with ID %v: %v\n", ride.ID, err)
		tx.Rollback()
		return c.Status(500).SendString("Error updating booked seats for ride")
	}

	// Commit the transaction after all successful updates
	if err := tx.Commit().Error; err != nil {
		log.Printf("Error committing transaction: %v\n", err)
		return c.Status(500).SendString("Error committing transaction")
	}

	log.Printf("Booking with ID %v accepted successfully\n", bookingID)
	return c.Status(200).SendString("Booking accepted")
}
