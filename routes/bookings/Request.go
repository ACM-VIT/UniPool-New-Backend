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

	// Women-only ride gate: if the target ride is flagged as
	// same-gender (currently always female-only — we only let female
	// hosts set the flag at create-ride time), reject requests from
	// non-female users with a clear 403 so the client can surface a
	// friendly "this ride is women-only" sheet instead of a generic
	// failure. Done BEFORE the insert so we never write a doomed
	// booking row.
	var targetRide models.Ride
	if err := database.Database.Db.Select("id, is_same_gender").First(&targetRide, booking.RideID).Error; err != nil {
		log.Printf("ride lookup for women-only check failed: %v", err)
		return c.Status(404).SendString("Ride not found")
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
