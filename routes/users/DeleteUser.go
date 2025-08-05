package users

import (
	"errors"
	"log"
	"time"
	"unipool-backend/database"
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

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message": "User account deleted successfully",
	})
}

func handleHostedRides(tx *gorm.DB, userID uuid.UUID) error {
	var hostedRides []models.Ride
	
	if err := tx.Where("host_user_id = ?", userID).Find(&hostedRides).Error; err != nil {
		return err
	}

	for _, ride := range hostedRides {
		if ride.IsOngoing > 0 {
			return errors.New("cannot delete account while hosting an ongoing ride")
		}
		
		// if ride.StartTime.Before(time.Now().Add(24 * time.Hour)) && ride.StartTime.After(time.Now()) {
		// 	return errors.New("cannot delete account with rides starting within 24 hours")
		// }

		var acceptedBookings []models.Booking
		if err := tx.Where("ride_id = ? AND request_status = ?", ride.ID, "accepted").
			Find(&acceptedBookings).Error; err != nil {
			return err
		}

		if len(acceptedBookings) > 0 {
			newHostID := acceptedBookings[0].PassengerID
			
			if err := tx.Model(&ride).Update("host_user_id", newHostID).Error; err != nil {
				return err
			}

			if err := tx.Where("ride_id = ? AND passenger_id = ?", ride.ID, newHostID).
				Delete(&models.Booking{}).Error; err != nil {
				return err
			}

			log.Printf("Transferred ride %s ownership from %s to %s", ride.ID, userID, newHostID)
		} else {
			if err := tx.Where("ride_id = ?", ride.ID).Delete(&models.Booking{}).Error; err != nil {
				return err
			}
			
			if err := tx.Delete(&ride).Error; err != nil {
				return err
			}

			log.Printf("Deleted ride %s (no passengers)", ride.ID)
		}
	}

	return nil
}

func handlePassengerBookings(tx *gorm.DB, userID uuid.UUID) error {
	var upcomingAcceptedBookings []models.Booking
	if err := tx.Joins("JOIN rides ON bookings.ride_id = rides.id").
		Where("bookings.passenger_id = ? AND bookings.request_status = ? AND rides.start_time > ? AND rides.is_ongoing = ?", 
			userID, "accepted", time.Now(), 0).
		Find(&upcomingAcceptedBookings).Error; err != nil {
		return err
	}

	for _, booking := range upcomingAcceptedBookings {
		var ride models.Ride
		if err := tx.Where("id = ?", booking.RideID).First(&ride).Error; err != nil {
			continue
		}
		
		if ride.StartTime.Before(time.Now().Add(24 * time.Hour)) && ride.StartTime.After(time.Now()) {
			return errors.New("cannot delete account with confirmed rides starting within 24 hours")
		}
	}

	if err := tx.Where("passenger_id = ?", userID).Delete(&models.Booking{}).Error; err != nil {
		return err
	}

	log.Printf("Deleted %d passenger bookings for user %s", len(upcomingAcceptedBookings), userID)
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
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Deleted User",
		Email:     "deleted@unipool.com",
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
