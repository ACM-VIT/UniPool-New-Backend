package rides

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// MatchingCreateRide is the "don't create a duplicate, join this
// one instead" probe. CreateRide calls it once the user has filled
// in from + to coordinates and a date; if the response carries any
// matches, the screen surfaces a "x hosts are already going your
// way" card before the user commits to posting a new ride.
//
// Match shape is strict-on-both-ends. A ride matches when:
//   - its start point is within `radius_m` of the requester's start
//   - its end point is within `radius_m` of the requester's end
//   - its start_time falls within `±window_hours` of the requester's
//   - it still has at least one open seat
//   - it isn't ongoing already
//   - it isn't the requester's own ride (when authed)
//
// Tighter geo + time window than /ride/search by design — search
// is built for "show me anything broadly similar", this is built
// for "is this trip already on offer". 1km / 4h defaults are the
// sweet spot for campus traffic.
//
// Public endpoint (under OptionalAuthenticate) — unauth callers
// still benefit from the suggestion. Signed-in callers also get
// their own rides filtered out so the host doesn't get told to
// join themselves.
//
// Route: GET /ride/matching-create
// Query: start_lat, start_lon, end_lat, end_lon, start_time,
//        radius_m (default 1000), window_hours (default 4),
//        limit (default 5, clamped at 10).
type MatchingRideSummary struct {
	ID             string    `json:"id"`
	HostUserID     string    `json:"host_user_id"`
	HostUserName   string    `json:"host_user_name,omitempty"`
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
	// Distance values in metres from the requested points. Lets the
	// client render "75m from your pickup" / "210m from your drop"
	// instead of a generic "match" badge.
	StartDistanceM float64 `json:"start_distance_m"`
	EndDistanceM   float64 `json:"end_distance_m"`
}

type MatchingRidesResponse struct {
	Matches  []MatchingRideSummary `json:"matches"`
	Count    int                   `json:"count"`
	RadiusM  float64               `json:"radius_m"`
	WindowHr float64               `json:"window_hours"`
}

