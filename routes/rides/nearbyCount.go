package rides

import (
	"context"
	"math"
	"strconv"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type NearbyRideSummary struct {
	ID             string    `json:"id"`
	HostUserID     string    `json:"host_user_id"`
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

func conservativeCoordinateRadius(radius float64) float64 {
	if radius <= 0 {
		return radius
	}
	padding := radius * 0.01
	if padding < 25 {
		padding = 25
	}
	return radius + padding
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

func applyNearbyRadiusFilter(q *gorm.DB, lat, lng, radius float64) *gorm.DB {
	return q.Where(`
		12742.0::float8 * ASIN(SQRT(
			POWER(SIN(RADIANS((start_latitude::float8 - ?::float8) / 2.0::float8)), 2.0::float8) +
			COS(RADIANS(?::float8)) *
			COS(RADIANS(start_latitude::float8)) *
			POWER(SIN(RADIANS((start_longitude::float8 - ?::float8) / 2.0::float8)), 2.0::float8)
		)) <= (?::float8 / 1000.0::float8)
	`, lat, lat, lng, radius)
}

func LoadNearbyRides(ctx context.Context, lat, lng, radius float64, limit int, excludeHostUserID string) (NearbyRidesPayload, error) {
	radius = NormalizeNearbyRadius(radius)
	limit = NormalizeNearbyLimit(limit)
	filterRadius := conservativeCoordinateRadius(radius)
	bounds := boundsForNearby(lat, lng, filterRadius)

	q := database.Database.Db.WithContext(ctx).
		Model(&models.Ride{}).
		Select(`
			id,
			host_user_id,
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
		// Only future rides should appear on the nearby map and list.
		Where("start_time > ?", time.Now()).
		// Exclude rides with no passenger seats left.
		Where(helpers.PassengerSeatsLeftPredicate).
		Where("is_ongoing = 0").
		Where("start_longitude IS NOT NULL AND start_latitude IS NOT NULL").
		Where("start_latitude BETWEEN ? AND ?", bounds.minLat, bounds.maxLat).
		Where("start_longitude BETWEEN ? AND ?", bounds.minLng, bounds.maxLng)
	q = applyNearbyRadiusFilter(q, lat, lng, radius)

	// Authenticated callers can exclude their own hosted rides server-side.
	if excludeHostUserID != "" {
		q = q.Where("host_user_id <> ?", excludeHostUserID)
	}

	var rides []NearbyRideSummary
	err := q.
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
	filterRadius := conservativeCoordinateRadius(radius)
	bounds := boundsForNearby(lat, lng, filterRadius)

	// Bound DB time so unauthenticated traffic cannot tie up the pool.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int64
	q := database.Database.Db.WithContext(ctx).Model(&models.Ride{}).
		// Match LoadNearbyRides: started rides no longer count as nearby.
		Where("start_time > ?", time.Now()).
		// Exclude rides with no passenger seats left.
		Where(helpers.PassengerSeatsLeftPredicate).
		Where("is_ongoing = 0").
		Where("start_longitude IS NOT NULL AND start_latitude IS NOT NULL").
		Where("start_latitude BETWEEN ? AND ?", bounds.minLat, bounds.maxLat).
		Where("start_longitude BETWEEN ? AND ?", bounds.minLng, bounds.maxLng)
	err := applyNearbyRadiusFilter(q, lat, lng, radius).Count(&count).Error

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
	// Optional viewer filter for excluding self-hosted rides.
	excludeHostUserID := c.Query("exclude_host_user_id")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, err := LoadNearbyRides(ctx, lat, lng, radius, limit, excludeHostUserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   true,
			"message": "Couldn't load nearby rides",
		})
	}

	return c.Status(fiber.StatusOK).JSON(payload)
}
