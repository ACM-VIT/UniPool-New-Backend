package rides

import (
	"errors"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RideCard struct {
	RideID                    uuid.UUID `json:"id"`
	HostUserID                uuid.UUID `json:"host_user_id"`
	HostUserName              string    `json:"host_user_name"`
	HostUserProfilePictureURL string    `json:"host_user_profile_picture_url"`
	StartLocation             string    `json:"start_location"`
	EndLocation               string    `json:"end_location"`
	StartTime                 time.Time `json:"start_time"`
	TotalSeats                uint      `json:"total_seats"`
	BookedSeats               uint      `json:"booked_seats"`
	TotalPrice                uint      `json:"total_price"`
}

func SearchRides(c *fiber.Ctx) error {
	// Constants
	const similarityThreshold = 0.4

	// Query parameters
	startLocation := c.Query("start_location")
	endLocation := c.Query("end_location")
	date := c.Query("date")
	userid := c.Locals("user").(*models.User).ID

	// Retrieve rides from the database
	var rides []models.Ride
	tx := database.Database.Db.Model(&models.Ride{})

	// First remove all rides that are full or have already started
	tx = tx.Where("booked_seats < total_seats AND start_time > NOW()")

	// Filtering by start location
	if startLocation != "" {
		tx = tx.Debug().Where("start_location ILIKE ? OR similarity(start_location, ?) > ?", "%"+startLocation+"%", startLocation, similarityThreshold)
	}

	// Filtering by end location
	if endLocation != "" {
		tx = tx.Debug().Where("end_location ILIKE ? OR similarity(end_location, ?) > ?", "%"+endLocation+"%", endLocation, similarityThreshold)
	}

	// Filtering by date
	if date != "" {
		inputDate, err := time.Parse("2006-01-02T15:04:05.000Z", date)
		if err != nil {
			log.Printf("Error parsing date: %v\n", err)
			return c.Status(400).SendString("Error parsing date")
		}

		// Manually adjust the time to UTC+5:30
		inputDate = inputDate.Add(5*time.Hour + 30*time.Minute)

		// Construct the start and end of the day in the adjusted time zone
		startOfDay := time.Date(inputDate.Year(), inputDate.Month(), inputDate.Day(), 0, 0, 0, 0, inputDate.Location())
		startOfDay = startOfDay.Add(-5*time.Hour - 30*time.Minute)
		endOfDay := time.Date(inputDate.Year(), inputDate.Month(), inputDate.Day(), 23, 59, 59, 0, inputDate.Location())
		endOfDay = endOfDay.Add(-5*time.Hour - 30*time.Minute)

		tx = tx.Debug().Where("start_time BETWEEN ? AND ?", startOfDay, endOfDay)
	}

	if userid != uuid.Nil {
		tx = tx.Where("host_user_id != ?", userid)
	}

	// Fetch rides from the database and include related user data
	err := tx.Preload("HostUser").Find(&rides).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.Status(404).SendString("No rides found")
	}

	if err != nil {
		return c.Status(500).SendString("Error fetching rides")
	}

	var responseRides []RideCard

	// Iterate over retrieved rides and format response
	for _, ride := range rides {
		responseRide := RideCard{
			RideID:                    ride.ID,
			HostUserID:                ride.HostUserID,
			HostUserName:              ride.HostUser.Name,
			HostUserProfilePictureURL: ride.HostUser.ProfilePictureURL,
			StartLocation:             ride.StartLocation,
			EndLocation:               ride.EndLocation,
			StartTime:                 ride.StartTime,
			TotalSeats:                ride.TotalSeats,
			BookedSeats:               ride.BookedSeats,
			TotalPrice:                ride.TotalPrice,
		}

		responseRides = append(responseRides, responseRide)
	}

	return c.Status(200).JSON(responseRides)
}
