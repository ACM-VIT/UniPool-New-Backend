package rides

import (
   "unipool-backend/database"
   "unipool-backend/models"
   "time"

   "github.com/gofiber/fiber/v2"
   "github.com/google/uuid"
)

func GetInvolvedRides(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	userUUID := user.ID

	var rides []models.Ride

	bookedRidesSubQuery := database.Database.Db.Model(&models.Booking{}).Select("ride_id").Where("passenger_id = ?", userUUID)

	   if err := database.Database.Db.Preload("HostUser").
			   Where("(host_user_id = ? OR id IN (?)) AND (is_ongoing = ? OR start_time > ?)", userUUID, bookedRidesSubQuery, 1, time.Now().UTC()).
			   Order("start_time desc").
			   Find(&rides).Error; err != nil {
			   return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch involved rides"})
	   }

	return c.JSON(fiber.Map{"rides": rides, "count": len(rides)})
}

func GetAllPassengers(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	userUUID := user.ID

	var hostedRides []models.Ride
	if err := database.Database.Db.Where("host_user_id = ?", userUUID).Find(&hostedRides).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch hosted rides"})
	}

	var rideIDs []uuid.UUID
	for _, ride := range hostedRides {
		rideIDs = append(rideIDs, ride.ID)
	}

	var passengers []models.User
	if len(rideIDs) > 0 {
		if err := database.Database.Db.Model(&models.User{}).Distinct().
			Joins("JOIN bookings ON bookings.passenger_id = users.id").
			Where("bookings.ride_id IN (?)", rideIDs).
			Find(&passengers).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch passengers"})
		}
	}

	return c.JSON(fiber.Map{"passengers": passengers, "count": len(passengers)})
}
