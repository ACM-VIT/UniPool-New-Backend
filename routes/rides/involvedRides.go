package rides

import (
	"context"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func GetInvolvedRides(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	userUUID := user.ID

	oneDayAgo := time.Now().UTC().Add(-24 * time.Hour)
	baseCtx := c.UserContext()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()

	type involvedRideRow struct {
		ID                    uuid.UUID
		HostUserID            uuid.UUID
		StartLocation         string
		EndLocation           string
		StartTime             time.Time
		TotalSeats            uint
		BookedSeats           uint
		TotalPrice            uint
		IsOngoing             uint
		IsSameGender          uint
		Settings              models.RideSettings
		HostName              string
		HostProfilePictureURL string
		HostIsEmailVerified   bool
	}

	var rows []involvedRideRow
	if err := database.Database.Db.WithContext(ctx).Raw(`
		SELECT r.id,
		       r.host_user_id,
		       r.start_location,
		       r.end_location,
		       r.start_time,
		       r.total_seats,
		       r.booked_seats,
		       r.total_price,
		       r.is_ongoing,
		       r.is_same_gender,
		       r.settings,
		       u.name AS host_name,
		       u.profile_picture_url AS host_profile_picture_url,
		       u.is_email_verified AS host_is_email_verified
		  FROM rides r
		  JOIN users u ON u.id = r.host_user_id
		  LEFT JOIN bookings b
		    ON b.ride_id = r.id
		   AND b.passenger_id = ?
		   AND b.deleted_at IS NULL
		 WHERE r.deleted_at IS NULL
		   AND (r.host_user_id = ? OR b.id IS NOT NULL)
		   AND (r.is_ongoing = 1 OR r.start_time > ?)
		 ORDER BY r.start_time DESC
	`, userUUID, userUUID, oneDayAgo).Scan(&rows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch involved rides"})
	}

	rides := make([]models.Ride, 0, len(rows))
	for _, row := range rows {
		rides = append(rides, models.Ride{
			BaseModel:  models.BaseModel{ID: row.ID},
			HostUserID: row.HostUserID,
			HostUser: models.User{
				BaseModel:         models.BaseModel{ID: row.HostUserID},
				Name:              row.HostName,
				ProfilePictureURL: row.HostProfilePictureURL,
				IsEmailVerified:   row.HostIsEmailVerified,
			},
			StartLocation: row.StartLocation,
			EndLocation:   row.EndLocation,
			StartTime:     row.StartTime,
			TotalSeats:    row.TotalSeats,
			BookedSeats:   row.BookedSeats,
			TotalPrice:    row.TotalPrice,
			IsOngoing:     row.IsOngoing,
			IsSameGender:  row.IsSameGender,
			Settings:      row.Settings,
		})
	}

	return c.JSON(fiber.Map{"rides": rides, "count": len(rides)})
}

func GetAllPassengers(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	userUUID := user.ID

	var passengers []models.User
	if err := database.Database.Db.Raw(`
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
		 WHERE u.deleted_at IS NULL
		   AND u.id <> ?
		   AND u.id IN (
				-- Accepted passengers on rides the caller hosts.
				SELECT b.passenger_id
				  FROM bookings b
				  JOIN rides r ON r.id = b.ride_id
				 WHERE r.host_user_id = ?
				   AND b.request_status = 'accepted'
				   AND b.deleted_at IS NULL
				   AND r.deleted_at IS NULL
				UNION
				-- Hosts for rides the caller has interacted with.
				SELECT r.host_user_id
				  FROM bookings b
				  JOIN rides r ON r.id = b.ride_id
				 WHERE b.passenger_id = ?
				   AND r.host_user_id <> ?
				   AND b.deleted_at IS NULL
				   AND r.deleted_at IS NULL
		   )
		 ORDER BY u.name ASC
	`, userUUID, userUUID, userUUID, userUUID).Scan(&passengers).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch passengers"})
	}

	return c.JSON(fiber.Map{"passengers": passengers, "count": len(passengers)})
}
