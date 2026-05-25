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

	type rideSummary struct {
		ID                        string    `json:"id"`
		RideID                    string    `json:"ride_id"`
		HostUserID                string    `json:"host_user_id"`
		HostUserName              string    `json:"host_user_name,omitempty"`
		HostUserProfilePictureURL string    `json:"host_user_profile_picture_url,omitempty"`
		StartLocation             string    `json:"start_location"`
		EndLocation               string    `json:"end_location"`
		StartTime                 time.Time `json:"start_time"`
		TotalSeats                uint      `json:"total_seats"`
		BookedSeats               uint      `json:"booked_seats"`
		TotalPrice                uint      `json:"total_price"`
		IsOngoing                 uint      `json:"is_ongoing"`
		IsSameGender              uint      `json:"is_same_gender"`
	}

	type bookingRow struct {
		ID                        uuid.UUID
		RideID                    uuid.UUID
		PassengerID               uuid.UUID
		RequestStatus             string
		HostUserID                uuid.UUID
		HostUserName              string
		HostUserProfilePictureURL string
		StartLocation             string
		EndLocation               string
		StartTime                 time.Time
		TotalSeats                uint
		BookedSeats               uint
		TotalPrice                uint
		IsOngoing                 uint
		IsSameGender              uint
	}

	var rows []bookingRow
	if err := database.Database.Db.
		Table("bookings AS b").
		Select(`
			b.id,
			b.ride_id,
			b.passenger_id,
			b.request_status,
			r.host_user_id,
			h.name AS host_user_name,
			h.profile_picture_url AS host_user_profile_picture_url,
			r.start_location,
			r.end_location,
			r.start_time,
			r.total_seats,
			r.booked_seats,
			r.total_price,
			r.is_ongoing,
			r.is_same_gender
		`).
		Joins("JOIN rides r ON r.id = b.ride_id").
		Joins("LEFT JOIN users h ON h.id = r.host_user_id").
		Where("b.passenger_id = ?", userUUID).
		Order("r.start_time ASC").
		Scan(&rows).Error; err != nil {
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

	bookingsResponse := make([]BookingWithRideDetails, len(rows))
	for i, b := range rows {
		bookingsResponse[i] = BookingWithRideDetails{
			ID:            b.ID.String(),
			RideID:        b.RideID.String(),
			PassengerID:   b.PassengerID.String(),
			RequestStatus: b.RequestStatus,
			RideDetails: rideSummary{
				ID:                        b.RideID.String(),
				RideID:                    b.RideID.String(),
				HostUserID:                b.HostUserID.String(),
				HostUserName:              b.HostUserName,
				HostUserProfilePictureURL: b.HostUserProfilePictureURL,
				StartLocation:             b.StartLocation,
				EndLocation:               b.EndLocation,
				StartTime:                 b.StartTime,
				TotalSeats:                b.TotalSeats,
				BookedSeats:               b.BookedSeats,
				TotalPrice:                b.TotalPrice,
				IsOngoing:                 b.IsOngoing,
				IsSameGender:              b.IsSameGender,
			},
		}
	}

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

	type bookingRideRow struct {
		BookingID        uuid.UUID           `gorm:"column:booking_id"`
		RideID           uuid.UUID           `gorm:"column:ride_id"`
		PassengerID      uuid.UUID           `gorm:"column:passenger_id"`
		RequestStatus    string              `gorm:"column:request_status"`
		RideCreatedAt    time.Time           `gorm:"column:ride_created_at"`
		RideUpdatedAt    time.Time           `gorm:"column:ride_updated_at"`
		HostUserID       uuid.UUID           `gorm:"column:host_user_id"`
		StartLocation    string              `gorm:"column:start_location"`
		EndLocation      string              `gorm:"column:end_location"`
		StartLatitude    *float64            `gorm:"column:start_latitude"`
		StartLongitude   *float64            `gorm:"column:start_longitude"`
		EndLatitude      *float64            `gorm:"column:end_latitude"`
		EndLongitude     *float64            `gorm:"column:end_longitude"`
		StartTime        time.Time           `gorm:"column:start_time"`
		TotalSeats       uint                `gorm:"column:total_seats"`
		BookedSeats      uint                `gorm:"column:booked_seats"`
		TotalPrice       uint                `gorm:"column:total_price"`
		IsOngoing        uint                `gorm:"column:is_ongoing"`
		IsSameGender     uint                `gorm:"column:is_same_gender"`
		Settings         models.RideSettings `gorm:"column:settings"`
		VehicleInfo      string              `gorm:"column:vehicle_info"`
		UpdateNotifStamp *time.Time          `gorm:"column:update_notif_last_sent_at"`
	}
	var rows []bookingRideRow
	if err := database.Database.Db.WithContext(ctx).
		Table("bookings AS b").
		Select(`
			b.id AS booking_id,
			b.ride_id,
			b.passenger_id,
			b.request_status,
			r.created_at AS ride_created_at,
			r.updated_at AS ride_updated_at,
			r.host_user_id,
			r.start_location,
			r.end_location,
			r.start_latitude,
			r.start_longitude,
			r.end_latitude,
			r.end_longitude,
			r.start_time,
			r.total_seats,
			r.booked_seats,
			r.total_price,
			r.is_ongoing,
			r.is_same_gender,
			r.settings,
			r.vehicle_info,
			r.update_notif_last_sent_at
		`).
		Joins("JOIN rides r ON r.id = b.ride_id AND r.deleted_at IS NULL").
		Where("b.ride_id = ? AND b.deleted_at IS NULL", rideID).
		Scan(&rows).Error; err != nil {
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

	bookingsResponse := make([]BookingWithRideDetails, len(rows))
	for i, b := range rows {
		ride := models.Ride{
			BaseModel: models.BaseModel{
				ID:        b.RideID,
				CreatedAt: b.RideCreatedAt,
				UpdatedAt: b.RideUpdatedAt,
			},
			HostUserID:            b.HostUserID,
			StartLocation:         b.StartLocation,
			EndLocation:           b.EndLocation,
			StartLatitude:         b.StartLatitude,
			StartLongitude:        b.StartLongitude,
			EndLatitude:           b.EndLatitude,
			EndLongitude:          b.EndLongitude,
			StartTime:             b.StartTime,
			TotalSeats:            b.TotalSeats,
			BookedSeats:           b.BookedSeats,
			TotalPrice:            b.TotalPrice,
			IsOngoing:             b.IsOngoing,
			IsSameGender:          b.IsSameGender,
			Settings:              b.Settings,
			VehicleInfo:           b.VehicleInfo,
			UpdateNotifLastSentAt: b.UpdateNotifStamp,
		}
		bookingsResponse[i] = BookingWithRideDetails{
			ID:            b.BookingID.String(),
			RideID:        b.RideID.String(),
			PassengerID:   b.PassengerID.String(),
			RequestStatus: b.RequestStatus,
			RideDetails:   ride,
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

	type bookingAuthRow struct {
		BookingID     uuid.UUID  `gorm:"column:booking_id"`
		RideID        uuid.UUID  `gorm:"column:ride_id"`
		PassengerID   uuid.UUID  `gorm:"column:passenger_id"`
		RequestStatus string     `gorm:"column:request_status"`
		HostUserID    *uuid.UUID `gorm:"column:host_user_id"`
	}
	var existing bookingAuthRow
	if err := database.Database.Db.
		Table("bookings AS b").
		Select(`
			b.id AS booking_id,
			b.ride_id,
			b.passenger_id,
			b.request_status,
			r.host_user_id
		`).
		Joins("LEFT JOIN rides r ON r.id = b.ride_id AND r.deleted_at IS NULL").
		Where("b.id = ? AND b.deleted_at IS NULL", bookingID).
		Scan(&existing).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
	}
	if existing.BookingID == (uuid.UUID{}) {
		return c.Status(404).SendString("Booking not found")
	}
	if existing.HostUserID == nil {
		log.Printf("Error finding ride with ID %v for booking %v\n", existing.RideID, bookingID)
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Ride not found",
			"booking_id": bookingID.String(),
		})
	}

	if *existing.HostUserID != user.ID && existing.PassengerID != user.ID {
		log.Printf("User %v is not authorized to update booking %v (host: %v, passenger: %v)\n", user.ID, existing.BookingID, *existing.HostUserID, existing.PassengerID)
		return c.Status(403).JSON(fiber.Map{
			"success":    false,
			"error":      "Only the ride host or the passenger can update this booking",
			"booking_id": bookingID.String(),
		})
	}

	updated := false

	// Update fields if changed
	nextStatus := existing.RequestStatus
	if bookingPayload.RequestStatus != "" && bookingPayload.RequestStatus != existing.RequestStatus {
		nextStatus = bookingPayload.RequestStatus
		updated = true
	}

	if updated {
		if err := database.Database.Db.
			Model(&models.Booking{}).
			Where("id = ?", existing.BookingID).
			Update("request_status", nextStatus).Error; err != nil {
			log.Println(err)
			return &fiber.Error{Code: 500, Message: "Database error"}
		}

		log.Printf("Booking with id %v updated\n", existing.BookingID)
		bookingResponse := BookingResponse{
			ID:            existing.BookingID,
			RideID:        existing.RideID,
			PassengerID:   existing.PassengerID,
			RequestStatus: nextStatus,
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

	type deleteBookingRow struct {
		BookingID     uuid.UUID  `gorm:"column:booking_id"`
		RideID        uuid.UUID  `gorm:"column:ride_id"`
		PassengerID   uuid.UUID  `gorm:"column:passenger_id"`
		RequestStatus string     `gorm:"column:request_status"`
		HostUserID    *uuid.UUID `gorm:"column:host_user_id"`
	}
	var booking deleteBookingRow
	err = tx.
		Table("bookings AS b").
		Select(`
			b.id AS booking_id,
			b.ride_id,
			b.passenger_id,
			b.request_status,
			r.host_user_id
		`).
		Joins("LEFT JOIN rides r ON r.id = b.ride_id AND r.deleted_at IS NULL").
		Where("b.id = ? AND b.deleted_at IS NULL", bookingID).
		Scan(&booking).Error
	if err != nil {
		tx.Rollback()
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
	}
	if booking.BookingID == (uuid.UUID{}) {
		tx.Rollback()
		return c.Status(404).SendString("Booking not found")
	}
	if booking.HostUserID == nil {
		tx.Rollback()
		log.Printf("Error finding ride with ID %v for booking %v\n", booking.RideID, bookingID)
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Ride not found",
			"booking_id": bookingID.String(),
		})
	}

	if *booking.HostUserID != user.ID && booking.PassengerID != user.ID {
		tx.Rollback()
		log.Printf("User %v is not authorized to delete booking %v (host: %v, passenger: %v)\n", user.ID, booking.BookingID, *booking.HostUserID, booking.PassengerID)
		return c.Status(403).JSON(fiber.Map{
			"success":    false,
			"error":      "Only the ride host or the passenger can delete this booking",
			"booking_id": bookingID.String(),
		})
	}

	// Perform the deletion
	if err := tx.Delete(&models.Booking{}, "id = ?", booking.BookingID).Error; err != nil {
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

	log.Printf("Booking with id %v deleted\n", booking.BookingID)
	return c.SendStatus(204)
}
