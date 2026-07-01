package bookings

import (
	"log"
	"strings"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
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
	if booking.RequestStatus != "" && booking.RequestStatus != "pending" {
		return c.Status(400).JSON(fiber.Map{
			"error": "New booking requests must start as pending",
			"code":  "invalid_request_status",
		})
	}
	booking.RequestStatus = "pending"

	// Pre-flight on the target ride. Three things to check before we
	// touch the bookings table:
	//   1. The ride exists.
	//   2. The caller isn't the host. Requesting your own ride is the
	//      bug that produced ride ad1d49b8… in the rating saga — a
	//      user ended up as both host_user_id and the only
	//      passenger_id on the same ride, the rate flow refused to
	//      submit, etc. Defence-in-depth at the booking-creation
	//      step so the bad row can't exist at all.
	//   3. The ride is still upcoming and has a passenger seat left.
	//   4. Women-only ride enforcement (existing behaviour). Done
	//      BEFORE the insert so we never write a doomed booking row.
	type requestPreflight struct {
		ID                uuid.UUID  `gorm:"column:id"`
		HostUserID        uuid.UUID  `gorm:"column:host_user_id"`
		IsSameGender      uint       `gorm:"column:is_same_gender"`
		StartTime         time.Time  `gorm:"column:start_time"`
		IsOngoing         uint       `gorm:"column:is_ongoing"`
		TotalSeats        uint       `gorm:"column:total_seats"`
		StartLocation     string     `gorm:"column:start_location"`
		EndLocation       string     `gorm:"column:end_location"`
		HostName          string     `gorm:"column:host_name"`
		HostEmail         string     `gorm:"column:host_email"`
		AcceptedCount     int64      `gorm:"column:accepted_count"`
		ExistingBookingID *uuid.UUID `gorm:"column:existing_booking_id"`
	}

	var targetRide requestPreflight
	if err := database.Database.Db.Raw(`
		SELECT
			r.id,
			r.host_user_id,
			r.is_same_gender,
			r.start_time,
			r.is_ongoing,
			r.total_seats,
			r.start_location,
			r.end_location,
			h.name AS host_name,
			h.email AS host_email,
			(
				SELECT COUNT(*)
				  FROM bookings accepted
				 WHERE accepted.ride_id = r.id
				   AND accepted.request_status = 'accepted'
				   AND accepted.deleted_at IS NULL
			) AS accepted_count,
			(
				SELECT existing.id
				  FROM bookings existing
				 WHERE existing.ride_id = r.id
				   AND existing.passenger_id = ?
				   AND existing.deleted_at IS NULL
				 LIMIT 1
			) AS existing_booking_id
		  FROM rides r
		  JOIN users h ON h.id = r.host_user_id
		 WHERE r.id = ?
		   AND r.deleted_at IS NULL
		 LIMIT 1
	`, PassengerID, booking.RideID).Scan(&targetRide).Error; err != nil || targetRide.ID == (uuid.UUID{}) {
		log.Printf("ride lookup for booking pre-flight failed: %v", err)
		return c.Status(404).SendString("Ride not found")
	}
	if targetRide.ExistingBookingID != nil {
		log.Println("Similar booking already exists")
		return c.Status(409).JSON(fiber.Map{
			"error": "Similar booking already exists",
			"code":  "already_booked",
		})
	}
	if targetRide.HostUserID == PassengerID {
		log.Printf("user %v tried to request their own ride %v", PassengerID, targetRide.ID)
		return c.Status(400).JSON(fiber.Map{
			"error": "You can't request a seat on your own ride",
			"code":  "self_booking",
		})
	}
	if targetRide.IsOngoing != 0 || !targetRide.StartTime.After(time.Now().UTC()) {
		return c.Status(409).JSON(fiber.Map{
			"error": "This ride has already started",
			"code":  "ride_started",
		})
	}
	if !helpers.CanAcceptAnotherPassenger(targetRide.TotalSeats, uint(targetRide.AcceptedCount)) {
		return c.Status(409).JSON(fiber.Map{
			"error": "No available seats for this ride",
			"code":  "ride_full",
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
	booking.RideID = targetRide.ID

	// Insert the booking record using the models.Booking struct
	if err := database.Database.Db.Create(&booking).Error; err != nil {
		log.Println("Error creating booking:", err)
		return c.Status(500).SendString("Database error")
	}

	fcmService := services.GetFCMService()
	if fcmService != nil {
		rideRoute := targetRide.StartLocation + " to " + targetRide.EndLocation
		go func() {
			if err := fcmService.SendBookingRequestNotification(targetRide.HostUserID, user.ID, user.Name, rideRoute, targetRide.ID, booking.ID); err != nil {
				log.Printf("Error sending booking request notification: %v", err)
			}
		}()
	}
	go sendBookingEmailIfAllowed(targetRide.HostUserID, helpers.BookingEmailParams{
		Kind:          helpers.BookingEmailRequested,
		ToEmail:       targetRide.HostEmail,
		ToName:        targetRide.HostName,
		ActorName:     user.Name,
		StartLocation: targetRide.StartLocation,
		EndLocation:   targetRide.EndLocation,
		StartTime:     targetRide.StartTime,
		RideID:        targetRide.ID.String(),
	})

	// Create the response
	bookingResponse := BookingResponse{
		ID:            booking.ID,
		RideID:        booking.RideID,
		RequestStatus: booking.RequestStatus,
	}

	log.Printf("Booking with ride_id %v and passenger_id %v created\n", booking.RideID, PassengerID)
	return c.Status(201).JSON(bookingResponse)
}
