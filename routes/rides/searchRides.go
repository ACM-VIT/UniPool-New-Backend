package rides

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RideCard struct {
	RideID                    uuid.UUID `json:"id"`
	HostUserID                uuid.UUID `json:"host_user_id"`
	HostUserName              string    `json:"host_user_name"`
	HostUserProfilePictureURL string    `json:"host_user_profile_picture_url"`
	HostUserYOB               uint      `json:"host_user_yob,omitempty"`
	// HostUserGender lets the client surface a same-gender affinity
	// signal (e.g. soft pink tint when a female passenger searches and
	// the host is also female). Omit when unset to avoid leaking blanks.
	HostUserGender   string    `json:"host_user_gender,omitempty"`
	SameGenderFemale bool      `json:"same_gender_female,omitempty"`
	StartLocation    string    `json:"start_location"`
	EndLocation      string    `json:"end_location"`
	StartTime        time.Time `json:"start_time"`
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
	MaxPrice      *uint
	MinSeats      *uint
	SortBy        string
	RadiusKm      float64
	Limit         int
	Offset        int
	User          models.User
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

func buildLocationQuery(tx *gorm.DB, location string, hasCoord bool, lat, lon float64, radiusMeters float64, isStart bool) *gorm.DB {
	if hasCoord {
		if isStart {
			return tx.Where(
				"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography, ?)",
				lon, lat, radiusMeters,
			)
		} else {
			return tx.Where(
				"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography, ?)",
				lon, lat, radiusMeters,
			)
		}
	}

	if location == "" {
		return tx
	}

	normalizedLocation := strings.TrimSpace(strings.ToLower(location))
	locationColumn := "start_location"
	if !isStart {
		locationColumn = "end_location"
	}

	log.Printf("Text search on %s for: %s", locationColumn, normalizedLocation)

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
	log.Printf("Location WHERE clause: (%s)", whereClause)

	return tx.Where(fmt.Sprintf("(%s)", whereClause), values...)
}

func searchWithAdaptiveRadius(tx *gorm.DB, startLat, startLon, endLat, endLon float64, hasStartCoord, hasEndCoord bool) (*gorm.DB, float64) {
	if !hasStartCoord && !hasEndCoord {
		return tx, 0
	}

	radiusOptions := []float64{5000, 10000, 20000, 50000} // 5km, 10km, 20km, 50km

	for _, radius := range radiusOptions {
		var count int64
		testTx := database.Database.Db.Model(&models.Ride{}).
			Where("booked_seats < total_seats AND start_time > NOW()")

		if hasStartCoord {
			testTx = testTx.Where(
				"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography, ?)",
				startLon, startLat, radius,
			)
		}
		if hasEndCoord {
			testTx = testTx.Where(
				"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography, ?)",
				endLon, endLat, radius,
			)
		}

		testTx.Count(&count)
		if count >= 5 { // Found enough results
			finalTx := tx
			if hasStartCoord {
				finalTx = finalTx.Where(
					"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography, ?)",
					startLon, startLat, radius,
				)
			}
			if hasEndCoord {
				finalTx = finalTx.Where(
					"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography, ?)",
					endLon, endLat, radius,
				)
			}
			return finalTx, radius / 1000 // Return radius in km
		}
	}

	// Fallback to largest radius
	finalTx := tx
	if hasStartCoord {
		finalTx = finalTx.Where(
			"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography, ?)",
			startLon, startLat, 50000,
		)
	}
	if hasEndCoord {
		finalTx = finalTx.Where(
			"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography, ?)",
			endLon, endLat, 50000,
		)
	}
	return finalTx, 50.0
}

func calculateRelevanceScore(ride models.Ride, params SearchParams, startDist, endDist *float64) float64 {
	score := 100.0

	// Distance penalties (closer = better)
	if startDist != nil {
		score -= (*startDist) * 2 // -2 points per km from start
	}
	if endDist != nil {
		score -= (*endDist) * 2 // -2 points per km from end
	}

	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	rideTime := ride.StartTime.In(loc)

	nowDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	rideDateStart := time.Date(rideTime.Year(), rideTime.Month(), rideTime.Day(), 0, 0, 0, 0, loc)

	timeDiff := ride.StartTime.Sub(now)
	daysDiff := rideDateStart.Sub(nowDate).Hours() / 24

	if timeDiff.Hours() < 0.5 {
		score -= 30 // Too soon (less than 30 minutes)
	} else if timeDiff.Hours() < 1 {
		score -= 10 // A bit soon
	} else if daysDiff == 0 && timeDiff.Hours() <= 6 {
		score += 15 // Good timing (same day, 1-6 hours)
	} else if daysDiff == 0 {
		score += 10 // Same day but later
	} else if daysDiff == 1 {
		score += 5 // Tomorrow
	} else if daysDiff <= 7 {
		score += 2 // This week
	} else if daysDiff > 7 {
		score -= (daysDiff - 7) * 0.5 // Penalty for far future rides
	}

	// Preferred time matching
	if params.PreferredTime != "" {
		hour := rideTime.Hour()
		switch params.PreferredTime {
		case "morning":
			if hour >= 6 && hour < 12 {
				score += 10
			}
		case "afternoon":
			if hour >= 12 && hour < 17 {
				score += 10
			}
		case "evening":
			if hour >= 17 && hour < 21 {
				score += 10
			}
		case "night":
			if hour >= 21 || hour < 6 {
				score += 10
			}
		}
	}

	// Availability bonus
	availableSeats := float64(ride.TotalSeats - ride.BookedSeats)
	totalSeats := float64(ride.TotalSeats)
	availabilityRatio := availableSeats / totalSeats
	score += availabilityRatio * 15 // Up to 15 bonus points for full availability

	// Multiple seats bonus
	if availableSeats > 1 {
		score += 5
	}

	// Price attractiveness (assuming lower prices are better)
	if params.MaxPrice != nil && ride.TotalPrice <= *params.MaxPrice {
		score += 10 // Bonus for being within budget
		if ride.TotalPrice <= *params.MaxPrice/2 {
			score += 5 // Extra bonus for being very affordable
		}
	}

	// Capacity bonus
	if params.MinSeats != nil && availableSeats >= float64(*params.MinSeats) {
		score += 8
	}

	return score
}

// addMatchContext kept for backwards-compatibility with callers that don't
// have host frequency. New caller path uses addMatchContextWithHost.
func addMatchContext(card *RideCard, params SearchParams) {
	addMatchContextWithHost(card, params, 0, false)
}

func addMatchContextWithHost(card *RideCard, params SearchParams, hostPastRides int64, repeatRoute bool) {
	reasons := []string{}

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

	availableSeats := card.TotalSeats - card.BookedSeats
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

	limitStr := c.Query("limit", "20")
	offsetStr := c.Query("offset", "0")

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 1 {
		params.Limit = 20
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
		if v, err := strconv.ParseFloat(latStr, 64); err == nil {
			params.StartLat = v
			if v2, err := strconv.ParseFloat(lonStr, 64); err == nil {
				params.StartLon = v2
				params.HasStartCoord = true
			}
		}
	}

	if latStr, lonStr := c.Query("end_lat"), c.Query("end_lon"); latStr != "" && lonStr != "" {
		if v, err := strconv.ParseFloat(latStr, 64); err == nil {
			params.EndLat = v
			if v2, err := strconv.ParseFloat(lonStr, 64); err == nil {
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

	log.Printf("Search params: StartLocation=%s, EndLocation=%s, StartCoord=(%f,%f), EndCoord=(%f,%f), HasStartCoord=%v, HasEndCoord=%v",
		params.StartLocation, params.EndLocation, params.StartLat, params.StartLon, params.EndLat, params.EndLon, params.HasStartCoord, params.HasEndCoord)

	if params.HasStartCoord || params.HasEndCoord {
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

		query := `
			SELECT 
				start_location, 
				end_location, 
				start_latitude, 
				start_longitude, 
				end_latitude, 
				end_longitude`

		if params.HasStartCoord {
			query += `,
				ST_Distance(
					ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography,
					ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography
				) / 1000.0 as start_distance`
		} else {
			query += `, NULL as start_distance`
		}

		if params.HasEndCoord {
			query += `,
				ST_Distance(
					ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography,
					ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography
				) / 1000.0 as end_distance`
		} else {
			query += `, NULL as end_distance`
		}

		query += `
			FROM rides 
			WHERE booked_seats < total_seats 
				AND start_time > NOW() 
				AND (start_latitude IS NOT NULL OR end_latitude IS NOT NULL)
			ORDER BY `

		if params.HasStartCoord && params.HasEndCoord {
			query += `LEAST(
				COALESCE(ST_Distance(
					ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography,
					ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography
				), 999999999),
				COALESCE(ST_Distance(
					ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography,
					ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography
				), 999999999)
			) ASC`
		} else if params.HasStartCoord {
			query += `ST_Distance(
				ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography,
				ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography
			) ASC`
		} else if params.HasEndCoord {
			query += `ST_Distance(
				ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography,
				ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography
			) ASC`
		}

		query += ` LIMIT 5`

		var queryArgs []interface{}
		if params.HasStartCoord {
			queryArgs = append(queryArgs, params.StartLon, params.StartLat)
		}
		if params.HasEndCoord {
			queryArgs = append(queryArgs, params.EndLon, params.EndLat)
		}
		if params.HasStartCoord && params.HasEndCoord {
			queryArgs = append(queryArgs, params.StartLon, params.StartLat, params.EndLon, params.EndLat)
		} else if params.HasStartCoord {
			queryArgs = append(queryArgs, params.StartLon, params.StartLat)
		} else if params.HasEndCoord {
			queryArgs = append(queryArgs, params.EndLon, params.EndLat)
		}

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

	tx := database.Database.Db.
		Model(&models.Ride{}).
		Where("booked_seats < total_seats AND start_time > NOW()").
		Where("host_user_id != ?", params.User.ID)

	if params.Date != "" {
		startTime, endTime, err := parseTimeWindow(params.Date, params.PreferredTime)
		if err != nil {
			log.Printf("Error parsing date %q: %v\n", params.Date, err)
			return c.Status(400).JSON(fiber.Map{"error": "Invalid date format; expected YYYY-MM-DD"})
		}
		tx = tx.Where("start_time BETWEEN ? AND ?", startTime, endTime)
	}

	if params.MaxPrice != nil {
		tx = tx.Where("total_price <= ?", *params.MaxPrice)
	}

	if params.MinSeats != nil {
		tx = tx.Where("(total_seats - booked_seats) >= ?", *params.MinSeats)
	}

	usedRadius := params.RadiusKm
	var useCoordinateSearch bool

	if params.HasStartCoord || params.HasEndCoord {
		coordinateTx := tx

		if params.HasStartCoord {
			coordinateTx = coordinateTx.Where(
				"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(start_longitude, start_latitude), 4326)::geography, ?) OR start_longitude IS NULL OR start_latitude IS NULL",
				params.StartLon, params.StartLat, usedRadius*1000,
			)
		}
		if params.HasEndCoord {
			coordinateTx = coordinateTx.Where(
				"ST_DWithin(ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, ST_SetSRID(ST_MakePoint(end_longitude, end_latitude), 4326)::geography, ?) OR end_longitude IS NULL OR end_latitude IS NULL",
				params.EndLon, params.EndLat, usedRadius*1000,
			)
		}

		var testCount int64
		coordinateTx.Count(&testCount)
		log.Printf("Coordinate-based search found %d results with radius %.1fkm", testCount, usedRadius)

		if testCount > 0 {
			tx = coordinateTx
			useCoordinateSearch = true
		}
	}

	if (!useCoordinateSearch || true) && (params.StartLocation != "" || params.EndLocation != "") {
		textTx := database.Database.Db.
			Model(&models.Ride{}).
			Where("booked_seats < total_seats AND start_time > NOW()").
			Where("host_user_id != ?", params.User.ID)

		if params.Date != "" {
			startTime, endTime, _ := parseTimeWindow(params.Date, params.PreferredTime)
			textTx = textTx.Where("start_time BETWEEN ? AND ?", startTime, endTime)
		}
		if params.MaxPrice != nil {
			textTx = textTx.Where("total_price <= ?", *params.MaxPrice)
		}
		if params.MinSeats != nil {
			textTx = textTx.Where("(total_seats - booked_seats) >= ?", *params.MinSeats)
		}

		if params.StartLocation != "" {
			textTx = buildLocationQuery(textTx, params.StartLocation, false, 0, 0, 0, true)
		}
		if params.EndLocation != "" {
			textTx = buildLocationQuery(textTx, params.EndLocation, false, 0, 0, 0, false)
		}

		var textCount int64
		textTx.Count(&textCount)
		log.Printf("Text-based search found %d results", textCount)

		// Use text search if coordinate search failed or supplement it
		if !useCoordinateSearch {
			tx = textTx
			log.Printf("Using text-based search")
		}
	}

	var rides []models.Ride
	if err := tx.
		Preload("HostUser").
		Order("start_time ASC").
		Limit(params.Limit * 2).
		Offset(params.Offset).
		Find(&rides).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("Database error: %v", err)
		return c.Status(500).JSON(fiber.Map{"error": "Error fetching rides"})
	}

	log.Printf("Query returned %d rides", len(rides))

	if len(rides) == 0 && (params.StartLocation != "" || params.EndLocation != "") {
		log.Printf("No results found, trying fallback search...")

		fallbackTx := database.Database.Db.
			Model(&models.Ride{}).
			Where("booked_seats < total_seats AND start_time > NOW()").
			Where("host_user_id != ?", params.User.ID)

		if params.Date != "" {
			startTime, endTime, _ := parseTimeWindow(params.Date, params.PreferredTime)
			fallbackTx = fallbackTx.Where("start_time BETWEEN ? AND ?", startTime, endTime)
		}

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

		if err := fallbackTx.
			Preload("HostUser").
			Order("start_time ASC").
			Limit(params.Limit * 2).
			Offset(params.Offset).
			Find(&rides).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("Fallback search error: %v", err)
		} else {
			log.Printf("Fallback search returned %d rides", len(rides))
		}
	}

	// Build a "routes the user has travelled" set so we can boost rides
	// that match a route they've taken before. Pulls (start_location,
	// end_location) tuples from both their past hosted rides and past
	// bookings, then normalises them for case-insensitive comparison.
	pastRoutes := make(map[string]bool)
	if params.User.ID != uuid.Nil {
		type routePair struct {
			StartLocation string `json:"start_location"`
			EndLocation   string `json:"end_location"`
		}
		var pairs []routePair
		// Past hosted rides
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
		}
		for _, p := range pairs {
			key := strings.ToLower(strings.TrimSpace(p.StartLocation)) + "|" +
				strings.ToLower(strings.TrimSpace(p.EndLocation))
			pastRoutes[key] = true
		}
	}

	// Build a "completed rides per host" map in a single batch query so
	// `addMatchContext` can surface a "Trusted host" reason for prolific
	// hosts without doing one query per ride.
	hostFreq := make(map[uuid.UUID]int64)
	if len(rides) > 0 {
		hostIDs := make([]uuid.UUID, 0, len(rides))
		seenHost := make(map[uuid.UUID]bool, len(rides))
		for _, r := range rides {
			if !seenHost[r.HostUserID] {
				hostIDs = append(hostIDs, r.HostUserID)
				seenHost[r.HostUserID] = true
			}
		}
		type hostCount struct {
			HostUserID uuid.UUID `json:"host_user_id"`
			RideCount  int64     `json:"ride_count"`
		}
		var counts []hostCount
		if err := database.Database.Db.Model(&models.Ride{}).
			Select("host_user_id, COUNT(*) as ride_count").
			Where("host_user_id IN ? AND start_time < NOW()", hostIDs).
			Group("host_user_id").
			Find(&counts).Error; err != nil {
			log.Printf("host frequency lookup failed (continuing without): %v", err)
		}
		for _, c := range counts {
			hostFreq[c.HostUserID] = c.RideCount
		}
	}

	// Batch-load the caller's bookings for *every* ride in this
	// result set so the viewer-state resolver below doesn't trigger
	// a per-ride query. Single round-trip even for 50 results.
	rideIDs := make([]uuid.UUID, 0, len(rides))
	for _, r := range rides {
		rideIDs = append(rideIDs, r.ID)
	}
	viewerBookings := LoadViewerBookings(params.User.ID, rideIDs)

	response := make([]RideCard, 0, len(rides))
	for _, ride := range rides {
		card := RideCard{
			RideID:                    ride.ID,
			HostUserID:                ride.HostUserID,
			HostUserName:              ride.HostUser.Name,
			HostUserProfilePictureURL: ride.HostUser.ProfilePictureURL,
			HostUserYOB:               ride.HostUser.YOB,
			HostUserGender:            ride.HostUser.Gender,
			SameGenderFemale:          normalizeGender(params.User.Gender) == "female" && normalizeGender(ride.HostUser.Gender) == "female",
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

		card.RelevanceScore = calculateRelevanceScore(ride, params, startDist, endDist)

		// Mild relevance boost for proven hosts so users see them first
		// when sorting by relevance. Capped so it never dominates
		// distance/time factors.
		if past, ok := hostFreq[ride.HostUserID]; ok {
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

		addMatchContextWithHost(&card, params, hostFreq[ride.HostUserID], repeatRoute)

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

	return c.Status(200).JSON(fiber.Map{
		"rides":          response,
		"total_found":    len(response),
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
		},
	})
}
