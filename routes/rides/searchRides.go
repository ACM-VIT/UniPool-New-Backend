package rides

import (
	"errors"
	"log"
	"strconv"
	"sort"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
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
	HostUserYOB               uint      `json:"host_user_yob,omitempty"`
	StartLocation             string    `json:"start_location"`
	EndLocation               string    `json:"end_location"`
	StartTime                 time.Time `json:"start_time"`
	TotalSeats                uint      `json:"total_seats"`
	BookedSeats               uint      `json:"booked_seats"`
	TotalPrice                uint      `json:"total_price"`
	StartLatitude             *float64  `json:"start_latitude,omitempty"`
	StartLongitude            *float64  `json:"start_longitude,omitempty"`
	EndLatitude               *float64  `json:"end_latitude,omitempty"`
	EndLongitude              *float64  `json:"end_longitude,omitempty"`
	StartDistance             *float64  `json:"start_distance,omitempty"`
	EndDistance               *float64  `json:"end_distance,omitempty"`
	TotalDistance             *float64  `json:"total_distance,omitempty"`
}

func SearchRides(c *fiber.Ctx) error {
	// Constants
	const similarityThreshold = 0.4
	const defaultRadius = 10.0 // Default search radius in kilometers

	// Query parameters
	startLocation := c.Query("start_location")
	endLocation := c.Query("end_location")
	date := c.Query("date")
	
	// New coordinate-based search parameters
	startLatStr := c.Query("start_lat")
	startLonStr := c.Query("start_lon")
	endLatStr := c.Query("end_lat")
	endLonStr := c.Query("end_lon")
	radiusStr := c.Query("radius")

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

	userid := user.ID

	// Parse coordinate parameters
	var startLat, startLon, endLat, endLon *float64
	var radius float64 = defaultRadius

	if startLatStr != "" && startLonStr != "" {
		if lat, err := strconv.ParseFloat(startLatStr, 64); err == nil {
			startLat = &lat
		}
		if lon, err := strconv.ParseFloat(startLonStr, 64); err == nil {
			startLon = &lon
		}
	}

	if endLatStr != "" && endLonStr != "" {
		if lat, err := strconv.ParseFloat(endLatStr, 64); err == nil {
			endLat = &lat
		}
		if lon, err := strconv.ParseFloat(endLonStr, 64); err == nil {
			endLon = &lon
		}
	}

	if radiusStr != "" {
		if r, err := strconv.ParseFloat(radiusStr, 64); err == nil && r > 0 {
			radius = r
		}
	}

	// Retrieve rides from the database
	var rides []models.Ride
	tx := database.Database.Db.Model(&models.Ride{})

	// First remove all rides that are full or have already started
	tx = tx.Where("booked_seats < total_seats AND start_time > NOW()")

	// Apply date filter first if provided
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

	// Check if we should use geographic search
	useGeographicSearch := helpers.AreCoordinatesValid(startLat, startLon) || helpers.AreCoordinatesValid(endLat, endLon)

	if !useGeographicSearch {
		// Use traditional string-based search
		if startLocation != "" {
			tx = tx.Debug().Where("start_location ILIKE ? OR similarity(start_location, ?) > ?", "%"+startLocation+"%", startLocation, similarityThreshold)
		}

		if endLocation != "" {
			tx = tx.Debug().Where("end_location ILIKE ? OR similarity(end_location, ?) > ?", "%"+endLocation+"%", endLocation, similarityThreshold)
		}
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

	// Process rides with geographic filtering if coordinates are provided
	if useGeographicSearch {
		// Filter rides based on geographic distance
		for _, ride := range rides {
			matchesStart := true
			matchesEnd := true
			
			var startDistance, endDistance *float64

			// Check start location distance
			if helpers.AreCoordinatesValid(startLat, startLon) {
				if helpers.AreCoordinatesValid(ride.StartLatitude, ride.StartLongitude) {
					distance := helpers.CalculateDistance(*startLat, *startLon, *ride.StartLatitude, *ride.StartLongitude)
					startDistance = &distance
					matchesStart = distance <= radius
				} else {
					// Fallback to string similarity if ride doesn't have coordinates
					if startLocation != "" {
						var count int64
						database.Database.Db.Raw("SELECT COUNT(*) FROM rides WHERE id = ? AND (start_location ILIKE ? OR similarity(start_location, ?) > ?)", 
							ride.ID, "%"+startLocation+"%", startLocation, similarityThreshold).Scan(&count)
						matchesStart = count > 0
					}
				}
			} else if startLocation != "" {
				// String-based search as fallback
				var count int64
				database.Database.Db.Raw("SELECT COUNT(*) FROM rides WHERE id = ? AND (start_location ILIKE ? OR similarity(start_location, ?) > ?)", 
					ride.ID, "%"+startLocation+"%", startLocation, similarityThreshold).Scan(&count)
				matchesStart = count > 0
			}

			// Check end location distance
			if helpers.AreCoordinatesValid(endLat, endLon) {
				if helpers.AreCoordinatesValid(ride.EndLatitude, ride.EndLongitude) {
					distance := helpers.CalculateDistance(*endLat, *endLon, *ride.EndLatitude, *ride.EndLongitude)
					endDistance = &distance
					matchesEnd = distance <= radius
				} else {
					// Fallback to string similarity if ride doesn't have coordinates
					if endLocation != "" {
						var count int64
						database.Database.Db.Raw("SELECT COUNT(*) FROM rides WHERE id = ? AND (end_location ILIKE ? OR similarity(end_location, ?) > ?)", 
							ride.ID, "%"+endLocation+"%", endLocation, similarityThreshold).Scan(&count)
						matchesEnd = count > 0
					}
				}
			} else if endLocation != "" {
				// String-based search as fallback
				var count int64
				database.Database.Db.Raw("SELECT COUNT(*) FROM rides WHERE id = ? AND (end_location ILIKE ? OR similarity(end_location, ?) > ?)", 
					ride.ID, "%"+endLocation+"%", endLocation, similarityThreshold).Scan(&count)
				matchesEnd = count > 0
			}

			// Include ride if it matches both criteria
			if matchesStart && matchesEnd {
				responseRide := RideCard{
					RideID:                    ride.ID,
					HostUserID:                ride.HostUserID,
					HostUserName:              ride.HostUser.Name,
					HostUserProfilePictureURL: ride.HostUser.ProfilePictureURL,
					HostUserYOB:               ride.HostUser.YOB,
					StartLocation:             ride.StartLocation,
					EndLocation:               ride.EndLocation,
					StartTime:                 ride.StartTime,
					TotalSeats:                ride.TotalSeats,
					BookedSeats:               ride.BookedSeats,
					TotalPrice:                ride.TotalPrice,
					StartLatitude:             ride.StartLatitude,
					StartLongitude:            ride.StartLongitude,
					EndLatitude:               ride.EndLatitude,
					EndLongitude:              ride.EndLongitude,
					StartDistance:             startDistance,
					EndDistance:               endDistance,
				}

				// Calculate total distance for sorting
				if startDistance != nil && endDistance != nil {
					totalDist := *startDistance + *endDistance
					responseRide.TotalDistance = &totalDist
				} else if startDistance != nil {
					responseRide.TotalDistance = startDistance
				} else if endDistance != nil {
					responseRide.TotalDistance = endDistance
				}

				responseRides = append(responseRides, responseRide)
			}
		}

		// Sort by distance (closest first), then by start time
		sort.Slice(responseRides, func(i, j int) bool {
			if responseRides[i].TotalDistance != nil && responseRides[j].TotalDistance != nil {
				if *responseRides[i].TotalDistance != *responseRides[j].TotalDistance {
					return *responseRides[i].TotalDistance < *responseRides[j].TotalDistance
				}
			}
			return responseRides[i].StartTime.Before(responseRides[j].StartTime)
		})

	} else {
		// Use traditional processing for string-based search
		for _, ride := range rides {
			responseRide := RideCard{
				RideID:                    ride.ID,
				HostUserID:                ride.HostUserID,
				HostUserName:              ride.HostUser.Name,
				HostUserProfilePictureURL: ride.HostUser.ProfilePictureURL,
				HostUserYOB:               ride.HostUser.YOB,
				StartLocation:             ride.StartLocation,
				EndLocation:               ride.EndLocation,
				StartTime:                 ride.StartTime,
				TotalSeats:                ride.TotalSeats,
				BookedSeats:               ride.BookedSeats,
				TotalPrice:                ride.TotalPrice,
				StartLatitude:             ride.StartLatitude,
				StartLongitude:            ride.StartLongitude,
				EndLatitude:               ride.EndLatitude,
				EndLongitude:              ride.EndLongitude,
			}

			responseRides = append(responseRides, responseRide)
		}
	}

	return c.Status(200).JSON(responseRides)
}
