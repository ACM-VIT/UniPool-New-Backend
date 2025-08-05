package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/services"

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

	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"error": "Invalid user data",
		})
	}

	PassengerID := user.ID

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
	booking.PassengerID = PassengerID

	// Insert the booking record using the models.Booking struct
	if err := database.Database.Db.Create(&booking).Error; err != nil {
		log.Println("Error creating booking:", err)
		return c.Status(500).SendString("Database error")
	}

	// Get ride details for notification
	var ride models.Ride
	if err := database.Database.Db.First(&ride, booking.RideID).Error; err == nil {
		// Send FCM notification to the ride owner
		fcmService := services.GetFCMService()
		if fcmService != nil {
			rideRoute := ride.StartLocation + " to " + ride.EndLocation
			go func() {
				if err := fcmService.SendBookingRequestNotification(ride.HostUserID, user.Name, rideRoute, booking.ID); err != nil {
					log.Printf("Error sending booking request notification: %v", err)
				}
			}()
		}
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
