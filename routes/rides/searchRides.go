package rides

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var searchDebugLogs = os.Getenv("SEARCH_DEBUG_LOGS") == "1"

type RideCard struct {
	RideID                    uuid.UUID `json:"id"`
	HostUserID                uuid.UUID `json:"host_user_id"`
	HostUserName              string    `json:"host_user_name"`
	HostUserProfilePictureURL string    `json:"host_user_profile_picture_url"`
	HostUserYOB               uint      `json:"host_user_yob,omitempty"`
	// HostUserGender lets the client surface a same-gender affinity
	// signal (e.g. soft pink tint when a female passenger searches and
	// the host is also female). Omit when unset to avoid leaking blanks.
	HostUserGender    string    `json:"host_user_gender,omitempty"`
	SameGenderFemale  bool      `json:"same_gender_female,omitempty"`
	HostRatingAverage *float64  `json:"host_rating_average,omitempty"`
	HostRatingCount   int64     `json:"host_rating_count,omitempty"`
	StartLocation     string    `json:"start_location"`
	EndLocation       string    `json:"end_location"`
	StartTime         time.Time `json:"start_time"`
	// CreatedAt feeds the "Just listed" match reason — we only surface the
	// flag on the frontend, but the timestamp is exposed in case clients
	// want their own freshness UX.
	CreatedAt      time.Time `json:"created_at"`
	TotalSeats     uint      `json:"total_seats"`
	BookedSeats    uint      `json:"booked_seats"`
	TotalPrice     uint      `json:"total_price"`
	StartLatitude  *float64  `json:"start_latitude,omitempty"`
	StartLongitude *float64  `json:"start_longitude,omitempty"`
	EndLatitude    *float64  `json:"end_latitude,omitempty"`
	EndLongitude   *float64  `json:"end_longitude,omitempty"`
	StartDistance  *float64  `json:"start_distance,omitempty"`
	EndDistance    *float64  `json:"end_distance,omitempty"`
	TotalDistance  *float64  `json:"total_distance,omitempty"`
	RelevanceScore float64   `json:"relevance_score,omitempty"`
	MatchReason    string    `json:"match_reason,omitempty"`
	// MatchSignals is the structured form of MatchReason: each entry
	// is a self-contained chip the client can render as a typed pill
	// (icon + label + optional detail) under the card. Ordered by
	// internal priority so clients can render the first N without
	// re-sorting. MatchReason stays as a back-compat string clients
	// without a chip renderer can still display.
	MatchSignals []MatchSignal `json:"match_signals,omitempty"`

	// Server-computed UI state — see viewerState.go. Lets clients
	// render the right CTA ("Request seat" vs "Your seat is
	// confirmed" vs "Waiting for host" vs nothing) without doing
	// any conditional math themselves.
	ViewerState     ViewerState   `json:"viewer_state,omitempty"`
	Actions         ViewerActions `json:"actions,omitempty"`
	ViewerBookingID *string       `json:"viewer_booking_id,omitempty"`
}

type SearchParams struct {
	StartLocation string
	EndLocation   string
	StartLat      float64
	StartLon      float64
	EndLat        float64
	EndLon        float64
	HasStartCoord bool
	HasEndCoord   bool
	Date          string
	PreferredTime string
	TargetTime    time.Time
	HasTargetTime bool
	MaxPrice      *uint
	MinSeats      *uint
	SortBy        string
	RadiusKm      float64
	Limit         int
	Offset        int
	User          models.User
}

type searchHostStats struct {
	RideCount int64
	Average   *float64
	Count     int64
}

type searchHostStatsRow struct {
	HostUserID uuid.UUID
	RideCount  int64
	Average    *float64
	Count      int64
}

type searchRideRow struct {
	ID                    uuid.UUID
	CreatedAt             time.Time
	HostUserID            uuid.UUID
	StartLocation         string
	EndLocation           string
	StartLatitude         *float64
	StartLongitude        *float64
	EndLatitude           *float64
	EndLongitude          *float64
	StartTime             time.Time
	TotalSeats            uint
	BookedSeats           uint
	TotalPrice            uint
	IsSameGender          uint
	HostUserName          string
	HostProfilePictureURL string
	HostUserYOB           uint
	HostUserGender        string
}

func loadSearchHostStats(hostIDs []uuid.UUID) map[uuid.UUID]searchHostStats {
	out := make(map[uuid.UUID]searchHostStats, len(hostIDs))
	if len(hostIDs) == 0 {
		return out
	}

	var rows []searchHostStatsRow
	if err := database.Database.Db.Raw(`
		SELECT h.id AS host_user_id,
		       COALESCE(rc.ride_count, 0) AS ride_count,
		       rr.average,
		       COALESCE(rr.count, 0) AS count
		  FROM users h
		  LEFT JOIN (
		       SELECT host_user_id, COUNT(*) AS ride_count
		         FROM rides
		        WHERE host_user_id IN ?
		          AND start_time < NOW()
		          AND deleted_at IS NULL
		        GROUP BY host_user_id
		  ) rc ON rc.host_user_id = h.id
		  LEFT JOIN (
		       SELECT rated_user_id, AVG(stars)::float8 AS average, COUNT(*) AS count
		         FROM ride_ratings
		        WHERE rated_user_id IN ?
		          AND deleted_at IS NULL
		        GROUP BY rated_user_id
		  ) rr ON rr.rated_user_id = h.id
		 WHERE h.id IN ?
	`, hostIDs, hostIDs, hostIDs).Scan(&rows).Error; err != nil {
		log.Printf("host search stats lookup failed (continuing without): %v", err)
		return out
	}

	for _, row := range rows {
		out[row.HostUserID] = searchHostStats{
			RideCount: row.RideCount,
			Average:   row.Average,
			Count:     row.Count,
		}
	}
	return out
}

