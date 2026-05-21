package bookings

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
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

	// Retrieve the booking
	// Ensure the bookingID is being treated as UUID in the query
	var booking models.Booking
	if err := tx.First(&booking, "id = ?", bookingID).Error; err != nil {
		log.Printf("Error finding booking with ID %v: %v\n", bookingID, err)
		tx.Rollback()
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Booking not found",
			"booking_id": bookingID,
		})
	}

	var ride models.Ride
	if err := tx.First(&ride, booking.RideID).Error; err != nil {
		log.Printf("Error finding ride with ID %v: %v\n", booking.RideID, err)
		tx.Rollback()
		return c.Status(404).JSON(fiber.Map{
			"success":    false,
			"error":      "Ride not found",
			"booking_id": bookingID,
		})
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
		Where("id = ? AND booked_seats < total_seats", ride.ID).
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
