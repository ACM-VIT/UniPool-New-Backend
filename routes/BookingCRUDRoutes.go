package routes

import (
	"errors"
	"log"
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

	if err := database.Database.Db.Find(&bookings).Error; err != nil {
		log.Printf("Error finding bookings: %v\n", err)
		return c.Status(502).SendString("Error finding bookings")
	}

	bookingsResponse := make([]BookingResponse, len(bookings))
	for i, b := range bookings {
		bookingsResponse[i] = BookingResponse{
			ID:            b.ID,
			RideID:        b.RideID,
			PassengerID:   b.PassengerID,
			RequestStatus: b.RequestStatus,
		}
	}

	return c.Status(200).JSON(bookingsResponse)
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

	bookingID, err := uuid.Parse(idParam)
	if err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid booking ID format"}
	}

	var booking models.Booking
	err = database.Database.Db.First(&booking, "id = ?", bookingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).SendString("Booking not found")
		}
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
	}

	// Perform the deletion
	if err := database.Database.Db.Delete(&booking).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}

	log.Printf("Booking with id %v deleted\n", booking.ID)
	return c.SendStatus(204)
}
