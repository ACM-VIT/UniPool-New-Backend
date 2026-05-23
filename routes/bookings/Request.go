package bookings

import (
	"log"
	"strings"
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

	// Pre-flight on the target ride. Three things to check before we
	// touch the bookings table:
	//   1. The ride exists.
	//   2. The caller isn't the host. Requesting your own ride is the
	//      bug that produced ride ad1d49b8… in the rating saga — a
	//      user ended up as both host_user_id and the only
	//      passenger_id on the same ride, the rate flow refused to
	//      submit, etc. Defence-in-depth at the booking-creation
	//      step so the bad row can't exist at all.
	//   3. Women-only ride enforcement (existing behaviour). Done
	//      BEFORE the insert so we never write a doomed booking row.
	var targetRide models.Ride
	if err := database.Database.Db.Select("id, host_user_id, is_same_gender").First(&targetRide, booking.RideID).Error; err != nil {
		log.Printf("ride lookup for booking pre-flight failed: %v", err)
		return c.Status(404).SendString("Ride not found")
	}
	if targetRide.HostUserID == PassengerID {
		log.Printf("user %v tried to request their own ride %v", PassengerID, targetRide.ID)
		return c.Status(400).JSON(fiber.Map{
			"error": "You can't request a seat on your own ride",
			"code":  "self_booking",
		})
	}
	if targetRide.IsSameGender == 1 && strings.ToLower(user.Gender) != "female" {
		log.Printf("non-female user %v tried to join women-only ride %v", user.ID, targetRide.ID)
		return c.Status(403).JSON(fiber.Map{
			"error": "This ride is reserved for women passengers",
			"code":  "women_only",
		})
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
				if err := fcmService.SendBookingRequestNotification(ride.HostUserID, user.ID, user.Name, rideRoute, ride.ID, booking.ID); err != nil {
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
