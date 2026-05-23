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

// MatchingRideSummary is the per-row shape both the standalone
// /ride/matching-create endpoint AND the `strict_matches` field on
// /ride/search return. Same JSON contract in both places so clients
// don't need to maintain two row types.
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

// StrictMatchParams configures FindStrictRouteMatches. All fields
// are caller-validated — the function trusts the inputs and only
// clamps the things that bound cost (radius, window, limit).
type StrictMatchParams struct {
	StartLat    float64
	StartLon    float64
	EndLat      float64
	EndLon      float64
	StartTime   time.Time
	RadiusM     float64
	WindowHours float64
	Limit       int
	// Set to the requester's user ID when authed. The function
	// excludes the requester's own hosted rides AND any rides the
	// requester already has any-status booking on (so a rejected
	// passenger doesn't see the same ride re-suggested as a "join
	// this!" prompt).
	ExcludeUserID uuid.UUID
}

// NormalizeStrictMatchParams clamps user-supplied bounds to sane
// defaults. Called by both endpoints that consume this — exposed
// so callers can show the effective values back to the user.
func NormalizeStrictMatchParams(p *StrictMatchParams) {
	if p.RadiusM <= 0 {
		p.RadiusM = 500
	}
	if p.RadiusM > 3000 {
		p.RadiusM = 3000
	}
	if p.WindowHours <= 0 {
		p.WindowHours = 3
	}
	if p.WindowHours > 12 {
		p.WindowHours = 12
	}
	if p.Limit <= 0 {
		p.Limit = 5
	}
	if p.Limit > 10 {
		p.Limit = 10
	}
	if p.StartTime.IsZero() {
		p.StartTime = time.Now().Add(time.Hour)
	}
}

