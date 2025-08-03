package CRUD

import (
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Ride response model
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

// Function to create a ride
func CreateRide(c *fiber.Ctx) error {
	var ride models.Ride

	   err := c.BodyParser(&ride)
	   if err != nil {
			   log.Printf("Error parsing JSON: %v\n", err)
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Error parsing JSON (body parser)",
			   })
	   }

	//Logs a 400 error if the Ride struct is invalid
	   err = helpers.ValidateRide(ride)
	   if err != nil {
			   log.Printf("Error validating ride: %v\n", err)
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Error validating ride - (helper function)",
			   })
	   }

	// Checks if the host user ID exists
	   var hostUser models.User
	   result := database.Database.Db.First(&hostUser, ride.HostUserID)
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

	// Checks if start time is in the future
	   if ride.StartTime.Before(time.Now()) {
			   log.Printf("Start time is in the past")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Start time is in the past",
			   })
	   }

	// Checks if the host user has enough seats
	   if ride.TotalSeats <= ride.BookedSeats {
			   log.Printf("Total seats available should be more than booked seats")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Total seats available should be more than booked seats",
			   })
	   }

	if ride.IsSameGender == 1 {
		log.Printf("This ride has been created for the same gender only.")
	} else if ride.IsSameGender == 0 {
		log.Printf("This ride has been created for any gender.")
	}

	   if ride.TotalPrice < 25 || ride.TotalPrice > 10000 {
			   log.Printf("Price too low")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Price too low!",
			   })
	   }

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

// Function to get all rides
func GetRides(c *fiber.Ctx) error {
	var rides []models.Ride
	result := database.Database.Db.Find(&rides)

	   if result.Error != nil {
			   log.Printf("Error getting rides: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error getting rides",
			   })
	   }

	ridesResponse := make([]RideResponse, len(rides))

	for i, ride := range rides {
		// Find the host user
		var hostUser models.User
		result := database.Database.Db.First(&hostUser, ride.HostUserID)
			   if result.Error != nil {
					   log.Printf("Error finding host user: %v\n", result.Error)
					   return c.Status(502).JSON(fiber.Map{
							   "error": "Error finding host user",
					   })
			   }

		ridesResponse[i] = RideResponse{
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
	}

	return c.Status(200).JSON(ridesResponse)
}

// Function to get a ride by ID
func GetRideByID(c *fiber.Ctx) error {

	rideID := c.Params("id")

	// Start a database transaction
	tx := database.Database.Db.Begin()

	var ride models.Ride

	parsedID, err := uuid.Parse(rideID)
	if err != nil {
		tx.Rollback()
		log.Printf("Invalid rideID format: %v", rideID)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid ride ID format",
		})
	}
	if err := tx.First(&ride, "id = ?", parsedID).Error; err != nil {
		tx.Rollback()
		log.Printf("Ride does not exist")
		return c.Status(404).JSON(fiber.Map{
			"error": "Ride not found",
		})
	}

	// Find the host user
	var hostUser models.User
	   if err := tx.First(&hostUser, ride.HostUserID).Error; err != nil {
			   // Rollback the transaction in case of an error
			   tx.Rollback()
			   log.Printf("Host user not found")
			   return c.Status(502).JSON(fiber.Map{
					   "error": "Error finding host user",
			   })
	   }

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

	return c.Status(200).JSON(rideResponse)
}

