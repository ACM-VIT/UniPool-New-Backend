package users

import (
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// Function to get details of user.

func GetUser(c *fiber.Ctx) error {
	// Extract user info from locals
	if user, ok := c.Locals("user").(models.User); ok {
		type detailRow struct {
			ID                uuid.UUID
			Name              string
			Email             string
			ProfilePictureURL string
			ContactNumber     string
			Gender            string
			YOB               uint
			DefaultAddress    string
			CreatedAt         time.Time
			UpdatedAt         time.Time
			UPIVPA            string
			IsEmailVerified   bool
			InstituteEmail    string
			InstituteID       *uuid.UUID
			InstituteName     *string
			InstituteCountry  *string
			TotalHostedRides  int64
			TotalBookings     int64
			CompletedTrips    int64
			DistanceTravelled int64
			WeightSaved       int64
		}

		var row detailRow
		if err := database.Database.Db.Raw(`
			WITH completed_user_rides AS (
				SELECT
					CASE
						WHEN r.start_latitude IS NOT NULL
						 AND r.start_longitude IS NOT NULL
						 AND r.end_latitude IS NOT NULL
						 AND r.end_longitude IS NOT NULL
						THEN
							12742.0::float8 * ASIN(SQRT(
								POWER(SIN(RADIANS((r.end_latitude::float8 - r.start_latitude::float8) / 2.0::float8)), 2.0::float8) +
								COS(RADIANS(r.start_latitude::float8)) *
								COS(RADIANS(r.end_latitude::float8)) *
								POWER(SIN(RADIANS((r.end_longitude::float8 - r.start_longitude::float8) / 2.0::float8)), 2.0::float8)
							))
						ELSE
							GREATEST(
								COALESCE(NULLIF(r.total_price, 0)::float8 / 10.0::float8, 0.0::float8),
								CASE lower(trim(r.start_location)) || '-' || lower(trim(r.end_location))
									WHEN 'vellore-chennai' THEN 140.0::float8
									WHEN 'chennai-vellore' THEN 140.0::float8
									WHEN 'vellore-bangalore' THEN 220.0::float8
									WHEN 'bangalore-vellore' THEN 220.0::float8
									WHEN 'delhi-gurgaon' THEN 30.0::float8
									WHEN 'gurgaon-delhi' THEN 30.0::float8
									WHEN 'mumbai-pune' THEN 150.0::float8
									WHEN 'pune-mumbai' THEN 150.0::float8
									ELSE 25.0::float8
								END
							)
					END AS estimated_km
				FROM rides r
				WHERE r.host_user_id = ?
				  AND r.deleted_at IS NULL
				  AND r.is_ongoing <> 1
				  AND r.start_time <= NOW()

				UNION ALL

				SELECT
					CASE
						WHEN r.start_latitude IS NOT NULL
						 AND r.start_longitude IS NOT NULL
						 AND r.end_latitude IS NOT NULL
						 AND r.end_longitude IS NOT NULL
						THEN
							12742.0::float8 * ASIN(SQRT(
								POWER(SIN(RADIANS((r.end_latitude::float8 - r.start_latitude::float8) / 2.0::float8)), 2.0::float8) +
								COS(RADIANS(r.start_latitude::float8)) *
								COS(RADIANS(r.end_latitude::float8)) *
								POWER(SIN(RADIANS((r.end_longitude::float8 - r.start_longitude::float8) / 2.0::float8)), 2.0::float8)
							))
						ELSE
							GREATEST(
								COALESCE(NULLIF(r.total_price, 0)::float8 / 10.0::float8, 0.0::float8),
								CASE lower(trim(r.start_location)) || '-' || lower(trim(r.end_location))
									WHEN 'vellore-chennai' THEN 140.0::float8
									WHEN 'chennai-vellore' THEN 140.0::float8
									WHEN 'vellore-bangalore' THEN 220.0::float8
									WHEN 'bangalore-vellore' THEN 220.0::float8
									WHEN 'delhi-gurgaon' THEN 30.0::float8
									WHEN 'gurgaon-delhi' THEN 30.0::float8
									WHEN 'mumbai-pune' THEN 150.0::float8
									WHEN 'pune-mumbai' THEN 150.0::float8
									ELSE 25.0::float8
								END
							)
					END AS estimated_km
				FROM bookings b
				JOIN rides r ON r.id = b.ride_id
				WHERE b.passenger_id = ?
				  AND b.request_status = 'accepted'
				  AND b.deleted_at IS NULL
				  AND r.deleted_at IS NULL
				  AND r.is_ongoing <> 1
				  AND r.start_time <= NOW()
			),
			profile_stats AS (
				SELECT
					COUNT(*)::bigint AS completed_trips,
					ROUND(COALESCE(SUM(
						CASE
							WHEN estimated_km >= 300.0::float8 THEN 100.0::float8
							WHEN estimated_km > 0.0::float8 THEN estimated_km
							ELSE 0.0::float8
						END
					), 0.0::float8))::bigint AS distance_travelled
				FROM completed_user_rides
			)
			SELECT
				u.id,
				u.name,
				u.email,
				u.profile_picture_url,
				u.contact_number,
				u.gender,
				u.yob,
				u.default_address,
				u.created_at,
				u.updated_at,
				u.upi_vpa,
				u.is_email_verified,
				u.institute_email,
				u.institute_id,
				i.name AS institute_name,
				i.country AS institute_country,
				(
					SELECT COUNT(*)
					  FROM rides r
					 WHERE r.host_user_id = u.id
					   AND r.deleted_at IS NULL
				) AS total_hosted_rides
				,
				(
					SELECT COUNT(*)
					  FROM bookings b
					 WHERE b.passenger_id = u.id
					   AND b.deleted_at IS NULL
				) AS total_bookings,
				ps.completed_trips,
				ps.distance_travelled,
				0::bigint AS weight_saved
			  FROM users u
			  LEFT JOIN institutes i ON i.id = u.institute_id
			  CROSS JOIN profile_stats ps
			 WHERE u.id = ?
			 LIMIT 1
		`, user.ID, user.ID, user.ID).Scan(&row).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error":   true,
				"message": "Failed to load user details",
			})
		}
		if row.ID == (uuid.UUID{}) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error":   true,
				"message": "User not found",
			})
		}
		row.WeightSaved = (row.DistanceTravelled*84 + 500) / 1000

		var institute any
		if row.InstituteID != nil {
			institute = fiber.Map{
				"id":      row.InstituteID,
				"name":    row.InstituteName,
				"country": row.InstituteCountry,
			}
		}

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"user": fiber.Map{
				"id":                  row.ID,
				"name":                row.Name,
				"email":               row.Email,
				"profile_picture_url": row.ProfilePictureURL,
				"contact_number":      row.ContactNumber,
				"gender":              row.Gender,
				"yob":                 row.YOB,
				"default_address":     row.DefaultAddress,
				"created_at":          row.CreatedAt,
				"updated_at":          row.UpdatedAt,
				"total_hosted_rides":  row.TotalHostedRides,
				"total_bookings":      row.TotalBookings,
				"completed_trips":     row.CompletedTrips,
				"distance_travelled":  row.DistanceTravelled,
				"weight_saved":        row.WeightSaved,
				// Verification + payment surface for the personal-info
				// screen. Empty strings/false/nil when unset.
				"upi_vpa":           row.UPIVPA,
				"is_email_verified": row.IsEmailVerified,
				"institute_email":   row.InstituteEmail,
				"institute":         institute,
				"institute_id":      row.InstituteID,
			},
		})
	}

	if newUser, ok := c.Locals("newuser").(map[string]interface{}); ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"newUser": newUser,
			"error":   false,
			"message": "User not found in database, signup required",
		})
	}

	// If user is not found in locals, return an error
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
		"error":   true,
		"message": "User data not found in locals",
	})
}
