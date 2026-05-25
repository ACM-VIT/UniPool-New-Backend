package users

import (
	"errors"
	"log"
	"strings"
	"time"
	"unipool-backend/database"
	"unipool-backend/middleware"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func DeleteUser(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   true,
			"message": "User data not found in locals",
		})
	}

	tx := database.Database.Db.Begin()
	if tx.Error != nil {
		log.Println("Error starting transaction:", tx.Error)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error starting deletion process",
		})
	}

	if err := handleHostedRides(tx, user.ID); err != nil {
		tx.Rollback()
		log.Println("Error handling hosted rides:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error transferring ride ownership: " + err.Error(),
		})
	}

	if err := handlePassengerBookings(tx, user.ID); err != nil {
		tx.Rollback()
		log.Println("Error handling passenger bookings:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error canceling your ride bookings: " + err.Error(),
		})
	}

	if err := handleUserMessages(tx, user.ID); err != nil {
		tx.Rollback()
		log.Println("Error handling user messages:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error updating chat messages: " + err.Error(),
		})
	}

	if err := handleUserMetadata(tx, user.ID); err != nil {
		tx.Rollback()
		log.Println("Error handling user metadata:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error deleting user metadata: " + err.Error(),
		})
	}

	// Finally delete the user
	if err := tx.Delete(&user).Error; err != nil {
		tx.Rollback()
		log.Println("Error deleting user:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error deleting user account",
		})
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		log.Println("Error committing transaction:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Error completing user deletion",
		})
	}
	middleware.InvalidateAuthUserCacheByEmail(user.Email)

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message": "User account deleted successfully",
	})
}

type hostedRideTransfer struct {
	RideID    uuid.UUID
	NewHostID uuid.UUID
}

