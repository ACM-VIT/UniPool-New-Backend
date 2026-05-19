package rides

import (
	"strconv"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// NearbyRidesCount returns a quick count of upcoming rides whose start
// point lies within `radius` metres of (lat, lng). Used by HomeScreen to
// decide whether to surface the "Carpools within X km of you" pill.
//
// Public — no auth required so unauthenticated users can see whether
// there's any activity worth signing in for. The shape is deliberately
// limited to a count to avoid leaking ride details.
func NearbyRidesCount(c *fiber.Ctx) error {
	lat, latErr := strconv.ParseFloat(c.Query("lat"), 64)
	lng, lngErr := strconv.ParseFloat(c.Query("lng"), 64)
	if latErr != nil || lngErr != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   true,
			"message": "lat and lng query params are required",
		})
	}

	// Default radius 5km. Clamp to keep the index-friendly range tight.
	radius, _ := strconv.ParseFloat(c.Query("radius"), 64)
	if radius <= 0 {
		radius = 5000
	}
	if radius > 50000 {
		radius = 50000
	}

	var count int64
	err := database.Database.Db.Model(&models.Ride{}).
		Where("start_time >= ?", time.Now()).
		Where("booked_seats < total_seats").
		Where("is_ongoing = 0").
		Where("start_longitude IS NOT NULL AND start_latitude IS NOT NULL").
		Where(
			"ST_DWithin("+
				"ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography, "+
				"ST_SetSRID(ST_MakePoint(start_longitude::float8, start_latitude::float8), 4326)::geography, "+
				"?"+
				")",
			lng, lat, radius,
		).
		Count(&count).Error

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Couldn't count nearby rides",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"count":  count,
		"radius": radius,
	})
}

// NearbyRides returns a lightweight list of upcoming rides whose start
// point lies within `radius` metres of (lat, lng). Used by HomeScreen
// to plot pins on the map so the user can see *where* rides are
// happening, not just a count.
//
// Public — same auth posture as `NearbyRidesCount`. Only emits the
// fields needed to draw a marker and navigate to ride details.
func NearbyRides(c *fiber.Ctx) error {
	lat, latErr := strconv.ParseFloat(c.Query("lat"), 64)
	lng, lngErr := strconv.ParseFloat(c.Query("lng"), 64)
	if latErr != nil || lngErr != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   true,
			"message": "lat and lng query params are required",
		})
	}

	radius, _ := strconv.ParseFloat(c.Query("radius"), 64)
	if radius <= 0 {
		radius = 5000
	}
	if radius > 50000 {
		radius = 50000
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 50 {
		limit = 30
	}

	type row struct {
		ID             string    `json:"id"`
		StartLocation  string    `json:"start_location"`
		EndLocation    string    `json:"end_location"`
		StartLatitude  float64   `json:"start_latitude"`
		StartLongitude float64   `json:"start_longitude"`
		EndLatitude    float64   `json:"end_latitude"`
		EndLongitude   float64   `json:"end_longitude"`
		StartTime      time.Time `json:"start_time"`
		TotalSeats     uint      `json:"total_seats"`
		BookedSeats    uint      `json:"booked_seats"`
		TotalPrice     uint      `json:"total_price"`
	}

	var rides []row
	err := database.Database.Db.
		Model(&models.Ride{}).
		Select(`
			id,
			start_location,
			end_location,
			start_latitude,
			start_longitude,
			end_latitude,
			end_longitude,
			start_time,
			total_seats,
			booked_seats,
			total_price
		`).
		Where("start_time >= ?", time.Now()).
		Where("booked_seats < total_seats").
		Where("is_ongoing = 0").
		Where("start_longitude IS NOT NULL AND start_latitude IS NOT NULL").
		Where(
			"ST_DWithin("+
				"ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography, "+
				"ST_SetSRID(ST_MakePoint(start_longitude::float8, start_latitude::float8), 4326)::geography, "+
				"?"+
				")",
			lng, lat, radius,
		).
		Order("start_time asc").
		Limit(limit).
		Scan(&rides).Error
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Couldn't load nearby rides",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"rides":  rides,
		"radius": radius,
	})
}
