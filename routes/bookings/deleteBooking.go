package bookings

import (
	"errors"
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

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
			return c.Status(404).JSON(fiber.Map{
				"success": false,
				"error": "Booking not found",
				"booking_id": bookingID.String(),
			})
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
	return c.Status(200).JSON(fiber.Map{
		"success": true,
		"message": "Booking deleted successfully",
		"booking_id": booking.ID.String(),
	})
}
