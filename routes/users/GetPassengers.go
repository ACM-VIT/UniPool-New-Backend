package users

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// GetPassengers returns a list of users the authenticated user has travelled with
func GetPassengers(c *fiber.Ctx) error {

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

	var passengerIDs []string
	query := `SELECT DISTINCT b.passenger_id FROM bookings b
		JOIN rides r ON b.ride_id = r.id
		WHERE r.host_user_id = ? OR b.passenger_id = ?`
	if err := database.Database.Db.Raw(query, user.ID, user.ID).Scan(&passengerIDs).Error; err != nil {
		log.Printf("Error finding passengers: %v\n", err)
		return c.Status(fiber.StatusBadGateway).SendString("Error finding passengers")
	}

	var passengers []models.User
	if len(passengerIDs) > 0 {
		if err := database.Database.Db.Where("id IN ? AND id != ?", passengerIDs, user.ID).Find(&passengers).Error; err != nil {
			log.Printf("Error fetching passenger users: %v\n", err)
			return c.Status(fiber.StatusBadGateway).SendString("Error fetching passenger users")
		}
	} else {
		passengers = []models.User{} // to ensure we do not just return nil
	}

	log.Printf("Returning %d passengers for user %v\n", len(passengers), user.ID)
	for _, p := range passengers {
		log.Printf("Passenger: ID=%v, Name=%v, Email=%v\n", p.ID, p.Name, p.Email)
	}
	return c.Status(fiber.StatusOK).JSON(passengers)
}