func searchRowsToRides(rows []searchRideRow) []models.Ride {
	rides := make([]models.Ride, 0, len(rows))
	for _, row := range rows {
		rides = append(rides, models.Ride{
			BaseModel: models.BaseModel{
				ID:        row.ID,
				CreatedAt: row.CreatedAt,
			},
			HostUserID:     row.HostUserID,
			StartLocation:  row.StartLocation,
			EndLocation:    row.EndLocation,
			StartLatitude:  row.StartLatitude,
			StartLongitude: row.StartLongitude,
			EndLatitude:    row.EndLatitude,
			EndLongitude:   row.EndLongitude,
			StartTime:      row.StartTime,
			TotalSeats:     row.TotalSeats,
			BookedSeats:    row.BookedSeats,
			TotalPrice:     row.TotalPrice,
			IsSameGender:   row.IsSameGender,
			HostUser: models.User{
				BaseModel:         models.BaseModel{ID: row.HostUserID},
				Name:              row.HostUserName,
				ProfilePictureURL: row.HostProfilePictureURL,
				YOB:               row.HostUserYOB,
				Gender:            row.HostUserGender,
			},
		})
	}
	return rides
}

const defaultSearchCandidateOrder = "rides.start_time ASC"

func loadSearchRideCandidates(tx *gorm.DB, limit, offset int, orderBy string) ([]models.Ride, error) {
	var rows []searchRideRow
	if strings.TrimSpace(orderBy) == "" {
		orderBy = defaultSearchCandidateOrder
	}
	err := tx.
		Select(`
				rides.id,
			rides.created_at,
			rides.host_user_id,
			rides.start_location,
			rides.end_location,
			rides.start_latitude,
			rides.start_longitude,
			rides.end_latitude,
			rides.end_longitude,
			rides.start_time,
			rides.total_seats,
			rides.booked_seats,
			rides.total_price,
			rides.is_same_gender,
			u.name AS host_user_name,
			u.profile_picture_url AS host_profile_picture_url,
			u.yob AS host_user_yob,
			u.gender AS host_user_gender
			`).
		Joins("JOIN users u ON u.id = rides.host_user_id").
		Order(orderBy).
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return searchRowsToRides(rows), nil
}

func coordinateDistanceKmOrderSQL(latCol, lonCol string, lat, lon float64) string {
	cosLat := strictMatchCosLat(lat)
	return fmt.Sprintf(`
		111.32::float8 * SQRT(
			((%s::float8 - %.8f::float8) * (%s::float8 - %.8f::float8)) +
			(((%s::float8 - %.8f::float8) * %.8f::float8) * ((%s::float8 - %.8f::float8) * %.8f::float8))
		)
	`, latCol, lat, latCol, lat, lonCol, lon, cosLat, lonCol, lon, cosLat)
}

func coordinateDistanceKmOrderTerm(latCol, lonCol string, lat, lon float64) string {
	return "COALESCE(" + coordinateDistanceKmOrderSQL(latCol, lonCol, lat, lon) + ", 999999999)"
}

func searchCandidateOrder(params SearchParams, coordinateOrder bool) string {
	if !coordinateOrder {
		return defaultSearchCandidateOrder
	}

	terms := []string{}
	if params.HasStartCoord {
		terms = append(terms, coordinateDistanceKmOrderTerm("rides.start_latitude", "rides.start_longitude", params.StartLat, params.StartLon))
	}
	if params.HasEndCoord {
		terms = append(terms, coordinateDistanceKmOrderTerm("rides.end_latitude", "rides.end_longitude", params.EndLat, params.EndLon))
	}

	switch len(terms) {
	case 0:
		return defaultSearchCandidateOrder
	case 1:
		return terms[0] + " ASC, rides.start_time ASC"
	default:
		return fmt.Sprintf("(%s + %s) ASC, %s ASC, rides.start_time ASC", terms[0], terms[1], terms[1])
	}
}

func isFiniteCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func parseTimeWindow(dateStr string, timeStr string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.UTC
	}

	if dateStr == "" {
		// Default to today and next 7 days
		now := time.Now().In(loc)
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end := start.Add(7 * 24 * time.Hour)
		return start.UTC(), end.UTC(), nil
	}

	date, err := time.ParseInLocation("2006-01-02", dateStr, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	// If time specified, create a window around it
	if timeStr != "" {
		targetTime, err := time.ParseInLocation("15:04", timeStr, loc)
		if err == nil {
			target := time.Date(date.Year(), date.Month(), date.Day(),
				targetTime.Hour(), targetTime.Minute(), 0, 0, loc)
			// ±2 hour window around target time
			start := target.Add(-2 * time.Hour)
			end := target.Add(2 * time.Hour)
			return start.UTC(), end.UTC(), nil
		}
	}

	// Full day
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	end := start.Add(24*time.Hour - time.Nanosecond)
	return start.UTC(), end.UTC(), nil
}

func parseSearchTargetTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	loc := searchLocation()
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", raw, loc); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", raw, loc); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func searchTimeWindowPreference(params SearchParams) string {
	if params.HasTargetTime {
		return ""
	}
	return params.PreferredTime
}