// Function to update a ride by ID
func UpdateRideByID(c *fiber.Ctx) error {
	rideID := c.Params("id")

	var ride models.Ride
	result := database.Database.Db.First(&ride, rideID)

	   if result.Error == gorm.ErrRecordNotFound {
			   log.Printf("Ride with id %v not found\n", rideID)
			   return c.Status(404).JSON(fiber.Map{
					   "error": "Ride not found",
			   })
	   } else if result.Error != nil {
			   log.Printf("Error finding ride: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error finding ride",
			   })
	   }

	var newRide models.Ride
	err := c.BodyParser(&newRide)
	   if err != nil {
			   log.Printf("Error parsing JSON: %v\n", err)
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Error parsing JSON (body parser)",
			   })
	   }

	// Check if the host user ID exists
	var hostUser models.User
	result = database.Database.Db.First(&hostUser, newRide.HostUserID)
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
	   if newRide.StartTime.Before(time.Now()) {
			   log.Printf("Start time is in the past")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Start time is in the past",
			   })
	   }

	// Check if the host user has enough seats
	   if newRide.TotalSeats <= newRide.BookedSeats {
			   log.Printf("Total seats available should be more than booked seats")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Total seats available should be more than booked seats",
			   })
	   }

	if newRide.IsSameGender == 1 {
		log.Printf("This ride has been created for the same gender only.")
	} else if newRide.IsSameGender == 0 {
		log.Printf("This ride has been created for any gender.")
	}

	   if newRide.TotalPrice < 25 || newRide.TotalPrice > 10000 {
			   log.Printf("Price too low")
			   return c.Status(400).JSON(fiber.Map{
					   "error": "Price too low!",
			   })
	   }

	// Update the ride in the database
	result = database.Database.Db.Model(&ride).Updates(newRide)

	   if result.Error != nil {
			   log.Printf("Error updating ride: %v\n", result.Error)
			   return c.Status(500).JSON(fiber.Map{
					   "error": "Error updating ride",
			   })
	   }

	// Track changes, and display them as a result
	rideResponse := RideResponse{
		RideID:        ride.ID,
		HostUserID:    ride.HostUserID,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		StartTime:     ride.StartTime,
		TotalSeats:    ride.TotalSeats,
		BookedSeats:   ride.BookedSeats,
		TotalPrice:    ride.TotalPrice,
		IsOngoing:     ride.IsOngoing,
		IsSameGender:  ride.IsSameGender,
	}

	log.Printf("Ride with id %v updated\n", ride.ID)
	return c.Status(200).JSON(rideResponse)
}

// Function to delete a ride by ID
func DeleteRideByID(c *fiber.Ctx) error {
	rideID := c.Params("id")

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

	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		log.Printf("Invalid ride ID format: %v\n", err)
		return c.Status(400).JSON(fiber.Map{
			"error": "Invalid ride ID format",
		})
	}

	var ride models.Ride
	result := database.Database.Db.Where("id = ?", rideUUID).First(&ride)

	if result.Error == gorm.ErrRecordNotFound {
		log.Printf("Ride with id %v not found\n", rideID)
		return c.Status(404).JSON(fiber.Map{
			"error": "Ride not found",
		})
	} else if result.Error != nil {
		log.Printf("Error finding ride: %v\n", result.Error)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error finding ride",
		})
	}

	if ride.HostUserID != user.ID {
		log.Printf("User %v is not authorized to delete ride %v (host: %v)\n", user.ID, ride.ID, ride.HostUserID)
		return c.Status(403).JSON(fiber.Map{
			"error": "Only the ride host can delete this ride",
		})
	}

	var acceptedBookingsCount int64
	err = database.Database.Db.Model(&models.Booking{}).
		Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").
		Count(&acceptedBookingsCount).Error
	
	if err != nil {
		log.Printf("Error checking bookings for ride %v: %v\n", rideID, err)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error checking ride bookings",
		})
	}

	if acceptedBookingsCount > 0 {
		log.Printf("Cannot delete ride %v: has %d accepted bookings\n", rideID, acceptedBookingsCount)
		return c.Status(400).JSON(fiber.Map{
			"error": "Cannot delete ride with accepted bookings. Please remove all passengers first.",
		})
	}

	result = database.Database.Db.Delete(&ride)

	if result.Error != nil {
		log.Printf("Error deleting ride: %v\n", result.Error)
		return c.Status(500).JSON(fiber.Map{
			"error": "Error deleting ride",
		})
	}

	log.Printf("Ride with id %v deleted by user %v\n", ride.ID, user.ID)
	return c.Status(200).JSON(fiber.Map{
		"message": "Ride deleted successfully",
	})
}
