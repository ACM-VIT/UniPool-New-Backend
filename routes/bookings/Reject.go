package bookings

import (
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func RejectRoute(c *fiber.Ctx) error {
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
		BookingID      uuid.UUID `gorm:"column:booking_id"`
		RideID         uuid.UUID `gorm:"column:ride_id"`
		PassengerID    uuid.UUID `gorm:"column:passenger_id"`
		RequestStatus  string    `gorm:"column:request_status"`
		HostUserID     uuid.UUID `gorm:"column:host_user_id"`
		StartLocation  string    `gorm:"column:start_location"`
		EndLocation    string    `gorm:"column:end_location"`
		StartTime      time.Time `gorm:"column:start_time"`
		PassengerName  string    `gorm:"column:passenger_name"`
		PassengerEmail string    `gorm:"column:passenger_email"`
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
			r.end_location,
			r.start_time,
			p.name AS passenger_name,
			p.email AS passenger_email
		  FROM bookings b
		  JOIN rides r ON r.id = b.ride_id
		  JOIN users p ON p.id = b.passenger_id
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
		log.Printf("User %v is not authorized to reject bookings for ride %v (host: %v)\n", user.ID, ride.ID, ride.HostUserID)
		tx.Rollback()
		return c.Status(403).JSON(fiber.Map{
			"success":    false,
			"error":      "Only the ride host can reject booking requests",
			"booking_id": bookingID,
		})
	}

	if booking.RequestStatus == "rejected" {
		tx.Rollback()
		return c.Status(200).JSON(fiber.Map{
			"success":    true,
			"message":    "Booking already rejected",
			"booking_id": bookingID,
			"booking":    booking,
		})
	}
	if booking.RequestStatus != "pending" {
		tx.Rollback()
		return c.Status(409).JSON(fiber.Map{
			"success":    false,
			"error":      "Only pending bookings can be rejected",
			"booking_id": bookingID,
		})
	}

	// Only pending rows can transition to rejected. In particular,
	// an accepted booking must be removed/cancelled through the delete
	// path so booked_seats is decremented in the same transaction.
	update := tx.Model(&models.Booking{}).
		Where("id = ? AND request_status = ?", booking.ID, "pending").
		Update("request_status", "rejected")
	if update.Error != nil {
		log.Printf("Error updating booking status for ID %v: %v\n", bookingID, update.Error)
		tx.Rollback()
		return c.Status(500).JSON(fiber.Map{
			"success":    false,
			"error":      "Error updating booking status",
			"booking_id": bookingID,
		})
	}
	if update.RowsAffected == 0 {
		tx.Rollback()
		return c.Status(409).JSON(fiber.Map{
			"success":    false,
			"error":      "Booking is no longer pending",
			"booking_id": bookingID,
		})
	}
	booking.RequestStatus = "rejected"

	// Commit the transaction
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
			if err := fcmService.SendBookingRejectedNotification(booking.PassengerID, rideRoute, ride.ID, booking.ID); err != nil {
				log.Printf("Error sending booking rejected notification: %v", err)
			}
		}()
	}
	go sendBookingEmailIfAllowed(booking.PassengerID, helpers.BookingEmailParams{
		Kind:          helpers.BookingEmailRejected,
		ToEmail:       row.PassengerEmail,
		ToName:        row.PassengerName,
		ActorName:     user.Name,
		StartLocation: row.StartLocation,
		EndLocation:   row.EndLocation,
		StartTime:     row.StartTime,
		RideID:        ride.ID.String(),
	})

	log.Printf("Booking with ID %v rejected successfully\n", bookingID)
	return c.Status(200).JSON(fiber.Map{
		"success":    true,
		"message":    "Booking rejected successfully",
		"booking_id": bookingID,
		"booking":    booking,
	})
}