func MatchingCreate(c *fiber.Ctx) error {
	startLat, sLatErr := strconv.ParseFloat(c.Query("start_lat"), 64)
	startLon, sLonErr := strconv.ParseFloat(c.Query("start_lon"), 64)
	endLat, eLatErr := strconv.ParseFloat(c.Query("end_lat"), 64)
	endLon, eLonErr := strconv.ParseFloat(c.Query("end_lon"), 64)
	if sLatErr != nil || sLonErr != nil || eLatErr != nil || eLonErr != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "start_lat, start_lon, end_lat, end_lon are all required floats",
		})
	}

	startTime := time.Now().Add(1 * time.Hour)
	if raw := c.Query("start_time"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			startTime = t
		}
	}

	// Defaults tuned for campus carpool: 1km radius is "same
	// pickup / dropoff cluster" (a bus stop, a hostel block, a
	// metro exit). 4h window is "same trip, give or take a coffee".
	// Caller can override per-request but we clamp to sane bounds
	// so a misbehaving client can't ask for "every ride in the
	// city in the next month".
	radiusM, _ := strconv.ParseFloat(c.Query("radius_m"), 64)
	if radiusM <= 0 {
		radiusM = 1000
	}
	if radiusM > 5000 {
		radiusM = 5000
	}
	windowHours, _ := strconv.ParseFloat(c.Query("window_hours"), 64)
	if windowHours <= 0 {
		windowHours = 4
	}
	if windowHours > 24 {
		windowHours = 24
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	windowStart := startTime.Add(-time.Duration(windowHours * float64(time.Hour)))
	windowEnd := startTime.Add(time.Duration(windowHours * float64(time.Hour)))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Exclude the requester's own rides when authed. The user's
	// own in-flight ride showing up as a match would be silly.
	var excludeHostID uuid.UUID
	if u, ok := c.Locals("user").(models.User); ok {
		excludeHostID = u.ID
	}

	// One query, scored. ST_DWithin on both ends gates the result
	// set; ST_Distance computes the per-row metres-to-pickup and
	// metres-to-dropoff for the response. Combined distance drives
	// the order so the closest matches come first.
	type row struct {
		ID             uuid.UUID `gorm:"column:id"`
		HostUserID     uuid.UUID `gorm:"column:host_user_id"`
		HostUserName   string    `gorm:"column:host_user_name"`
		StartLocation  string    `gorm:"column:start_location"`
		EndLocation    string    `gorm:"column:end_location"`
		StartLatitude  float64   `gorm:"column:start_latitude"`
		StartLongitude float64   `gorm:"column:start_longitude"`
		EndLatitude    float64   `gorm:"column:end_latitude"`
		EndLongitude   float64   `gorm:"column:end_longitude"`
		StartTime      time.Time `gorm:"column:start_time"`
		TotalSeats     uint      `gorm:"column:total_seats"`
		BookedSeats    uint      `gorm:"column:booked_seats"`
		TotalPrice     uint      `gorm:"column:total_price"`
		StartDistanceM float64   `gorm:"column:start_distance_m"`
		EndDistanceM   float64   `gorm:"column:end_distance_m"`
	}
	var rows []row
	q := database.Database.Db.WithContext(ctx).
		Table("rides AS r").
		Select(`
			r.id, r.host_user_id,
			COALESCE(u.name, '') AS host_user_name,
			r.start_location, r.end_location,
			r.start_latitude, r.start_longitude,
			r.end_latitude, r.end_longitude,
			r.start_time, r.total_seats, r.booked_seats, r.total_price,
			ST_Distance(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.start_longitude::float8, r.start_latitude::float8), 4326)::geography
			) AS start_distance_m,
			ST_Distance(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.end_longitude::float8, r.end_latitude::float8), 4326)::geography
			) AS end_distance_m
		`, startLon, startLat, endLon, endLat).
		Joins("LEFT JOIN users u ON u.id = r.host_user_id").
		Where("r.start_time BETWEEN ? AND ?", windowStart, windowEnd).
		Where("r.booked_seats < r.total_seats").
		Where("r.is_ongoing = 0").
		Where("r.deleted_at IS NULL").
		Where("r.start_latitude IS NOT NULL AND r.start_longitude IS NOT NULL").
		Where("r.end_latitude IS NOT NULL AND r.end_longitude IS NOT NULL").
		Where(`
			ST_DWithin(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.start_longitude::float8, r.start_latitude::float8), 4326)::geography,
				?
			)`, startLon, startLat, radiusM).
		Where(`
			ST_DWithin(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.end_longitude::float8, r.end_latitude::float8), 4326)::geography,
				?
			)`, endLon, endLat, radiusM)
	if excludeHostID != (uuid.UUID{}) {
		q = q.Where("r.host_user_id <> ?", excludeHostID)
	}
	// CockroachDB doesn't accept SELECT aliases inside an
	// arithmetic ORDER BY expression (only bare-alias references
	// work). Inline the ST_Distance calls so the sort uses the
	// expression directly. Coordinate floats come from ParseFloat
	// above so inlining them via fmt.Sprintf is safe — no SQL
	// injection surface.
	orderBy := fmt.Sprintf(`
		(ST_Distance(
			ST_SetSRID(ST_MakePoint(%f::float8, %f::float8), 4326)::geography,
			ST_SetSRID(ST_MakePoint(r.start_longitude::float8, r.start_latitude::float8), 4326)::geography
		) + ST_Distance(
			ST_SetSRID(ST_MakePoint(%f::float8, %f::float8), 4326)::geography,
			ST_SetSRID(ST_MakePoint(r.end_longitude::float8, r.end_latitude::float8), 4326)::geography
		)) ASC, r.start_time ASC
	`, startLon, startLat, endLon, endLat)
	if err := q.
		Order(orderBy).
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to fetch matches",
		})
	}

	matches := make([]MatchingRideSummary, 0, len(rows))
	for _, r := range rows {
		matches = append(matches, MatchingRideSummary{
			ID:             r.ID.String(),
			HostUserID:     r.HostUserID.String(),
			HostUserName:   r.HostUserName,
			StartLocation:  r.StartLocation,
			EndLocation:    r.EndLocation,
			StartLatitude:  r.StartLatitude,
			StartLongitude: r.StartLongitude,
			EndLatitude:    r.EndLatitude,
			EndLongitude:   r.EndLongitude,
			StartTime:      r.StartTime,
			TotalSeats:     r.TotalSeats,
			BookedSeats:    r.BookedSeats,
			TotalPrice:     r.TotalPrice,
			StartDistanceM: r.StartDistanceM,
			EndDistanceM:   r.EndDistanceM,
		})
	}

	return c.JSON(MatchingRidesResponse{
		Matches:  matches,
		Count:    len(matches),
		RadiusM:  radiusM,
		WindowHr: windowHours,
	})
}
