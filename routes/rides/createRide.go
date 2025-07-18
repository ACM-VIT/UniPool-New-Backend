package rides

import (
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RideResponse struct {
	RideID        uuid.UUID `json:"id"`
	HostUserID    uuid.UUID `json:"host_user_id"`
	HostUserName  string    `json:"host_user_name"`
	StartLocation string    `json:"start_location"`
	EndLocation   string    `json:"end_location"`
	StartTime     time.Time `json:"start_time"`
	TotalSeats    uint      `json:"total_seats"`
	BookedSeats   uint      `json:"booked_seats"`
	TotalPrice    uint      `json:"total_price"`
	IsOngoing     uint      `json:"is_ongoing"`
	IsSameGender  uint      `json:"is_same_gender"`
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
		return c.Status(400).SendString("Error parsing JSON (body parser)")
	}

	// Set the host user ID from locals into the Ride struct
	ride.HostUserID = hostUserID

	// Fetch the host user from the database
	var hostUser models.User
	result := database.Database.Db.First(&hostUser, hostUserID)
	if result.Error == gorm.ErrRecordNotFound {
		log.Printf("Host user ID does not exist")
		return c.Status(400).SendString("Host user ID does not exist")
	} else if result.Error != nil {
		log.Printf("Error finding host user: %v\n", result.Error)
		return c.Status(502).SendString("Error finding host user")
	}

	// Check if start time is in the future
	if ride.StartTime.Before(time.Now()) {
		log.Printf("Start time is in the past")
		return c.Status(400).SendString("Start time is in the past")
	}

	// Check if the host user has enough seats
	if ride.TotalSeats <= ride.BookedSeats {
		log.Printf("Total seats available should be more than booked seats")
		return c.Status(400).SendString("Total seats available should be more than booked seats")
	}

	// Log gender-specific ride information
	if ride.IsSameGender == 1 {
		log.Printf("This ride has been created for the same gender only.")
	} else if ride.IsSameGender == 0 {
		log.Printf("This ride has been created for any gender.")
	}

	// Validate ride price
	if ride.TotalPrice < 25 || ride.TotalPrice > 10000 {
		log.Printf("Price too low")
		return c.Status(400).SendString("Price too low!")
	}

	// Create the ride in the database
	result = database.Database.Db.Create(&ride)
	if result.Error != nil {
		log.Printf("Error creating ride: %v\n", result.Error)
		return c.Status(500).SendString("Error creating ride - (database creation error)")
	}

	// Create the ride response
	rideResponse := RideResponse{
		RideID:        ride.ID,
		HostUserID:    ride.HostUserID,
		HostUserName:  hostUser.Name,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime,
		TotalSeats:    ride.TotalSeats,
		BookedSeats:   ride.BookedSeats,
		TotalPrice:    ride.TotalPrice,
		IsOngoing:     ride.IsOngoing,
		IsSameGender:  ride.IsSameGender,
	}

	log.Printf("Ride with id %v created\n", ride.ID)
	return c.Status(200).JSON(rideResponse)
}
