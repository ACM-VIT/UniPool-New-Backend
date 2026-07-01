package bookings

import (
	"errors"
	"log"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

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

	type bookingRideRow struct {
		BookingID      uuid.UUID `gorm:"column:booking_id"`
		RideID         uuid.UUID `gorm:"column:ride_id"`
		PassengerID    uuid.UUID `gorm:"column:passenger_id"`
		RequestStatus  string    `gorm:"column:request_status"`
		HostUserID     uuid.UUID `gorm:"column:host_user_id"`
		StartLocation  string    `gorm:"column:start_location"`
		EndLocation    string    `gorm:"column:end_location"`
		StartTime      time.Time `gorm:"column:start_time"`
		HostName       string    `gorm:"column:host_name"`
		HostEmail      string    `gorm:"column:host_email"`
		PassengerName  string    `gorm:"column:passenger_name"`
		PassengerEmail string    `gorm:"column:passenger_email"`
	}

	var row bookingRideRow
	err = tx.Raw(`
		SELECT
			b.id AS booking_id,
			b.ride_id,
			b.passenger_id,
			b.request_status,
			r.host_user_id,
			r.start_location,
			r.end_location,
			r.start_time,
			h.name AS host_name,
			h.email AS host_email,
			p.name AS passenger_name,
			p.email AS passenger_email
		  FROM bookings b
		  JOIN rides r ON r.id = b.ride_id
		  JOIN users h ON h.id = r.host_user_id
		  JOIN users p ON p.id = b.passenger_id
		 WHERE b.id = ?
		   AND b.deleted_at IS NULL
		   AND r.deleted_at IS NULL
		 LIMIT 1
	`, bookingID).Scan(&row).Error
	if err != nil || row.BookingID == (uuid.UUID{}) {
		tx.Rollback()
		if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).JSON(fiber.Map{
				"success":    false,
				"error":      "Booking not found",
				"booking_id": bookingID.String(),
			})
		}
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding booking"}
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

	// Passenger-initiated withdrawal of an accepted booking: notify
	// the host so they don't keep counting on the seat being filled.
	// Three guards keep the noise low:
	//   1. Only when the user removing the booking is the passenger
	//      themselves (host-initiated removes are handled below).
	//   2. Only when the booking was actually accepted. Withdrawing
	//      a still-pending request shouldn't ping anyone (the host
	//      hadn't decided yet).
	//   3. Best-effort, async, never blocks the response.
	if booking.PassengerID == user.ID && booking.RequestStatus == "accepted" {
		fcmService := services.GetFCMService()
		if fcmService != nil {
			passengerName := user.Name
			rideRoute := ride.StartLocation + " to " + ride.EndLocation
			rideID := ride.ID
			bookingID := booking.ID
			passengerID := user.ID
			hostUserID := ride.HostUserID
			go func() {
				if err := fcmService.SendBookingWithdrawnNotification(
					hostUserID,
					passengerID,
					passengerName,
					rideRoute,
					rideID,
					bookingID,
				); err != nil {
					log.Printf("Error sending booking withdrawn notification: %v", err)
				}
			}()
		}
		go sendBookingEmailIfAllowed(ride.HostUserID, helpers.BookingEmailParams{
			Kind:          helpers.BookingEmailWithdrawn,
			ToEmail:       row.HostEmail,
			ToName:        row.HostName,
			ActorName:     user.Name,
			StartLocation: row.StartLocation,
			EndLocation:   row.EndLocation,
			StartTime:     row.StartTime,
			RideID:        ride.ID.String(),
		})
	}

	// Host-initiated removal of an accepted passenger: notify the
	// passenger they lost their seat. Same posture as the passenger-
	// withdrew branch above — async + best-effort. Pending-request
	// rejections go through a different endpoint (/bookings/reject)
	// which already pings via SendBookingRejectedNotification, so
	// gate on accepted-only here.
	if ride.HostUserID == user.ID && booking.PassengerID != user.ID && booking.RequestStatus == "accepted" {
		fcmService := services.GetFCMService()
		if fcmService != nil {
			rideRoute := ride.StartLocation + " to " + ride.EndLocation
			rideID := ride.ID
			bookingID := booking.ID
			passengerID := booking.PassengerID
			go func() {
				if err := fcmService.SendBookingRemovedByHostNotification(
					passengerID,
					rideRoute,
					rideID,
					bookingID,
				); err != nil {
					log.Printf("Error sending booking removed-by-host notification: %v", err)
				}
			}()
		}
		go sendBookingEmailIfAllowed(booking.PassengerID, helpers.BookingEmailParams{
			Kind:          helpers.BookingEmailRemoved,
			ToEmail:       row.PassengerEmail,
			ToName:        row.PassengerName,
			ActorName:     user.Name,
			StartLocation: row.StartLocation,
			EndLocation:   row.EndLocation,
			StartTime:     row.StartTime,
			RideID:        ride.ID.String(),
		})
	}

	log.Printf("Booking with id %v deleted\n", booking.ID)
	return c.Status(200).JSON(fiber.Map{
		"success":    true,
		"message":    "Booking deleted successfully",
		"booking_id": booking.ID.String(),
	})
}