func buildLocationQuery(tx *gorm.DB, location string, hasCoord bool, lat, lon float64, radiusMeters float64, isStart bool) *gorm.DB {
	if hasCoord {
		return applyCoordinateRadiusFilter(tx, lat, lon, radiusMeters, isStart)
	}

	if location == "" {
		return tx
	}

	normalizedLocation := strings.TrimSpace(strings.ToLower(location))
	locationColumn := "start_location"
	if !isStart {
		locationColumn = "end_location"
	}

	if searchDebugLogs {
		log.Printf("Text search on %s for: %s", locationColumn, normalizedLocation)
	}

	words := strings.Fields(normalizedLocation)

	conditions := []string{
		fmt.Sprintf("%s ILIKE ?", locationColumn), // exact match
		fmt.Sprintf("%s ILIKE ?", locationColumn), // contains match
	}
	values := []interface{}{
		normalizedLocation,
		"%" + normalizedLocation + "%",
	}

	for _, word := range words {
		if len(word) > 2 {
			conditions = append(conditions, fmt.Sprintf("%s ILIKE ?", locationColumn))
			values = append(values, "%"+word+"%")
		}
	}

	if len(words) > 0 {
		conditions = append(conditions, fmt.Sprintf("similarity(%s, ?) > ?", locationColumn))
		values = append(values, location, 0.2)
	}

	whereClause := strings.Join(conditions, " OR ")
	if searchDebugLogs {
		log.Printf("Location WHERE clause: (%s)", whereClause)
	}

	return tx.Where(fmt.Sprintf("(%s)", whereClause), values...)
}

func applyCoordinateRadiusFilter(tx *gorm.DB, lat, lon, radiusMeters float64, isStart bool) *gorm.DB {
	latCol := "start_latitude"
	lonCol := "start_longitude"
	if !isStart {
		latCol = "end_latitude"
		lonCol = "end_longitude"
	}

	filterRadiusMeters := conservativeCoordinateRadius(radiusMeters)
	bounds := boundsForNearby(lat, lon, filterRadiusMeters)
	cosLat := strictMatchCosLat(lat)
	radiusDegreesSq := strictMatchRadiusDegreesSq(filterRadiusMeters)

	return tx.
		Where(fmt.Sprintf("%s IS NOT NULL AND %s IS NOT NULL", lonCol, latCol)).
		Where(fmt.Sprintf("%s BETWEEN ? AND ?", latCol), bounds.minLat, bounds.maxLat).
		Where(fmt.Sprintf("%s BETWEEN ? AND ?", lonCol), bounds.minLng, bounds.maxLng).
		Where(fmt.Sprintf(`
			(
				((%s::float8 - ?::float8) * (%s::float8 - ?::float8)) +
				(((%s::float8 - ?::float8) * ?::float8) * ((%s::float8 - ?::float8) * ?::float8))
			) <= ?::float8
		`, latCol, latCol, lonCol, lonCol), lat, lat, lon, cosLat, lon, cosLat, radiusDegreesSq)
}

func coordinateDistanceKmExpression(latCol, lonCol string) string {
	return fmt.Sprintf(`
		111.32::float8 * SQRT(
			((%s::float8 - ?::float8) * (%s::float8 - ?::float8)) +
			(((%s::float8 - ?::float8) * ?::float8) * ((%s::float8 - ?::float8) * ?::float8))
		)
	`, latCol, latCol, lonCol, lonCol)
}

func appendCoordinateDistanceArgs(args []interface{}, lat, lon float64) []interface{} {
	cosLat := strictMatchCosLat(lat)
	return append(args, lat, lat, lon, cosLat, lon, cosLat)
}

func searchWithAdaptiveRadius(tx *gorm.DB, startLat, startLon, endLat, endLon float64, hasStartCoord, hasEndCoord bool) (*gorm.DB, float64) {
	if !hasStartCoord && !hasEndCoord {
		return tx, 0
	}

	radiusOptions := []float64{5000, 10000, 20000, 50000} // 5km, 10km, 20km, 50km

	for _, radius := range radiusOptions {
		var count int64
		testTx := database.Database.Db.Model(&models.Ride{}).
			Where(helpers.PassengerSeatsLeftPredicate + " AND start_time > NOW()")

		if hasStartCoord {
			testTx = applyCoordinateRadiusFilter(testTx, startLat, startLon, radius, true)
		}
		if hasEndCoord {
			testTx = applyCoordinateRadiusFilter(testTx, endLat, endLon, radius, false)
		}

		testTx.Count(&count)
		if count >= 5 { // Found enough results
			finalTx := tx
			if hasStartCoord {
				finalTx = applyCoordinateRadiusFilter(finalTx, startLat, startLon, radius, true)
			}
			if hasEndCoord {
				finalTx = applyCoordinateRadiusFilter(finalTx, endLat, endLon, radius, false)
			}
			return finalTx, radius / 1000 // Return radius in km
		}
	}

	// Fallback to largest radius
	finalTx := tx
	if hasStartCoord {
		finalTx = applyCoordinateRadiusFilter(finalTx, startLat, startLon, 50000, true)
	}
	if hasEndCoord {
		finalTx = applyCoordinateRadiusFilter(finalTx, endLat, endLon, 50000, false)
	}
	return finalTx, 50.0
}

// addMatchContext kept for backwards-compatibility with callers that don't
// have host frequency. New caller path uses addMatchContextWithHost.
func addMatchContext(card *RideCard, params SearchParams) {
	addMatchContextWithHost(card, params, 0, false)
}

