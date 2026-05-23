package rides

import (
	"context"
	"math"
	"strconv"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

type NearbyRideSummary struct {
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

type NearbyRidesPayload struct {
	Rides  []NearbyRideSummary `json:"rides"`
	Radius float64             `json:"radius"`
	Count  int                 `json:"count"`
}

type nearbyBounds struct {
	minLat float64
	maxLat float64
	minLng float64
	maxLng float64
}

func NormalizeNearbyRadius(radius float64) float64 {
	if radius <= 0 {
		return 5000
	}
	if radius > 50000 {
		return 50000
	}
	return radius
}

func NormalizeNearbyLimit(limit int) int {
	if limit <= 0 || limit > 50 {
		return 30
	}
	return limit
}

func boundsForNearby(lat, lng, radius float64) nearbyBounds {
	const metersPerDegree = 111320.0
	latDelta := radius / metersPerDegree
	cosLat := math.Cos(lat * math.Pi / 180)
	if math.Abs(cosLat) < 0.01 {
		cosLat = 0.01
	}
	lngDelta := radius / (metersPerDegree * math.Abs(cosLat))

	return nearbyBounds{
		minLat: math.Max(-90, lat-latDelta),
		maxLat: math.Min(90, lat+latDelta),
		minLng: lng - lngDelta,
		maxLng: lng + lngDelta,
	}
}

func LoadNearbyRides(ctx context.Context, lat, lng, radius float64, limit int) (NearbyRidesPayload, error) {
	radius = NormalizeNearbyRadius(radius)
	limit = NormalizeNearbyLimit(limit)
	bounds := boundsForNearby(lat, lng, radius)

	var rides []NearbyRideSummary
	err := database.Database.Db.WithContext(ctx).
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
		Where("start_latitude BETWEEN ? AND ?", bounds.minLat, bounds.maxLat).
		Where("start_longitude BETWEEN ? AND ?", bounds.minLng, bounds.maxLng).
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
		return NearbyRidesPayload{}, err
	}
	if rides == nil {
		rides = []NearbyRideSummary{}
	}

	return NearbyRidesPayload{
		Rides:  rides,
		Radius: radius,
		Count:  len(rides),
	}, nil
}

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
	radius = NormalizeNearbyRadius(radius)
	bounds := boundsForNearby(lat, lng, radius)

	// PUBLIC endpoint — a slow PostGIS plan or a DB hiccup must not
	// be able to pile up requests against the unauthenticated route
	// and exhaust the connection pool. NearbyRides already has a 5s
	// cap; this mirrors that posture.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int64
	err := database.Database.Db.WithContext(ctx).Model(&models.Ride{}).
		Where("start_time >= ?", time.Now()).
		Where("booked_seats < total_seats").
		Where("is_ongoing = 0").
		Where("start_longitude IS NOT NULL AND start_latitude IS NOT NULL").
		Where("start_latitude BETWEEN ? AND ?", bounds.minLat, bounds.maxLat).
		Where("start_longitude BETWEEN ? AND ?", bounds.minLng, bounds.maxLng).
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

	limit, _ := strconv.Atoi(c.Query("limit"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, err := LoadNearbyRides(ctx, lat, lng, radius, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Couldn't load nearby rides",
		})
	}

	return c.Status(fiber.StatusOK).JSON(payload)
}
