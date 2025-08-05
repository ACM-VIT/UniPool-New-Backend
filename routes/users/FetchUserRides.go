package users

import (
	"context"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// UserRidesResponse is the response model representing a single ride record
// related to a particular user.
type UserRidesResponse struct {
	RideID        uuid.UUID  `json:"ride_id"`
	HostUserID    uuid.UUID  `json:"host_user_id"`
	StartLocation string     `json:"start_location"`
	EndLocation   string     `json:"end_location"`
	StartTime     time.Time  `json:"start_time"`
	TotalSeats    uint       `json:"total_seats"`
	BookedSeats   uint       `json:"booked_seats"`
	TotalPrice    uint       `json:"total_price"`
	IsOngoing     uint       `json:"is_ongoing"`
	IsSameGender  uint       `json:"is_same_gender"`
	PassengerID   *uuid.UUID `json:"passenger_id,omitempty"`
	// Add any other fields you wish to return (e.g., host user name, etc.)
}

func FetchUserRides(c *fiber.Ctx) error {
	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"error": "User not authenticated or not found",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"error": "Invalid user data",
		})
	}

	userUUID := user.ID
	userRides := make([]UserRidesResponse, 0)

	// Add context timeout for the query
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Optimized query with proper indexing
	if err := database.Database.Db.WithContext(ctx).
		Table("rides").
		Select(`DISTINCT rides.id AS ride_id,
				rides.host_user_id,
				rides.start_location,
				rides.end_location,
				rides.start_time,
				rides.total_seats,
				rides.booked_seats,
				rides.total_price,
				rides.is_ongoing,
				rides.is_same_gender,
				bookings.passenger_id`).
		Joins("LEFT JOIN bookings ON bookings.ride_id = rides.id").
		Where("(rides.host_user_id = ? OR bookings.passenger_id = ?)", userUUID, userUUID).
		Order("rides.start_time DESC").
		Scan(&userRides).Error; err != nil {
		log.Printf("Error finding user rides: %v\n", err)
		return c.Status(fiber.StatusBadGateway).SendString("Error finding rides for user")
	}
	return c.Status(fiber.StatusOK).JSON(userRides)
}
