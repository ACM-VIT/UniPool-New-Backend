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

	var passengers []models.User
	if err := database.Database.Db.Raw(`
		WITH travelled_rides AS (
			SELECT r.id
			  FROM rides r
			 WHERE r.host_user_id = ?
			   AND r.deleted_at IS NULL
			   AND r.is_ongoing <> 1
			   AND r.start_time <= NOW()
			UNION
			SELECT b.ride_id
			  FROM bookings b
			  JOIN rides r ON r.id = b.ride_id
			 WHERE b.passenger_id = ?
			   AND b.request_status = 'accepted'
			   AND b.deleted_at IS NULL
			   AND r.deleted_at IS NULL
			   AND r.is_ongoing <> 1
			   AND r.start_time <= NOW()
		),
		travelled_users AS (
			SELECT r.host_user_id AS user_id
			  FROM travelled_rides tr
			  JOIN rides r ON r.id = tr.id
			 WHERE r.host_user_id <> ?
			UNION
			SELECT b.passenger_id AS user_id
			  FROM travelled_rides tr
			  JOIN bookings b ON b.ride_id = tr.id
			 WHERE b.request_status = 'accepted'
			   AND b.deleted_at IS NULL
			   AND b.passenger_id <> ?
		)
		SELECT DISTINCT
		       u.id,
		       u.name,
		       u.email,
		       u.profile_picture_url,
		       u.contact_number,
		       u.gender,
		       u.yob,
		       u.is_email_verified
		  FROM users u
		  JOIN travelled_users tu ON tu.user_id = u.id
		 WHERE u.deleted_at IS NULL
		 ORDER BY u.name ASC
		 LIMIT 200
	`, user.ID, user.ID, user.ID, user.ID).Scan(&passengers).Error; err != nil {
		log.Printf("Error fetching passenger users: %v\n", err)
		return c.Status(fiber.StatusBadGateway).SendString("Error fetching passenger users")
	}

	return c.Status(fiber.StatusOK).JSON(passengers)
}