func handleHostedRides(tx *gorm.DB, userID uuid.UUID) error {
	type hostedRideRow struct {
		ID        uuid.UUID `gorm:"column:id"`
		IsOngoing uint      `gorm:"column:is_ongoing"`
	}
	var hostedRides []hostedRideRow

	if err := tx.Model(&models.Ride{}).
		Select("id, is_ongoing").
		Where("host_user_id = ?", userID).
		Find(&hostedRides).Error; err != nil {
		return err
	}
	if len(hostedRides) == 0 {
		return nil
	}

	rideIDs := make([]uuid.UUID, 0, len(hostedRides))
	for _, ride := range hostedRides {
		if ride.IsOngoing > 0 {
			return errors.New("cannot delete account while hosting an ongoing ride")
		}
		rideIDs = append(rideIDs, ride.ID)
	}

	type acceptedBookingRow struct {
		RideID      uuid.UUID `gorm:"column:ride_id"`
		PassengerID uuid.UUID `gorm:"column:passenger_id"`
	}
	var acceptedBookings []acceptedBookingRow
	if err := tx.Model(&models.Booking{}).
		Select("ride_id, passenger_id").
		Where("ride_id IN ? AND request_status = ?", rideIDs, "accepted").
		Order("created_at ASC").
		Find(&acceptedBookings).Error; err != nil {
		return err
	}
	firstAcceptedByRide := make(map[uuid.UUID]uuid.UUID, len(acceptedBookings))
	for _, booking := range acceptedBookings {
		if _, exists := firstAcceptedByRide[booking.RideID]; !exists {
			firstAcceptedByRide[booking.RideID] = booking.PassengerID
		}
	}

	transfers := make([]hostedRideTransfer, 0, len(firstAcceptedByRide))
	noPassengerRideIDs := make([]uuid.UUID, 0, len(hostedRides))
	for _, ride := range hostedRides {
		if newHostID, ok := firstAcceptedByRide[ride.ID]; ok {
			transfers = append(transfers, hostedRideTransfer{RideID: ride.ID, NewHostID: newHostID})
		} else {
			noPassengerRideIDs = append(noPassengerRideIDs, ride.ID)
		}
	}

	if len(transfers) > 0 {
		if err := batchTransferHostedRides(tx, transfers); err != nil {
			return err
		}
		if err := batchDeletePromotedBookings(tx, transfers); err != nil {
			return err
		}
		log.Printf("Transferred %d hosted rides from user %s", len(transfers), userID)
	}

	if len(noPassengerRideIDs) > 0 {
		if err := tx.Where("ride_id IN ?", noPassengerRideIDs).Delete(&models.Booking{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Ride{}, "id IN ?", noPassengerRideIDs).Error; err != nil {
			return err
		}
		log.Printf("Deleted %d hosted rides with no accepted passengers for user %s", len(noPassengerRideIDs), userID)
	}

	return nil
}

func batchTransferHostedRides(tx *gorm.DB, transfers []hostedRideTransfer) error {
	caseClauses := make([]string, 0, len(transfers))
	rideIDs := make([]uuid.UUID, 0, len(transfers))
	args := make([]any, 0, len(transfers)*2+2)
	for _, transfer := range transfers {
		caseClauses = append(caseClauses, "WHEN id = ? THEN ?")
		args = append(args, transfer.RideID, transfer.NewHostID)
		rideIDs = append(rideIDs, transfer.RideID)
	}
	args = append(args, time.Now(), rideIDs)
	return tx.Exec(`
		UPDATE rides
		   SET host_user_id = CASE `+strings.Join(caseClauses, " ")+` ELSE host_user_id END,
		       updated_at = ?
		 WHERE deleted_at IS NULL
		   AND id IN ?
	`, args...).Error
}

func batchDeletePromotedBookings(tx *gorm.DB, transfers []hostedRideTransfer) error {
	pairs := make([]string, 0, len(transfers))
	args := make([]any, 0, len(transfers)*2+2)
	now := time.Now()
	args = append(args, now, now)
	for _, transfer := range transfers {
		pairs = append(pairs, "(?, ?)")
		args = append(args, transfer.RideID, transfer.NewHostID)
	}
	return tx.Exec(`
		UPDATE bookings
		   SET updated_at = ?,
		       deleted_at = ?
		 WHERE deleted_at IS NULL
		   AND (ride_id, passenger_id) IN (`+strings.Join(pairs, ", ")+`)
	`, args...).Error
}

func handlePassengerBookings(tx *gorm.DB, userID uuid.UUID) error {
	now := time.Now()
	var hasRideStartingSoon bool
	if err := tx.Raw(`
		SELECT EXISTS (
			SELECT 1
			  FROM bookings b
			  JOIN rides r ON r.id = b.ride_id
			 WHERE b.deleted_at IS NULL
			   AND r.deleted_at IS NULL
			   AND b.passenger_id = ?
			   AND b.request_status = ?
			   AND r.start_time > ?
			   AND r.start_time < ?
			   AND r.is_ongoing = ?
			 LIMIT 1
		)
	`, userID, "accepted", now, now.Add(24*time.Hour), 0).Scan(&hasRideStartingSoon).Error; err != nil {
		return err
	}
	if hasRideStartingSoon {
		return errors.New("cannot delete account with confirmed rides starting within 24 hours")
	}

	deleted := tx.Where("passenger_id = ?", userID).Delete(&models.Booking{})
	if deleted.Error != nil {
		return deleted.Error
	}

	log.Printf("Deleted %d passenger bookings for user %s", deleted.RowsAffected, userID)
	return nil
}

func handleUserMessages(tx *gorm.DB, userID uuid.UUID) error {
	// Option 1: Delete all messages sent by the user
	// if err := tx.Where("sender_id = ?", userID).Delete(&models.Message{}).Error; err != nil {
	//     return err
	// }

	// Option 2: Keep messages but mark sender as deleted (recommended for chat history)
	// Create a "deleted user" placeholder if it doesn't exist
	deletedUser := models.User{
		BaseModel:     models.BaseModel{ID: uuid.New()},
		Name:          "Deleted User",
		Email:         "deleted@unipool.com",
		ContactNumber: "0000000000",
	}

	var existingDeletedUser models.User
	err := tx.Where("email = ?", "deleted@unipool.com").First(&existingDeletedUser).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&deletedUser).Error; err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		deletedUser = existingDeletedUser
	}

	if err := tx.Model(&models.Message{}).Where("sender_id = ?", userID).
		Update("sender_id", deletedUser.ID).Error; err != nil {
		return err
	}

	log.Printf("Updated messages for deleted user %s", userID)
	return nil
}

func handleUserMetadata(tx *gorm.DB, userID uuid.UUID) error {
	// Delete user metadata records
	if err := tx.Where("user_id = ?", userID).Delete(&models.UserMetadata{}).Error; err != nil {
		return err
	}

	log.Printf("Deleted user metadata for user %s", userID)
	return nil
}