func addMatchContextWithHost(card *RideCard, params SearchParams, hostPastRides int64, repeatRoute bool) {
	reasons := []string{}

	if strings.TrimSpace(card.MatchReason) != "" {
		reasons = append(reasons, card.MatchReason)
	}

	// "Repeat route" is the strongest signal we have — the user already
	// took this exact origin → destination before. Goes to the top.
	if repeatRoute {
		reasons = append(reasons, "Your usual route")
	}

	// "Just listed" wins the top slot when there's no repeat-route — fresh
	// activity is the strongest social signal in a peer-to-peer carpool
	// board. 60-minute window so newly posted rides surface quickly
	// without becoming noise.
	if !card.CreatedAt.IsZero() && time.Since(card.CreatedAt) < 60*time.Minute {
		reasons = append(reasons, "Just listed")
	}

	// Trusted host = ≥3 past rides. Capped wording: "Trusted host" is the
	// strongest social-proof phrase a stranger-to-stranger marketplace
	// can credibly use without ratings infrastructure.
	if hostPastRides >= 10 {
		reasons = append(reasons, "Top host")
	} else if hostPastRides >= 3 {
		reasons = append(reasons, "Trusted host")
	}

	if card.StartDistance != nil && *card.StartDistance < 2.0 {
		reasons = append(reasons, "Very close to pickup")
	} else if card.StartDistance != nil && *card.StartDistance < 5.0 {
		reasons = append(reasons, "Close to pickup")
	}

	if card.EndDistance != nil && *card.EndDistance < 2.0 {
		reasons = append(reasons, "Very close to destination")
	} else if card.EndDistance != nil && *card.EndDistance < 5.0 {
		reasons = append(reasons, "Close to destination")
	}

	availableSeats := helpers.PassengerSeatsLeft(card.TotalSeats, card.BookedSeats)
	if availableSeats > 2 {
		reasons = append(reasons, "Multiple seats available")
	} else if availableSeats > 1 {
		reasons = append(reasons, "2 seats available")
	}

	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	rideTime := card.StartTime.In(loc)

	nowDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	rideDateStart := time.Date(rideTime.Year(), rideTime.Month(), rideTime.Day(), 0, 0, 0, 0, loc)
	tomorrowDate := nowDate.Add(24 * time.Hour)

	timeDiff := card.StartTime.Sub(now)

	if rideDateStart.Equal(nowDate) {
		if timeDiff.Hours() > 1 && timeDiff.Hours() < 6 {
			reasons = append(reasons, "Good timing today")
		} else if timeDiff.Hours() >= 0.5 {
			reasons = append(reasons, "Today's ride")
		} else if timeDiff.Hours() > 0 {
			reasons = append(reasons, "Leaving soon")
		}
	} else if rideDateStart.Equal(tomorrowDate) {
		reasons = append(reasons, "Tomorrow's ride")
	} else if timeDiff.Hours() > 0 && timeDiff.Hours() <= 72 {
		reasons = append(reasons, "This week")
	}

	if params.MaxPrice != nil && card.TotalPrice <= *params.MaxPrice/2 {
		reasons = append(reasons, "Great price")
	} else if params.MaxPrice != nil && card.TotalPrice <= *params.MaxPrice {
		reasons = append(reasons, "Within budget")
	}

	if len(reasons) > 0 {
		card.MatchReason = strings.Join(reasons, ", ")
	}
}

func parseSearchParams(c *fiber.Ctx) (SearchParams, error) {
	params := SearchParams{}

	params.StartLocation = c.Query("start_location")
	params.EndLocation = c.Query("end_location")
	params.Date = c.Query("date")
	params.PreferredTime = c.Query("preferred_time")
	params.SortBy = c.Query("sort_by", "relevance")

	if t, ok := parseSearchTargetTime(c.Query("target_time")); ok {
		params.TargetTime = t
		params.HasTargetTime = true
	} else if t, ok := parseSearchTargetTime(c.Query("start_time")); ok {
		params.TargetTime = t
		params.HasTargetTime = true
	}
	if params.HasTargetTime && params.Date == "" {
		params.Date = params.TargetTime.In(searchLocation()).Format("2006-01-02")
	}

	limitStr := c.Query("limit", "20")
	offsetStr := c.Query("offset", "0")

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 1 {
		params.Limit = 20
	} else if limit > 50 {
		params.Limit = 50
	} else {
		params.Limit = limit
	}

	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		params.Offset = 0
	} else {
		params.Offset = offset
	}

	if latStr, lonStr := c.Query("start_lat"), c.Query("start_lon"); latStr != "" && lonStr != "" {
		if v, err := strconv.ParseFloat(latStr, 64); err == nil && isFiniteCoordinate(v) {
			params.StartLat = v
			if v2, err := strconv.ParseFloat(lonStr, 64); err == nil && isFiniteCoordinate(v2) {
				params.StartLon = v2
				params.HasStartCoord = true
			}
		}
	}

	if latStr, lonStr := c.Query("end_lat"), c.Query("end_lon"); latStr != "" && lonStr != "" {
		if v, err := strconv.ParseFloat(latStr, 64); err == nil && isFiniteCoordinate(v) {
			params.EndLat = v
			if v2, err := strconv.ParseFloat(lonStr, 64); err == nil && isFiniteCoordinate(v2) {
				params.EndLon = v2
				params.HasEndCoord = true
			}
		}
	}

	params.RadiusKm = 10.0 // default
	if rStr := c.Query("radius"); rStr != "" {
		if v, err := strconv.ParseFloat(rStr, 64); err == nil && v > 0 {
			params.RadiusKm = v
		}
	}

	if maxPriceStr := c.Query("max_price"); maxPriceStr != "" {
		if v, err := strconv.ParseUint(maxPriceStr, 10, 32); err == nil {
			maxPrice := uint(v)
			params.MaxPrice = &maxPrice
		}
	}

	if minSeatsStr := c.Query("min_seats"); minSeatsStr != "" {
		if v, err := strconv.ParseUint(minSeatsStr, 10, 32); err == nil {
			minSeats := uint(v)
			params.MinSeats = &minSeats
		}
	}

	// Search is a public endpoint via OptionalAuthenticate, so a
	// missing or invalid user is fine — we treat the caller as a
	// guest. Downstream:
	//   - WHERE host_user_id != uuid.Nil matches every real ride
	//     (no host has a nil ID), so guests see the full catalogue.
	//   - LoadViewerBookings short-circuits on viewerID == uuid.Nil
	//     and returns an empty map.
	//   - ResolveViewerState falls through to StateAvailable for a
	//     nil viewer, which is exactly what we want a guest to see
	//     on every result.
	if userIntf := c.Locals("user"); userIntf != nil {
		if user, ok := userIntf.(models.User); ok {
			params.User = user
		}
	}

	return params, nil
}

