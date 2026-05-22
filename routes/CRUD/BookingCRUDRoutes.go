package CRUD

import (
	"context"
	"errors"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BookingResponse is used to serialize/deserialize Booking data
type BookingResponse struct {
	ID            uuid.UUID `json:"id"`
	RideID        uuid.UUID `json:"ride_id"`
	PassengerID   uuid.UUID `json:"passenger_id"`
	RequestStatus string    `json:"request_status"`
}

// CreateBooking handles the creation of a new booking
func CreateBooking(c *fiber.Ctx) error {
	var booking models.Booking

	// Parse JSON
	if err := c.BodyParser(&booking); err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid JSON body"}
	}

	if err := helpers.ValidateBooking(booking); err != nil {
		return &fiber.Error{Code: 400, Message: err.Error()}
	}

	// Check if a booking with the same RideID and PassengerID already exists
	var existingBooking models.Booking
	err := database.Database.Db.Where("ride_id = ? AND passenger_id = ?", booking.RideID, booking.PassengerID).
		First(&existingBooking).Error
	if err == nil {
		// Found an existing booking -> conflict
		return c.Status(409).SendString("Booking already exists for this ride and passenger")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error checking existing booking"}
	}

	// Create the booking
	if err := database.Database.Db.Create(&booking).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}

	log.Printf("Booking with id %v created\n", booking.ID)

	bookingResponse := BookingResponse{
		ID:            booking.ID,
		RideID:        booking.RideID,
		PassengerID:   booking.PassengerID,
		RequestStatus: booking.RequestStatus,
	}

	return c.Status(201).JSON(bookingResponse)
}

// GetBookings retrieves all bookings in the database
func GetBookings(c *fiber.Ctx) error {
	var bookings []models.Booking

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

	if err := database.Database.Db.
		Joins("JOIN rides ON rides.id = bookings.ride_id").
		Preload("Ride").
		Preload("Passenger").
		Where("bookings.passenger_id = ?", userUUID).
		Order("rides.start_time ASC").
		Find(&bookings).Error; err != nil {
		log.Printf("Error finding bookings: %v\n", err)
		return c.Status(502).SendString("Error finding bookings")
	}

	type BookingWithRideDetails struct {
		ID            string      `json:"id"`
		RideID        string      `json:"ride_id"`
		PassengerID   string      `json:"passenger_id"`
		RequestStatus string      `json:"request_status"`
		RideDetails   interface{} `json:"ride_details"`
	}

	bookingsResponse := make([]BookingWithRideDetails, len(bookings))
	for i, b := range bookings {
		bookingsResponse[i] = BookingWithRideDetails{
			ID:            b.ID.String(),
			RideID:        b.RideID.String(),
			PassengerID:   b.PassengerID.String(),
			RequestStatus: b.RequestStatus,
			RideDetails:   b.Ride, // This will include all ride fields
		}
	}

	log.Printf("Returning %d bookings: %+v\n", len(bookingsResponse), bookingsResponse)
	return c.Status(200).JSON(fiber.Map{"bookings": bookingsResponse})
}

func GetBookingsByRideID(c *fiber.Ctx) error {
	rideIDParam := c.Params("ride_id")
	rideID, err := uuid.Parse(rideIDParam)
	if err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid ride ID format"}
	}

	// Add context timeout for database operations
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var bookings []models.Booking
	if err := database.Database.Db.WithContext(ctx).Preload("Ride").Preload("Passenger").Where("ride_id = ?", rideID).Find(&bookings).Error; err != nil {
		log.Printf("Error finding bookings for ride %v: %v\n", rideID, err)
		return c.Status(502).SendString("Error finding bookings for ride")
	}

	type BookingWithRideDetails struct {
		ID            string      `json:"id"`
		RideID        string      `json:"ride_id"`
		PassengerID   string      `json:"passenger_id"`
		RequestStatus string      `json:"request_status"`
		RideDetails   interface{} `json:"ride_details"`
	}

	bookingsResponse := make([]BookingWithRideDetails, len(bookings))
	for i, b := range bookings {
		bookingsResponse[i] = BookingWithRideDetails{
			ID:            b.ID.String(),
			RideID:        b.RideID.String(),
			PassengerID:   b.PassengerID.String(),
			RequestStatus: b.RequestStatus,
			RideDetails:   b.Ride,
		}
	}

	log.Printf("Returning %d bookings for ride %v\n", len(bookingsResponse), rideID)
	return c.Status(200).JSON(fiber.Map{"bookings": bookingsResponse})
}

// GetBookingByID retrieves a single booking by its UUID
func GetBookingByID(c *fiber.Ctx) error {
	idParam := c.Params("id")

	bookingID, err := uuid.Parse(idParam)
	if err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid booking ID format"}
	}

	var booking models.Booking
	result := database.Database.Db.First(&booking, "id = ?", bookingID)
	if result.Error != nil {
		log.Printf("Error finding booking: %v\n", result.Error)
		return c.Status(404).SendString("Booking not found")
	}

	bookingResponse := BookingResponse{
		ID:            booking.ID,
		RideID:        booking.RideID,
		PassengerID:   booking.PassengerID,
		RequestStatus: booking.RequestStatus,
	}

	return c.Status(200).JSON(bookingResponse)
}

