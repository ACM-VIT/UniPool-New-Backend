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

	var booking models.Booking

	if err := tx.First(&booking, bookingID).Error; err != nil {
		// Rollback the transaction in case of an error
		tx.Rollback()
		log.Printf("Booking does not exist")
		return c.Status(404).SendString("Booking not found")
	}

	if err := tx.Model(&booking).Update("request_status", "accepted").Error; err != nil {
		// Rollback the transaction in case of an error
		tx.Rollback()
		log.Printf("Error updating booking status: %v\n", err)
		return c.Status(500).SendString("Error updating booking status")
	}

	var ride models.Ride
	RideID := booking.RideID
	if err := tx.First(&ride, RideID).Error; err != nil {
		// Rollback the transaction in case of an error
		tx.Rollback()
		log.Printf("Ride not Found")
		return c.Status(404).SendString("Ride not Found")
	}

	if ride.BookedSeats >= ride.TotalSeats {
		// Rollback the transaction in case of an error
		tx.Rollback()
		log.Println("No available seats for this ride")
		return c.Status(400).SendString("No available seats for this ride")
	}

	// Increment the booked seats count
	if err := tx.Model(&ride).Update("booked_seats", ride.BookedSeats+1).Error; err != nil {
		// Rollback the transaction in case of an error
		tx.Rollback()
		log.Printf("Error updating booked seats for ride: %v\n", err)
		return c.Status(500).SendString("Error updating booked seats for ride")
	}

	// Commit the transaction if all operations are successful
	tx.Commit()

	log.Printf("Booking with ID %v accepted\n", bookingID)
	return c.Status(200).SendString("Booking accepted")
}
