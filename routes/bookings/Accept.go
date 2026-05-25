package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func AcceptRoute(c *fiber.Ctx) error {
	bookingID := c.Params("bookingID")

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

	// Start a database transaction
	tx := database.Database.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	type bookingRideRow struct {
		BookingID     uuid.UUID `gorm:"column:booking_id"`
		RideID        uuid.UUID `gorm:"column:ride_id"`
		PassengerID   uuid.UUID `gorm:"column:passenger_id"`
		RequestStatus string    `gorm:"column:request_status"`
		HostUserID    uuid.UUID `gorm:"column:host_user_id"`
		StartLocation string    `gorm:"column:start_location"`
		EndLocation   string    `gorm:"column:end_location"`
	}

	var row bookingRideRow
	if err := tx.Raw(`
		SELECT
			b.id AS booking_id,
			b.ride_id,
			b.passenger_id,
			b.request_status,
			r.host_user_id,
			r.start_location,
			r.end_location
		  FROM bookings b
		  JOIN rides r ON r.id = b.ride_id
		 WHERE b.id = ?
		   AND b.deleted_at IS NULL
		   AND r.deleted_at IS NULL
		 LIMIT 1
	`, bookingID).Scan(&row).Error; err != nil || row.BookingID == (uuid.UUID{}) {
		log.Printf("Error finding booking with ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Booking not found",
			"booking_id": bookingID,
		})
	}

	booking := models.Booking{
		BaseModel:     models.BaseModel{ID: row.BookingID},
		RideID:        row.RideID,
		PassengerID:   row.PassengerID,
		RequestStatus: row.RequestStatus,
	}
	ride := models.Ride{
		BaseModel:     models.BaseModel{ID: row.RideID},
		HostUserID:    row.HostUserID,
		StartLocation: row.StartLocation,
		EndLocation:   row.EndLocation,
	}

	if ride.HostUserID != user.ID {
		log.Printf("User %v is not authorized to accept bookings for ride %v (host: %v)\n", user.ID, ride.ID, ride.HostUserID)
		tx.Rollback()
		return c.Status(403).JSON(fiber.Map{
			"success":    false,
			"error":      "Only the ride host can accept booking requests",
			"booking_id": bookingID,
		})
	}

	if booking.RequestStatus == "accepted" {
		tx.Rollback()
		return c.Status(200).JSON(fiber.Map{
			"success":    true,
			"message":    "Booking already accepted",
			"booking_id": bookingID,
			"booking":    booking,
		})
	}
	if booking.RequestStatus != "pending" {
		tx.Rollback()
		return c.Status(409).JSON(fiber.Map{
			"success":    false,
			"error":      "Only pending bookings can be accepted",
			"booking_id": bookingID,
		})
	}

	// Atomically reserve a seat. The WHERE clause is rechecked under
	// the row lock, so concurrent accepts cannot overbook the ride.
	seatUpdate := tx.Model(&models.Ride{}).
		// Source of truth for the seat-availability gate lives in
		// helpers/seats.go. The predicate compiles to
		// "booked_seats < total_seats - 1" — the `- 1` discounts
		// the host's seat from total_seats.
		Where("id = ? AND "+helpers.PassengerSeatsLeftPredicate, ride.ID).
		Update("booked_seats", gorm.Expr("booked_seats + 1"))
	if seatUpdate.Error != nil {
		log.Printf("Error updating booked seats for ride with ID %v: %v\n", ride.ID, seatUpdate.Error)
		tx.Rollback()
		return c.Status(500).JSON(fiber.Map{
			"success":    false,
			"error":      "Error updating booked seats for ride",
			"booking_id": bookingID,
		})
	}
	if seatUpdate.RowsAffected == 0 {
		log.Printf("No available seats for ride with ID %v\n", booking.RideID)
		tx.Rollback()
		return c.Status(400).JSON(fiber.Map{
			"success":    false,
			"error":      "No available seats for this ride",
			"booking_id": bookingID,
		})
	}

	// Only the first accept for a pending booking may transition the
	// row. If another request changed it while this transaction was
	// waiting, roll back the seat reservation above.
	bookingUpdate := tx.Model(&models.Booking{}).
		Where("id = ? AND request_status = ?", booking.ID, "pending").
		Update("request_status", "accepted")
	if bookingUpdate.Error != nil {
		log.Printf("Error updating booking status for ID %v: %v\n", bookingID, bookingUpdate.Error)
		tx.Rollback()
		return c.Status(500).JSON(fiber.Map{
			"success":    false,
			"error":      "Error updating booking status",
			"booking_id": bookingID,
		})
	}
	if bookingUpdate.RowsAffected == 0 {
		tx.Rollback()
		return c.Status(409).JSON(fiber.Map{
			"success":    false,
			"error":      "Booking is no longer pending",
			"booking_id": bookingID,
		})
	}
	booking.RequestStatus = "accepted"

	// Commit the transaction after all successful updates
	if err := tx.Commit().Error; err != nil {
		log.Printf("Error committing transaction: %v\n", err)
		return c.Status(500).JSON(fiber.Map{
			"success":    false,
			"error":      "Error committing transaction",
			"booking_id": bookingID,
		})
	}

	// Send FCM notification to the passenger
	fcmService := services.GetFCMService()
	if fcmService != nil {
		rideRoute := ride.StartLocation + " to " + ride.EndLocation
		go func() {
			if err := fcmService.SendBookingAcceptedNotification(booking.PassengerID, rideRoute, ride.ID, booking.ID); err != nil {
				log.Printf("Error sending booking accepted notification: %v", err)
			}
		}()
	}

	log.Printf("Booking with ID %v accepted successfully\n", bookingID)
	return c.Status(200).JSON(fiber.Map{
		"success":    true,
		"message":    "Booking accepted successfully",
		"booking_id": bookingID,
		"booking":    booking,
	})
}