// UpdateBooking updates the details of an existing booking
func UpdateBooking(c *fiber.Ctx) error {
	idParam := c.Params("id")

	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"success": false,
			"error":   "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"success": false,
			"error":   "Invalid user data",
		})
	}

	bookingID, err := uuid.Parse(idParam)
	if err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid booking ID format"}
	}

	var bookingPayload models.Booking
	if err := c.BodyParser(&bookingPayload); err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid JSON body"}
	}

	if err := helpers.ValidateBooking(bookingPayload); err != nil {
		return &fiber.Error{Code: 400, Message: err.Error()}
	}

	// Find the existing booking
	var existingBooking models.Booking
	err = database.Database.Db.First(&existingBooking, "id = ?", bookingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).SendString("Booking not found")
		}
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
	}

	var ride models.Ride
	if err := database.Database.Db.First(&ride, existingBooking.RideID).Error; err != nil {
		log.Printf("Error finding ride with ID %v: %v\n", existingBooking.RideID, err)
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Ride not found",
			"booking_id": bookingID.String(),
		})
	}

	if ride.HostUserID != user.ID && existingBooking.PassengerID != user.ID {
		log.Printf("User %v is not authorized to update booking %v (host: %v, passenger: %v)\n", user.ID, existingBooking.ID, ride.HostUserID, existingBooking.PassengerID)
		return c.Status(403).JSON(fiber.Map{
			"success":    false,
			"error":      "Only the ride host or the passenger can update this booking",
			"booking_id": bookingID.String(),
		})
	}

	updated := false

	// Update fields if changed
	if bookingPayload.RequestStatus != "" && bookingPayload.RequestStatus != existingBooking.RequestStatus {
		existingBooking.RequestStatus = bookingPayload.RequestStatus
		updated = true
	}

	if updated {
		if err := database.Database.Db.Save(&existingBooking).Error; err != nil {
			log.Println(err)
			return &fiber.Error{Code: 500, Message: "Database error"}
		}

		log.Printf("Booking with id %v updated\n", existingBooking.ID)
		bookingResponse := BookingResponse{
			ID:            existingBooking.ID,
			RideID:        existingBooking.RideID,
			PassengerID:   existingBooking.PassengerID,
			RequestStatus: existingBooking.RequestStatus,
		}
		return c.Status(200).JSON(bookingResponse)
	}

	return c.Status(409).SendString("No fields were updated")
}

// DeleteBooking deletes an existing booking
func DeleteBooking(c *fiber.Ctx) error {
	idParam := c.Params("id")

	userInterface := c.Locals("user")
	if userInterface == nil {
		return c.Status(401).JSON(fiber.Map{
			"success": false,
			"error":   "User not authenticated",
		})
	}

	user, ok := userInterface.(models.User)
	if !ok {
		return c.Status(401).JSON(fiber.Map{
			"success": false,
			"error":   "Invalid user data",
		})
	}

	bookingID, err := uuid.Parse(idParam)
	if err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid booking ID format"}
	}

	tx := database.Database.Db.Begin()
	if tx.Error != nil {
		return &fiber.Error{Code: 500, Message: "Database error"}
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	var booking models.Booking
	err = tx.First(&booking, "id = ?", bookingID).Error
	if err != nil {
		tx.Rollback()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).SendString("Booking not found")
		}
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
	}

	var ride models.Ride
	if err := tx.First(&ride, booking.RideID).Error; err != nil {
		tx.Rollback()
		log.Printf("Error finding ride with ID %v: %v\n", booking.RideID, err)
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Ride not found",
			"booking_id": bookingID.String(),
		})
	}

	if ride.HostUserID != user.ID && booking.PassengerID != user.ID {
		tx.Rollback()
		log.Printf("User %v is not authorized to delete booking %v (host: %v, passenger: %v)\n", user.ID, booking.ID, ride.HostUserID, booking.PassengerID)
		return c.Status(403).JSON(fiber.Map{
			"success":    false,
			"error":      "Only the ride host or the passenger can delete this booking",
			"booking_id": bookingID.String(),
		})
	}

	// Perform the deletion
	if err := tx.Delete(&booking).Error; err != nil {
		tx.Rollback()
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}

	if booking.RequestStatus == "accepted" {
		if err := tx.Model(&models.Ride{}).
			Where("id = ? AND booked_seats > 0", booking.RideID).
			Update("booked_seats", gorm.Expr("booked_seats - 1")).Error; err != nil {
			tx.Rollback()
			log.Printf("Error decrementing booked seats for ride %v: %v\n", booking.RideID, err)
			return &fiber.Error{Code: 500, Message: "Database error"}
		}
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("Error committing booking delete transaction: %v\n", err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}

	log.Printf("Booking with id %v deleted\n", booking.ID)
	return c.SendStatus(204)
}
