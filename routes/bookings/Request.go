package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type BookingResponse struct {
	ID            uuid.UUID `json:"id"`
	RideID        uuid.UUID `json:"ride_id"`
	RequestStatus string    `json:"request_status"`
}

// Request function to create a booking
func Request(c *fiber.Ctx) error {
	var booking models.Booking

	// Retrieve PassengerID (user ID) from the local context
	PassengerID := c.Locals("user").(models.User).ID

	// Parse the request body to fill booking fields
	if err := c.BodyParser(&booking); err != nil {
		log.Println("Error parsing booking request:", err)
		return c.Status(400).SendString("Invalid request body")
	}

	// Check if a booking with the same RideID and PassengerID already exists
	existingBooking := models.Booking{}
	if database.Database.Db.Where("ride_id = ? AND passenger_id = ?", booking.RideID, PassengerID).First(&existingBooking).Error == nil {
		log.Println("Similar booking already exists")
		return c.Status(400).SendString("Similar booking already exists")
	}

	// Associate the PassengerID with the booking
	// Add PassengerID indirectly by creating a mapping or storing it in the database
	bookingMap := map[string]interface{}{
		"ride_id":        booking.RideID,
		"request_status": booking.RequestStatus,
		"passenger_id":   PassengerID,
	}

	// Insert the booking record
	if err := database.Database.Db.Model(&models.Booking{}).Create(bookingMap).Error; err != nil {
		log.Println("Error creating booking:", err)
		return c.Status(500).SendString("Database error")
	}

	// Create the response
	bookingResponse := BookingResponse{
		ID:            booking.ID,
		RideID:        booking.RideID,
		RequestStatus: booking.RequestStatus,
	}

	log.Printf("Booking with ride_id %v and passenger_id %v created\n", booking.RideID, PassengerID)
	return c.Status(201).JSON(bookingResponse)
}