func normalizeGender(gender string) string {
	return strings.ToLower(strings.TrimSpace(gender))
}

func SearchRides(c *fiber.Ctx) error {
	params, err := parseSearchParams(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
	}

	if searchDebugLogs {
		log.Printf("Search params: StartLocation=%s, EndLocation=%s, StartCoord=(%f,%f), EndCoord=(%f,%f), HasStartCoord=%v, HasEndCoord=%v",
			params.StartLocation, params.EndLocation, params.StartLat, params.StartLon, params.EndLat, params.EndLon, params.HasStartCoord, params.HasEndCoord)
	}

	if searchDebugLogs && (params.HasStartCoord || params.HasEndCoord) {
		var nearestRides []struct {
			StartLocation  string   `json:"start_location"`
			EndLocation    string   `json:"end_location"`
			StartLatitude  *float64 `json:"start_latitude"`
			StartLongitude *float64 `json:"start_longitude"`
			EndLatitude    *float64 `json:"end_latitude"`
			EndLongitude   *float64 `json:"end_longitude"`
			StartDistance  *float64 `json:"start_distance"`
			EndDistance    *float64 `json:"end_distance"`
		}
		var queryArgs []interface{}

		query := `
			SELECT 
				start_location, 
				end_location, 
				start_latitude, 
				start_longitude, 
				end_latitude, 
				end_longitude`

		if params.HasStartCoord {
			query += `, ` + coordinateDistanceKmExpression("start_latitude", "start_longitude") + ` as start_distance`
			queryArgs = appendCoordinateDistanceArgs(queryArgs, params.StartLat, params.StartLon)
		} else {
			query += `, NULL as start_distance`
		}

		if params.HasEndCoord {
			query += `, ` + coordinateDistanceKmExpression("end_latitude", "end_longitude") + ` as end_distance`
			queryArgs = appendCoordinateDistanceArgs(queryArgs, params.EndLat, params.EndLon)
		} else {
			query += `, NULL as end_distance`
		}

		// Raw SQL fragment — can't use a Go const inside a string literal
		// without `+`, so inline the predicate here. Mirrors
		// helpers.PassengerSeatsLeftPredicate exactly.
		query += `
			FROM rides
			WHERE booked_seats < total_seats - 1
				AND start_time > NOW()
				AND (start_latitude IS NOT NULL OR end_latitude IS NOT NULL)
			ORDER BY `

		if params.HasStartCoord && params.HasEndCoord {
			query += `LEAST(
				COALESCE(` + coordinateDistanceKmExpression("start_latitude", "start_longitude") + `, 999999999),
				COALESCE(` + coordinateDistanceKmExpression("end_latitude", "end_longitude") + `, 999999999)
			) ASC`
			queryArgs = appendCoordinateDistanceArgs(queryArgs, params.StartLat, params.StartLon)
			queryArgs = appendCoordinateDistanceArgs(queryArgs, params.EndLat, params.EndLon)
		} else if params.HasStartCoord {
			query += coordinateDistanceKmExpression("start_latitude", "start_longitude") + ` ASC`
			queryArgs = appendCoordinateDistanceArgs(queryArgs, params.StartLat, params.StartLon)
		} else if params.HasEndCoord {
			query += coordinateDistanceKmExpression("end_latitude", "end_longitude") + ` ASC`
			queryArgs = appendCoordinateDistanceArgs(queryArgs, params.EndLat, params.EndLon)
		}

		query += ` LIMIT 5`

		if err := database.Database.Db.Raw(query, queryArgs...).Scan(&nearestRides).Error; err == nil && len(nearestRides) > 0 {
			log.Printf("=== NEAREST RIDES IN DATABASE ===")
			for i, ride := range nearestRides {
				log.Printf("  %d. Start: %s (%.6f, %.6f) End: %s (%.6f, %.6f)",
					i+1,
					ride.StartLocation,
					func() float64 {
						if ride.StartLatitude != nil {
							return *ride.StartLatitude
						} else {
							return 0
						}
					}(),
					func() float64 {
						if ride.StartLongitude != nil {
							return *ride.StartLongitude
						} else {
							return 0
						}
					}(),
					ride.EndLocation,
					func() float64 {
						if ride.EndLatitude != nil {
							return *ride.EndLatitude
						} else {
							return 0
						}
					}(),
					func() float64 {
						if ride.EndLongitude != nil {
							return *ride.EndLongitude
						} else {
							return 0
						}
					}())

				if ride.StartDistance != nil {
					log.Printf("     Start distance: %.2f km", *ride.StartDistance)
				}
				if ride.EndDistance != nil {
					log.Printf("     End distance: %.2f km", *ride.EndDistance)
				}
			}
			log.Printf("=== END NEAREST RIDES ===")
		} else {
			log.Printf("No rides found in database with coordinates or error: %v", err)
		}
	}

	usedRadius := params.RadiusKm
	var (
		dateStart           time.Time
		dateEnd             time.Time
		hasDateFilter       bool
		useCoordinateSearch bool
	)

	if params.Date != "" {
		startTime, endTime, err := parseTimeWindow(params.Date, searchTimeWindowPreference(params))
		if err != nil {
			log.Printf("Error parsing date %q: %v\n", params.Date, err)
			return c.Status(400).JSON(fiber.Map{"error": "Invalid date format; expected YYYY-MM-DD"})
		}
		dateStart = startTime
		dateEnd = endTime
		hasDateFilter = true
	}

	buildBaseSearchTx := func() *gorm.DB {
		tx := database.Database.Db.
			Model(&models.Ride{}).
			Where(helpers.PassengerSeatsLeftPredicate+" AND start_time > NOW()").
			Where("host_user_id != ?", params.User.ID)

		if hasDateFilter {
			tx = tx.Where("start_time BETWEEN ? AND ?", dateStart, dateEnd)
		}
		if params.MaxPrice != nil {
			tx = tx.Where("total_price <= ?", *params.MaxPrice)
		}
		if params.MinSeats != nil {
			tx = tx.Where(helpers.MinSeatsLeftPredicate, *params.MinSeats)
		}
		return tx
	}

	candidateLimit := params.Limit * 8
	if candidateLimit < 80 {
		candidateLimit = 80
	}
	if candidateLimit > 250 {
		candidateLimit = 250
	}

	rides := make([]models.Ride, 0, candidateLimit)
	seenRideIDs := make(map[uuid.UUID]bool, candidateLimit)
	appendUniqueRides := func(next []models.Ride) {
		for _, ride := range next {
			if seenRideIDs[ride.ID] {
				continue
			}
			seenRideIDs[ride.ID] = true
			rides = append(rides, ride)
		}
	}

	ranCandidateSearch := false
	var textSearchErr error

	if params.HasStartCoord || params.HasEndCoord {
		coordinateTx := buildBaseSearchTx()

		if params.HasStartCoord {
			coordinateTx = applyCoordinateRadiusFilter(coordinateTx, params.StartLat, params.StartLon, usedRadius*1000, true)
		}
		if params.HasEndCoord && !params.HasStartCoord {
			coordinateTx = applyCoordinateRadiusFilter(coordinateTx, params.EndLat, params.EndLon, usedRadius*1000, false)
		}

		if loaded, err := loadSearchRideCandidates(
			coordinateTx,
			candidateLimit,
			params.Offset,
			searchCandidateOrder(params, true),
		); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("Database error: %v", err)
			return c.Status(500).JSON(fiber.Map{"error": "Error fetching rides"})
		} else {
			appendUniqueRides(loaded)
		}
		ranCandidateSearch = true
		useCoordinateSearch = true
	}

	if params.StartLocation != "" || params.EndLocation != "" {
		textTx := buildBaseSearchTx()

		if params.StartLocation != "" {
			textTx = buildLocationQuery(textTx, params.StartLocation, false, 0, 0, 0, true)
		}
		if params.EndLocation != "" {
			textTx = buildLocationQuery(textTx, params.EndLocation, false, 0, 0, 0, false)
		}

		if loaded, err := loadSearchRideCandidates(
			textTx,
			candidateLimit,
			params.Offset,
			searchCandidateOrder(params, false),
		); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			textSearchErr = err
			log.Printf("Text search error: %v", err)
		} else {
			appendUniqueRides(loaded)
		}
		ranCandidateSearch = true
	}

	if !ranCandidateSearch {
		if loaded, err := loadSearchRideCandidates(
			buildBaseSearchTx(),
			candidateLimit,
			params.Offset,
			searchCandidateOrder(params, false),
		); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("Database error: %v", err)
			return c.Status(500).JSON(fiber.Map{"error": "Error fetching rides"})
		} else {
			appendUniqueRides(loaded)
		}
	}

	if searchDebugLogs {
		log.Printf("Query returned %d rides", len(rides))
	}

	if len(rides) == 0 && (params.StartLocation != "" || params.EndLocation != "") {
		if searchDebugLogs {
			log.Printf("No results found, trying fallback search...")
		}

		fallbackTx := buildBaseSearchTx()

		if params.StartLocation != "" {
			words := strings.Fields(strings.ToLower(params.StartLocation))
			for _, word := range words {
				if len(word) > 3 {
					fallbackTx = fallbackTx.Where("LOWER(start_location) LIKE ?", "%"+word+"%")
					break
				}
			}
		}

		if params.EndLocation != "" {
			words := strings.Fields(strings.ToLower(params.EndLocation))
			for _, word := range words {
				if len(word) > 3 {
					fallbackTx = fallbackTx.Where("LOWER(end_location) LIKE ?", "%"+word+"%")
					break
				}
			}
		}

		if loaded, err := loadSearchRideCandidates(
			fallbackTx,
			candidateLimit,
			params.Offset,
			searchCandidateOrder(params, false),
		); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("Fallback search error: %v", err)
			if textSearchErr != nil {
				return c.Status(500).JSON(fiber.Map{"error": "Error fetching rides"})
			}
		} else if searchDebugLogs {
			appendUniqueRides(loaded)
			log.Printf("Fallback search returned %d rides", len(rides))
		} else {
			appendUniqueRides(loaded)
		}
	}

	// The three enrichment reads below are independent:
	//   - user's past route pairs for repeat-route boosts
	//   - host ride/rating stats for trust signals
	//   - viewer bookings for CTA state
	// Running them as one fan-out keeps search latency closer to the
	// slowest query instead of the sum of all three.
	pastRoutes := make(map[string]bool)
	hostStats := make(map[uuid.UUID]searchHostStats)
	viewerBookings := map[uuid.UUID]*models.Booking{}
	externalRides := FetchExternalRidesForSearch(ExternalRideSearchParams{
		StartLocation: params.StartLocation,
		EndLocation:   params.EndLocation,
		StartLat:      params.StartLat,
		StartLon:      params.StartLon,
		EndLat:        params.EndLat,
		EndLon:        params.EndLon,
		HasStartCoord: params.HasStartCoord,
		HasEndCoord:   params.HasEndCoord,
		RadiusKm:      usedRadius,
		HasDateFilter: hasDateFilter,
		DateStart:     dateStart,
		DateEnd:       dateEnd,
		MaxPrice:      params.MaxPrice,
		MinSeats:      params.MinSeats,
	})

	hostIDs := make([]uuid.UUID, 0, len(rides))
	rideIDs := make([]uuid.UUID, 0, len(rides))
	seenHost := make(map[uuid.UUID]bool, len(rides))
	for _, r := range rides {
		rideIDs = append(rideIDs, r.ID)
		if !seenHost[r.HostUserID] {
			hostIDs = append(hostIDs, r.HostUserID)
			seenHost[r.HostUserID] = true
		}
	}

	var enrichWG sync.WaitGroup
	enrichWG.Add(3)
	go func() {
		defer enrichWG.Done()
		if params.User.ID == uuid.Nil {
			return
		}
		type routePair struct {
			StartLocation string `json:"start_location"`
			EndLocation   string `json:"end_location"`
		}
		var pairs []routePair
		if err := database.Database.Db.Raw(`
			SELECT start_location, end_location FROM rides
			WHERE host_user_id = ?
			  AND start_time < NOW()
			  AND deleted_at IS NULL
			UNION
			SELECT r.start_location, r.end_location FROM bookings b
			JOIN rides r ON r.id = b.ride_id
			WHERE b.passenger_id = ?
			  AND b.request_status = 'accepted'
			  AND r.deleted_at IS NULL
			LIMIT 200
		`, params.User.ID, params.User.ID).Scan(&pairs).Error; err != nil {
			log.Printf("past-routes lookup failed (continuing without): %v", err)
			return
		}
		next := make(map[string]bool, len(pairs))
		for _, p := range pairs {
			key := strings.ToLower(strings.TrimSpace(p.StartLocation)) + "|" +
				strings.ToLower(strings.TrimSpace(p.EndLocation))
			next[key] = true
		}
		pastRoutes = next
	}()
	go func() {
		defer enrichWG.Done()
		hostStats = loadSearchHostStats(hostIDs)
	}()
	go func() {
		defer enrichWG.Done()
		// Batch-load the caller's bookings for *every* ride in this
		// result set so the viewer-state resolver below doesn't
		// trigger a per-ride query. Single round-trip even for 50
		// results.
		viewerBookings = LoadViewerBookings(params.User.ID, rideIDs)
	}()
	enrichWG.Wait()

	response := make([]RideCard, 0, len(rides))
	now := time.Now()
	for _, ride := range rides {
		card := RideCard{
			RideID:                    ride.ID,
			HostUserID:                ride.HostUserID,
			HostUserName:              ride.HostUser.Name,
			HostUserProfilePictureURL: ride.HostUser.ProfilePictureURL,
			HostUserYOB:               ride.HostUser.YOB,
			HostUserGender:            ride.HostUser.Gender,
			SameGenderFemale:          normalizeGender(params.User.Gender) == "female" && normalizeGender(ride.HostUser.Gender) == "female",
			HostRatingAverage:         hostStats[ride.HostUserID].Average,
			HostRatingCount:           hostStats[ride.HostUserID].Count,
			StartLocation:             ride.StartLocation,
			EndLocation:               ride.EndLocation,
			StartTime:                 ride.StartTime,
			CreatedAt:                 ride.CreatedAt,
			TotalSeats:                ride.TotalSeats,
			BookedSeats:               ride.BookedSeats,
			TotalPrice:                ride.TotalPrice,
			StartLatitude:             ride.StartLatitude,
			StartLongitude:            ride.StartLongitude,
			EndLatitude:               ride.EndLatitude,
			EndLongitude:              ride.EndLongitude,
		}

		// Annotate with server-computed viewer state so the search
		// results list can render "Request seat" / "Pending" /
		// "Confirmed" / "Hosting" per card without re-deriving.
		rideRef := ride
		viewerCtx := ResolveViewerState(&rideRef, params.User.ID, viewerBookings[ride.ID])
		card.ViewerState = viewerCtx.State
		card.Actions = viewerCtx.Actions
		card.ViewerBookingID = viewerCtx.BookingID

		var startDist, endDist *float64
		if params.HasStartCoord && helpers.AreCoordinatesValid(ride.StartLatitude, ride.StartLongitude) {
			d := helpers.CalculateDistance(params.StartLat, params.StartLon, *ride.StartLatitude, *ride.StartLongitude)
			card.StartDistance = &d
			startDist = &d
		}
		if params.HasEndCoord && helpers.AreCoordinatesValid(ride.EndLatitude, ride.EndLongitude) {
			d := helpers.CalculateDistance(params.EndLat, params.EndLon, *ride.EndLatitude, *ride.EndLongitude)
			card.EndDistance = &d
			endDist = &d
		}
		if card.StartDistance != nil && card.EndDistance != nil {
			total := *card.StartDistance + *card.EndDistance
			card.TotalDistance = &total
		} else if card.StartDistance != nil {
			card.TotalDistance = card.StartDistance
		} else if card.EndDistance != nil {
			card.TotalDistance = card.EndDistance
		}

		scored := scoreRideForSearch(ride, params, startDist, endDist)
		if useCoordinateSearch {
			if params.HasStartCoord {
				startTextMatch := scoreTextFit(params.StartLocation, ride.StartLocation, 1) > 0
				if (startDist == nil || *startDist > params.RadiusKm) && !startTextMatch {
					continue
				}
			}
			if params.HasEndCoord {
				destinationNear := endDist != nil && *endDist <= params.RadiusKm
				destinationTextMatch := scoreTextFit(params.EndLocation, ride.EndLocation, 1) > 0
				if !destinationNear && scored.RouteOverlapScore <= 0 && !destinationTextMatch {
					continue
				}
			}
		}

		card.RelevanceScore = scored.Score
		card.MatchReason = scored.MatchReason

		// Mild relevance boost for proven hosts so users see them first
		// when sorting by relevance. Capped so it never dominates
		// distance/time factors.
		if past := hostStats[ride.HostUserID].RideCount; past > 0 {
			if past >= 10 {
				card.RelevanceScore += 12
			} else if past >= 3 {
				card.RelevanceScore += 6
			}
		}

		// Repeat-route boost. If the user has taken this exact origin →
		// destination pair before (either hosted or booked + accepted),
		// nudge it up the list. Strongest single signal that this ride
		// fits the user's life.
		routeKey := strings.ToLower(strings.TrimSpace(ride.StartLocation)) + "|" +
			strings.ToLower(strings.TrimSpace(ride.EndLocation))
		repeatRoute := pastRoutes[routeKey]
		if repeatRoute {
			card.RelevanceScore += 18
		}

		addMatchContextWithHost(&card, params, hostStats[ride.HostUserID].RideCount, repeatRoute)

		// Structured signal list — chip rows the client renders under the
		// card. Built from the same scoring/context inputs but as
		// machine-parseable objects rather than a comma-joined string.
		card.MatchSignals = buildMatchSignals(
			scored,
			ride,
			params,
			startDist,
			endDist,
			hostStats[ride.HostUserID].RideCount,
			repeatRoute,
			now,
		)

		response = append(response, card)
	}

	// Sort by preference
	switch params.SortBy {
	case "time":
		sort.Slice(response, func(i, j int) bool {
			return response[i].StartTime.Before(response[j].StartTime)
		})
	case "price":
		sort.Slice(response, func(i, j int) bool {
			return response[i].TotalPrice < response[j].TotalPrice
		})
	case "distance":
		sort.Slice(response, func(i, j int) bool {
			if response[i].TotalDistance == nil && response[j].TotalDistance == nil {
				return false
			}
			if response[i].TotalDistance == nil {
				return false
			}
			if response[j].TotalDistance == nil {
				return true
			}
			return *response[i].TotalDistance < *response[j].TotalDistance
		})
	default: // "relevance"
		sort.Slice(response, func(i, j int) bool {
			if response[i].RelevanceScore != response[j].RelevanceScore {
				return response[i].RelevanceScore > response[j].RelevanceScore
			}
			return response[i].StartTime.Before(response[j].StartTime)
		})
	}

	if len(response) > params.Limit {
		response = response[:params.Limit]
	}
	externalRides = limitExternalRides(externalRides, params.Limit-len(response))

	// Strict matches: surfaced alongside the regular fuzzy results
	// so clients can render a "best match" badge on overlapping
	// rows (or, in CreateRide's case, drive the "you could just
	// join one of these" prompt by reading only this field).
	// Computed only when both endpoints carry coords — strict-mode
	// is a pure-geo gate, there's nothing to match on without
	// concrete points. Failures are logged and swallowed so the
	// search response shape is always stable for the client.
	strictMatches := []MatchingRideSummary{}
	if params.HasStartCoord && params.HasEndCoord {
		strictCtx, strictCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer strictCancel()
		var excludeID uuid.UUID
		if params.User.ID != (uuid.UUID{}) {
			excludeID = params.User.ID
		}
		startTime := time.Now().Add(time.Hour)
		if params.HasTargetTime {
			startTime = params.TargetTime
		} else if params.Date != "" {
			if t, err := time.ParseInLocation("2006-01-02", params.Date, searchLocation()); err == nil {
				startTime = t.Add(12 * time.Hour)
			}
		}
		matches, mErr := FindStrictRouteMatches(strictCtx, StrictMatchParams{
			StartLat:      params.StartLat,
			StartLon:      params.StartLon,
			EndLat:        params.EndLat,
			EndLon:        params.EndLon,
			StartTime:     startTime,
			ExcludeUserID: excludeID,
		})
		if mErr != nil {
			log.Printf("SearchRides: strict match probe failed: %v", mErr)
		} else {
			strictMatches = matches
		}
	}

	return c.Status(200).JSON(fiber.Map{
		"rides":          response,
		"external_rides": externalRides,
		"strict_matches": strictMatches,
		"total_found":    len(response) + len(externalRides),
		"used_radius_km": usedRadius,
		"sort_by":        params.SortBy,
		"search_method": func() string {
			if useCoordinateSearch {
				return "coordinate"
			}
			return "text"
		}(),
		"debug": fiber.Map{
			"has_start_coord": params.HasStartCoord,
			"has_end_coord":   params.HasEndCoord,
			"start_location":  params.StartLocation,
			"end_location":    params.EndLocation,
			"target_time":     params.TargetTime,
			"has_target_time": params.HasTargetTime,
		},
	})
}