// FindStrictRouteMatches runs the strict "is this ride already on
// offer?" probe and returns up to p.Limit candidates ordered by
// combined distance ascending. Match shape:
//
//   - start point within p.RadiusM of (StartLat, StartLon)
//   - end point within p.RadiusM of (EndLat, EndLon)
//   - start_time within ±p.WindowHours of p.StartTime
//   - at least one open seat (counted live from bookings, not the
//     cached r.booked_seats counter — that counter has drifted in
//     the past when an Accept transaction partially failed)
//   - not ongoing, not soft-deleted
//   - not owned by p.ExcludeUserID, not already booked by them
//
// Defaults intentionally conservative: 500m radius + 3h window.
// Geocoders drift several hundred metres on vague place names; a
// wider window surfaces rides that are actually elsewhere and
// trains users to ignore the prompt. Caller can override either
// via StrictMatchParams.
//
// Used by:
//   - /ride/matching-create (dedicated CreateRide probe)
//   - /ride/search (surfaced as `strict_matches` alongside the
//     regular fuzzy results so the client can render a "best match"
//     badge on overlapping rows)
func FindStrictRouteMatches(ctx context.Context, p StrictMatchParams) ([]MatchingRideSummary, error) {
	NormalizeStrictMatchParams(&p)

	windowStart := p.StartTime.Add(-time.Duration(p.WindowHours * float64(time.Hour)))
	windowEnd := p.StartTime.Add(time.Duration(p.WindowHours * float64(time.Hour)))

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
			r.start_time, r.total_seats,
			(
				SELECT COUNT(*)
				FROM bookings b
				WHERE b.ride_id = r.id
				  AND b.request_status = 'accepted'
				  AND b.deleted_at IS NULL
			)::int AS booked_seats,
			r.total_price,
			ST_Distance(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.start_longitude::float8, r.start_latitude::float8), 4326)::geography
			) AS start_distance_m,
			ST_Distance(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.end_longitude::float8, r.end_latitude::float8), 4326)::geography
			) AS end_distance_m
		`, p.StartLon, p.StartLat, p.EndLon, p.EndLat).
		Joins("LEFT JOIN users u ON u.id = r.host_user_id").
		Where("r.start_time BETWEEN ? AND ?", windowStart, windowEnd).
		Where(`
			r.total_seats > (
				SELECT COUNT(*) FROM bookings b
				WHERE b.ride_id = r.id
				  AND b.request_status = 'accepted'
				  AND b.deleted_at IS NULL
			)
		`).
		Where("r.is_ongoing = 0").
		Where("r.deleted_at IS NULL").
		Where("r.start_latitude IS NOT NULL AND r.start_longitude IS NOT NULL").
		Where("r.end_latitude IS NOT NULL AND r.end_longitude IS NOT NULL").
		Where(`
			ST_DWithin(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.start_longitude::float8, r.start_latitude::float8), 4326)::geography,
				?
			)`, p.StartLon, p.StartLat, p.RadiusM).
		Where(`
			ST_DWithin(
				ST_SetSRID(ST_MakePoint(?::float8, ?::float8), 4326)::geography,
				ST_SetSRID(ST_MakePoint(r.end_longitude::float8, r.end_latitude::float8), 4326)::geography,
				?
			)`, p.EndLon, p.EndLat, p.RadiusM)
	if p.ExcludeUserID != (uuid.UUID{}) {
		q = q.Where("r.host_user_id <> ?", p.ExcludeUserID)
		q = q.Where(`
			r.id NOT IN (
				SELECT b.ride_id FROM bookings b
				WHERE b.passenger_id = ?
				  AND b.deleted_at IS NULL
			)
		`, p.ExcludeUserID)
	}
	// CockroachDB doesn't accept SELECT aliases inside an arithmetic
	// ORDER BY expression — only bare-alias references work. Inline
	// the ST_Distance calls so the sort uses the expression directly.
	// Coordinate floats come from the caller (validated upstream) so
	// fmt.Sprintf has no SQL-injection surface.
	orderBy := fmt.Sprintf(`
		(ST_Distance(
			ST_SetSRID(ST_MakePoint(%f::float8, %f::float8), 4326)::geography,
			ST_SetSRID(ST_MakePoint(r.start_longitude::float8, r.start_latitude::float8), 4326)::geography
		) + ST_Distance(
			ST_SetSRID(ST_MakePoint(%f::float8, %f::float8), 4326)::geography,
			ST_SetSRID(ST_MakePoint(r.end_longitude::float8, r.end_latitude::float8), 4326)::geography
		)) ASC, r.start_time ASC
	`, p.StartLon, p.StartLat, p.EndLon, p.EndLat)
	if err := q.
		Order(orderBy).
		Limit(p.Limit).
		Scan(&rows).Error; err != nil {
		return nil, err
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
	return matches, nil
}

// MatchingCreate is the standalone HTTP handler for the dedicated
// /ride/matching-create endpoint. Thin wrapper around
// FindStrictRouteMatches: parses query params, normalises, calls
// the function, formats the response. CreateRide on the client can
// hit this OR read `strict_matches` from /ride/search — same
// underlying logic, same row shape.
//
// Route: GET /ride/matching-create
// Query: start_lat, start_lon, end_lat, end_lon (required),
//        start_time (RFC3339, default now+1h),
//        radius_m (default 500, max 3000),
//        window_hours (default 3, max 12),
//        limit (default 5, max 10).
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
	radiusM, _ := strconv.ParseFloat(c.Query("radius_m"), 64)
	windowHours, _ := strconv.ParseFloat(c.Query("window_hours"), 64)
	limit, _ := strconv.Atoi(c.Query("limit"))

	var excludeID uuid.UUID
	if u, ok := c.Locals("user").(models.User); ok {
		excludeID = u.ID
	}

	params := StrictMatchParams{
		StartLat:      startLat,
		StartLon:      startLon,
		EndLat:        endLat,
		EndLon:        endLon,
		StartTime:     startTime,
		RadiusM:       radiusM,
		WindowHours:   windowHours,
		Limit:         limit,
		ExcludeUserID: excludeID,
	}
	NormalizeStrictMatchParams(&params)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	matches, err := FindStrictRouteMatches(ctx, params)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to fetch matches",
		})
	}

	return c.JSON(fiber.Map{
		"matches":      matches,
		"count":        len(matches),
		"radius_m":     params.RadiusM,
		"window_hours": params.WindowHours,
	})
}
