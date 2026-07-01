package rides

import (
	"log"
	"strings"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RideResponse struct {
	RideID         uuid.UUID `json:"id"`
	HostUserID     uuid.UUID `json:"host_user_id"`
	HostUserName   string    `json:"host_user_name"`
	StartLocation  string    `json:"start_location"`
	EndLocation    string    `json:"end_location"`
	StartTime      time.Time `json:"start_time"`
	TotalSeats     uint      `json:"total_seats"`
	BookedSeats    uint      `json:"booked_seats"`
	TotalPrice     uint      `json:"total_price"`
	IsOngoing      uint      `json:"is_ongoing"`
	IsSameGender   uint      `json:"is_same_gender"`
	StartLatitude  *float64  `json:"start_latitude,omitempty"`
	StartLongitude *float64  `json:"start_longitude,omitempty"`
	EndLatitude    *float64  `json:"end_latitude,omitempty"`
	EndLongitude   *float64  `json:"end_longitude,omitempty"`
}

func CreateRide(c *fiber.Ctx) error {
	var ride models.Ride

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

	hostUserID := user.ID

	// Parse the request body into a Ride struct
	err := c.BodyParser(&ride)
	if err != nil {
		log.Printf("Error parsing JSON: %v\n", err)
		return c.Status(400).JSON(fiber.Map{
			"error": "Error parsing JSON (body parser)",
		})
	}

	// Set the host user ID from locals into the Ride struct
	ride.HostUserID = hostUserID

	// Fetch the host user from the database
	var hostUser models.User
	result := database.Database.Db.First(&hostUser, hostUserID)
	if result.Error == gorm.ErrRecordNotFound {
		log.Printf("Host user ID does not exist")
		return c.Status(400).JSON(fiber.Map{
			"error": "Host user ID does not exist",
		})
	} else if result.Error != nil {
		log.Printf("Error finding host user: %v\n", result.Error)
		return c.Status(502).JSON(fiber.Map{
			"error": "Error finding host user",
		})
	}

	// Check if start time is in the future
	if ride.StartTime.Before(time.Now().UTC()) {
		log.Printf("Start time is in the past")
		return c.Status(400).JSON(fiber.Map{
			"error": "Start time is in the past",
		})
	}

	// Minimum total_seats is 2 (host + 1 passenger). Anything less
	// means there's no passenger slot to offer — the ride wouldn't
	// be useful to anyone. See helpers/seats.go for the canonical
	// contract on what total_seats includes.
	if ride.TotalSeats < helpers.MinTotalSeats {
		log.Printf("Total seats must be at least %d (host + 1 passenger)", helpers.MinTotalSeats)
		return c.Status(400).JSON(fiber.Map{
			"error": "Total seats must include you plus at least one passenger",
		})
	}
	// Booked seats can never exceed the passenger capacity at
	// create time. New rides always have booked_seats=0, so this
	// is really only defensive — but cheap belt-and-braces.
	if ride.BookedSeats > helpers.PassengerCapacity(ride.TotalSeats) {
		log.Printf("Booked seats exceed passenger capacity")
		return c.Status(400).JSON(fiber.Map{
			"error": "Booked seats cannot exceed passenger capacity",
		})
	}

	// Women-only rides: server-side gate. Only female hosts can flag a
	// ride as same-gender. Without this check, anyone (incl. male
	// hosts) could send `is_same_gender=1` and the ride would surface
	// as "Women-only" on the search screen — making the safety signal
	// meaningless. UI hides the toggle for non-female users, but the
	// server is the source of truth.
	if ride.IsSameGender == 1 {
		if strings.ToLower(hostUser.Gender) != "female" {
			log.Printf("Non-female user %v tried to create a same-gender ride", hostUserID)
			return c.Status(403).JSON(fiber.Map{
				"error": "Only female hosts can create women-only rides",
			})
		}
		log.Printf("This ride has been created for the same gender only.")
	} else if ride.IsSameGender == 0 {
		log.Printf("This ride has been created for any gender.")
	}

	// Validate ride price
	if ride.TotalPrice < 25 || ride.TotalPrice > 10000 {
		log.Printf("Price too low")
		return c.Status(400).JSON(fiber.Map{
			"error": "Price too low!",
		})
	}

	applyCanonicalRideCoordinates(&ride)

	// Create the ride in the database
	result = database.Database.Db.Create(&ride)
	if result.Error != nil {
		log.Printf("Error creating ride: %v\n", result.Error)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error creating ride - (database creation error)",
		})
	}

	// Create the ride response
	rideResponse := RideResponse{
		RideID:         ride.ID,
		HostUserID:     ride.HostUserID,
		HostUserName:   hostUser.Name,
		StartLocation:  ride.StartLocation,
		EndLocation:    ride.EndLocation,
		StartTime:      ride.StartTime,
		TotalSeats:     ride.TotalSeats,
		BookedSeats:    ride.BookedSeats,
		TotalPrice:     ride.TotalPrice,
		IsOngoing:      ride.IsOngoing,
		IsSameGender:   ride.IsSameGender,
		StartLatitude:  ride.StartLatitude,
		StartLongitude: ride.StartLongitude,
		EndLatitude:    ride.EndLatitude,
		EndLongitude:   ride.EndLongitude,
	}

	log.Printf("Ride with id %v created\n", ride.ID)
	return c.Status(200).JSON(rideResponse)
}

func applyCanonicalRideCoordinates(ride *models.Ride) {
	if lat, lon, ok := helpers.CanonicalLocationCoords(ride.StartLocation); ok {
		ride.StartLatitude = &lat
		ride.StartLongitude = &lon
	}
	if lat, lon, ok := helpers.CanonicalLocationCoords(ride.EndLocation); ok {
		ride.EndLatitude = &lat
		ride.EndLongitude = &lon
	}
}
