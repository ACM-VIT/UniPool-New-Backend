package rides

import (
	"errors"
	"log"
	"regexp"
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
	StartLocation             string    `json:"start_location"`
	EndLocation               string    `json:"end_location"`
	StartTime                 time.Time `json:"start_time"`
	TotalSeats                uint      `json:"total_seats"`
	BookedSeats               uint      `json:"booked_seats"`
	TotalPrice                uint      `json:"total_price"`
	StartLatitude             *float64  `json:"start_latitude,omitempty"`
	StartLongitude            *float64  `json:"start_longitude,omitempty"`
	EndLatitude               *float64  `json:"end_latitude,omitempty"`
	EndLongitude              *float64  `json:"end_longitude,omitempty"`
	StartDistance             *float64  `json:"start_distance,omitempty"`
	EndDistance               *float64  `json:"end_distance,omitempty"`
	TotalDistance             *float64  `json:"total_distance,omitempty"`
	RelevanceScore            float64   `json:"relevance_score,omitempty"`
	MatchReason               string    `json:"match_reason,omitempty"`
}

type SearchParams struct {
	StartLocation    string
	EndLocation      string
	StartLat         float64
	StartLon         float64
	EndLat           float64
	EndLon           float64
	HasStartCoord    bool
	HasEndCoord      bool
	Date             string
	PreferredTime    string
	MaxPrice         *uint
	MinSeats         *uint
	SortBy           string
	RadiusKm         float64
	Limit            int
	Offset           int
	User             models.User
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
	
	return tx.Where(
		locationColumn+" ILIKE ? OR "+
		locationColumn+" ILIKE ? OR "+
		"similarity("+locationColumn+", ?) > ? OR "+
		locationColumn+" ~ ?",
		normalizedLocation,
		"%"+normalizedLocation+"%",
		location,
		0.3,
		`\y`+regexp.QuoteMeta(normalizedLocation)+`\y`,
	)
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

func addMatchContext(card *RideCard, params SearchParams) {
	reasons := []string{}
	
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
	
	userIntf := c.Locals("user")
	if userIntf == nil {
		return params, errors.New("user not authenticated")
	}
	user, ok := userIntf.(models.User)
	if !ok {
		return params, errors.New("invalid user data")
	}
	params.User = user
	
	return params, nil
}

func SearchRides(c *fiber.Ctx) error {
	params, err := parseSearchParams(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
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
	
	if params.HasStartCoord || params.HasEndCoord {
		tx, usedRadius = searchWithAdaptiveRadius(tx, params.StartLat, params.StartLon, 
			params.EndLat, params.EndLon, params.HasStartCoord, params.HasEndCoord)
	}
	
	if !params.HasStartCoord && params.StartLocation != "" {
		tx = buildLocationQuery(tx, params.StartLocation, false, 0, 0, 0, true)
	}
	if !params.HasEndCoord && params.EndLocation != "" {
		tx = buildLocationQuery(tx, params.EndLocation, false, 0, 0, 0, false)
	}
	
	var rides []models.Ride
	if err := tx.
		Preload("HostUser").
		Order("start_time ASC").
		Limit(params.Limit * 2).
		Offset(params.Offset).
		Find(&rides).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return c.Status(500).JSON(fiber.Map{"error": "Error fetching rides"})
	}
	
	response := make([]RideCard, 0, len(rides))
	for _, ride := range rides {
		card := RideCard{
			RideID:                    ride.ID,
			HostUserID:                ride.HostUserID,
			HostUserName:              ride.HostUser.Name,
			HostUserProfilePictureURL: ride.HostUser.ProfilePictureURL,
			HostUserYOB:               ride.HostUser.YOB,
			StartLocation:             ride.StartLocation,
			EndLocation:               ride.EndLocation,
			StartTime:                 ride.StartTime,
			TotalSeats:                ride.TotalSeats,
			BookedSeats:               ride.BookedSeats,
			TotalPrice:                ride.TotalPrice,
			StartLatitude:             ride.StartLatitude,
			StartLongitude:            ride.StartLongitude,
			EndLatitude:               ride.EndLatitude,
			EndLongitude:              ride.EndLongitude,
		}
		
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
		
		addMatchContext(&card, params)
		
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
		"rides": response,
		"meta": fiber.Map{
			"total_found": len(response),
			"used_radius_km": usedRadius,
			"sort_by": params.SortBy,
		},
	})
}