package locations

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

type LocationResult struct {
	DisplayName string  `json:"display_name"`
	Lat         string  `json:"lat"`
	Lon         string  `json:"lon"`
	PlaceID     string  `json:"place_id"`
	Name        string  `json:"name,omitempty"`
	Source      string  `json:"source,omitempty"`
	DistanceKm  float64 `json:"distance_km,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

type locationCandidate struct {
	Name     string
	City     string
	Country  string
	Lat      float64
	Lon      float64
	Category string
	Priority float64
}

type cacheEntry struct {
	Results []LocationResult
	Expires time.Time
}

var (
	searchCacheMu sync.RWMutex
	searchCache   = map[string]cacheEntry{}

	httpClient = &http.Client{Timeout: 650 * time.Millisecond}
)

// curatedLocations holds the seed list of places that show up in
// empty-query "popular" suggestions and outrank free-text matches via
// the priority bonus. India-only by design: this is an Indian student
// carpool product, and surfacing San Francisco / Berkeley / Stanford
// to a user in Vellore (which is what the previous US seed entries
// did before they were resolved against a real GPS fix) makes the
// selector look broken.
var curatedLocations = []locationCandidate{
	{Name: "VIT Vellore", City: "Vellore", Country: "India", Lat: 12.9692, Lon: 79.1559, Category: "university", Priority: 120},
	{Name: "Katpadi Junction", City: "Vellore", Country: "India", Lat: 12.9726, Lon: 79.1372, Category: "station", Priority: 115},
	{Name: "Christian Medical College", City: "Vellore", Country: "India", Lat: 12.9249, Lon: 79.1353, Category: "hospital", Priority: 105},
	{Name: "Vellore Bus Stand", City: "Vellore", Country: "India", Lat: 12.9344, Lon: 79.1466, Category: "bus_station", Priority: 100},
	{Name: "Chennai International Airport", City: "Chennai", Country: "India", Lat: 12.9934, Lon: 80.1726, Category: "airport", Priority: 95},
	{Name: "Chennai Central Railway Station", City: "Chennai", Country: "India", Lat: 13.0826, Lon: 80.2763, Category: "station", Priority: 95},
	{Name: "Bangalore Kempegowda International Airport", City: "Bangalore", Country: "India", Lat: 13.1976, Lon: 77.7075, Category: "airport", Priority: 95},
	{Name: "Majestic Bus Stand", City: "Bangalore", Country: "India", Lat: 12.9782, Lon: 77.5722, Category: "bus_station", Priority: 90},
	{Name: "New Delhi Railway Station", City: "Delhi", Country: "India", Lat: 28.6403, Lon: 77.2204, Category: "station", Priority: 90},
	{Name: "Indira Gandhi International Airport", City: "Delhi", Country: "India", Lat: 28.5550, Lon: 77.0847, Category: "airport", Priority: 90},
	{Name: "Mumbai CSMT", City: "Mumbai", Country: "India", Lat: 18.9402, Lon: 72.8356, Category: "station", Priority: 90},
	{Name: "Mumbai Airport", City: "Mumbai", Country: "India", Lat: 19.0896, Lon: 72.8656, Category: "airport", Priority: 90},
	{Name: "Hyderabad HITEC City", City: "Hyderabad", Country: "India", Lat: 17.4490, Lon: 78.3831, Category: "business", Priority: 85},
	{Name: "Secunderabad Junction", City: "Hyderabad", Country: "India", Lat: 17.4338, Lon: 78.5020, Category: "station", Priority: 85},
	{Name: "Pune Railway Station", City: "Pune", Country: "India", Lat: 18.5289, Lon: 73.8744, Category: "station", Priority: 85},
	{Name: "Ahmedabad Junction", City: "Ahmedabad", Country: "India", Lat: 23.0263, Lon: 72.6010, Category: "station", Priority: 85},
}

type rideLocationRow struct {
	Name string
	Lat  *float64
	Lon  *float64
}

func SearchLocations(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))
	includeCurrent := strings.ToLower(strings.TrimSpace(c.Query("include_current"))) != "false"
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	var userLat, userLon float64
	hasUserLocation := false
	if lat, err := strconv.ParseFloat(c.Query("lat"), 64); err == nil {
		if lon, err := strconv.ParseFloat(c.Query("lng"), 64); err == nil {
			userLat, userLon = lat, lon
			hasUserLocation = true
		}
	}

	cacheKey := fmt.Sprintf("%s|%d|%.3f|%.3f|%t", strings.ToLower(query), limit, userLat, userLon, includeCurrent)
	if cached, ok := readSearchCache(cacheKey); ok {
		c.Set("Cache-Control", "public, max-age=60")
		return c.JSON(fiber.Map{"locations": cached, "cached": true})
	}

	results := mergeAndRank(query, limit, hasUserLocation, userLat, userLon)
	if query == "" && hasUserLocation && includeCurrent {
		results = append([]LocationResult{currentLocationResult(c.UserContext(), userLat, userLon)}, results...)
		results = dedupeAndSort(results, query, limit, hasUserLocation, userLat, userLon)
	}

	if len(results) < limit && len(query) >= 3 {
		remote := searchNominatim(c.UserContext(), query, limit-len(results), hasUserLocation, userLat, userLon)
		results = append(results, remote...)
		results = dedupeAndSort(results, query, limit, hasUserLocation, userLat, userLon)
	}

	writeSearchCache(cacheKey, results, 10*time.Minute)
	c.Set("Cache-Control", "public, max-age=60")
	return c.JSON(fiber.Map{"locations": results, "cached": false})
}

func mergeAndRank(query string, limit int, hasUserLocation bool, userLat, userLon float64) []LocationResult {
	results := make([]LocationResult, 0, limit*2)
	for _, candidate := range curatedLocations {
		if score := scoreCandidate(candidate.Name, candidate.City, candidate.Priority, query, hasUserLocation, userLat, userLon, candidate.Lat, candidate.Lon); score > 0 {
			results = append(results, locationCandidateToResult(candidate, score, hasUserLocation, userLat, userLon))
		}
	}

	for _, candidate := range rideLocationCandidates(query, limit*2) {
		priority := 80.0
		if candidate.Lat == nil || candidate.Lon == nil {
			priority = 45
		}
		lat, lon := 0.0, 0.0
		if candidate.Lat != nil && candidate.Lon != nil {
			lat, lon = *candidate.Lat, *candidate.Lon
		}
		if score := scoreCandidate(candidate.Name, "", priority, query, hasUserLocation, userLat, userLon, lat, lon); score > 0 {
			result := LocationResult{
				DisplayName: candidate.Name,
				Name:        candidate.Name,
				PlaceID:     "ride_" + slug(candidate.Name),
				Source:      "ride_history",
				Score:       score,
			}
			if candidate.Lat != nil && candidate.Lon != nil {
				result.Lat = strconv.FormatFloat(*candidate.Lat, 'f', -1, 64)
				result.Lon = strconv.FormatFloat(*candidate.Lon, 'f', -1, 64)
				if hasUserLocation {
					result.DistanceKm = haversineKm(userLat, userLon, *candidate.Lat, *candidate.Lon)
				}
			}
			results = append(results, result)
		}
	}

	return dedupeAndSort(results, query, limit, hasUserLocation, userLat, userLon)
}

func rideLocationCandidates(query string, limit int) []rideLocationRow {
	q := strings.TrimSpace(query)
	if q == "" {
		q = "%"
	} else {
		q = "%" + q + "%"
	}

	var starts []rideLocationRow
	_ = database.Database.Db.Model(&models.Ride{}).
		Select("start_location AS name, start_latitude AS lat, start_longitude AS lon").
		Where("start_location ILIKE ?", q).
		Where("start_location <> ''").
		Group("start_location, start_latitude, start_longitude").
		Order("max(created_at) DESC").
		Limit(limit).
		Scan(&starts).Error

	var ends []rideLocationRow
	_ = database.Database.Db.Model(&models.Ride{}).
		Select("end_location AS name, end_latitude AS lat, end_longitude AS lon").
		Where("end_location ILIKE ?", q).
		Where("end_location <> ''").
		Group("end_location, end_latitude, end_longitude").
		Order("max(created_at) DESC").
		Limit(limit).
		Scan(&ends).Error

	return append(starts, ends...)
}

func searchNominatim(parent context.Context, query string, limit int, hasUserLocation bool, userLat, userLon float64) []LocationResult {
	ctx, cancel := context.WithTimeout(parent, 650*time.Millisecond)
	defer cancel()

	values := url.Values{}
	values.Set("format", "json")
	values.Set("q", query)
	values.Set("limit", strconv.Itoa(limit))
	values.Set("addressdetails", "0")
	values.Set("extratags", "0")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://nominatim.openstreetmap.org/search?"+values.Encode(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "UniPool-Backend/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var payload []struct {
		DisplayName string `json:"display_name"`
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
		PlaceID     int64  `json:"place_id"`
		Name        string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}

	results := make([]LocationResult, 0, len(payload))
	for _, item := range payload {
		lat, latErr := strconv.ParseFloat(item.Lat, 64)
		lon, lonErr := strconv.ParseFloat(item.Lon, 64)
		if latErr != nil || lonErr != nil {
			continue
		}
		name := item.Name
		if name == "" {
			name = firstPart(item.DisplayName)
		}
		result := LocationResult{
			DisplayName: item.DisplayName,
			Lat:         item.Lat,
			Lon:         item.Lon,
			PlaceID:     fmt.Sprintf("osm_%d", item.PlaceID),
			Name:        name,
			Source:      "osm",
			Score:       scoreName(name, item.DisplayName, query) + 20,
		}
		if hasUserLocation {
			result.DistanceKm = haversineKm(userLat, userLon, lat, lon)
			result.Score += nearbyBoost(result.DistanceKm)
		}
		results = append(results, result)
	}
	return results
}

func dedupeAndSort(results []LocationResult, query string, limit int, hasUserLocation bool, userLat, userLon float64) []LocationResult {
	seen := map[string]LocationResult{}
	for _, result := range results {
		key := strings.ToLower(strings.TrimSpace(result.Name))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(firstPart(result.DisplayName)))
		}
		if result.Lat != "" && result.Lon != "" {
			key += "|" + result.Lat + "|" + result.Lon
		}
		if existing, ok := seen[key]; ok && existing.Score >= result.Score {
			continue
		}
		seen[key] = result
	}

	out := make([]LocationResult, 0, len(seen))
	for _, result := range seen {
		out = append(out, result)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if math.Abs(out[i].Score-out[j].Score) > 0.001 {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func scoreCandidate(name, city string, priority float64, query string, hasUserLocation bool, userLat, userLon, lat, lon float64) float64 {
	if strings.TrimSpace(query) == "" {
		score := priority
		if hasUserLocation {
			if lat == 0 || lon == 0 {
				return 0
			}
			distance := haversineKm(userLat, userLon, lat, lon)
			if distance > 150 {
				return 0
			}
			score += nearbyBoost(distance)
		}
		return score
	}

	textScore := scoreName(name, city, query)
	if textScore <= 0 {
		return 0
	}
	score := priority + textScore
	if hasUserLocation && lat != 0 && lon != 0 {
		score += nearbyBoost(haversineKm(userLat, userLon, lat, lon))
	}
	return score
}

func scoreName(name, secondary, query string) float64 {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 1
	}
	haystack := strings.ToLower(strings.TrimSpace(name + " " + secondary))
	nameOnly := strings.ToLower(strings.TrimSpace(name))
	switch {
	case nameOnly == q:
		return 130
	case strings.HasPrefix(nameOnly, q):
		return 105
	case strings.Contains(nameOnly, q):
		return 75
	case strings.Contains(haystack, q):
		return 45
	default:
		words := strings.Fields(q)
		if len(words) == 0 {
			return 0
		}
		matched := 0
		for _, word := range words {
			if len(word) >= 2 && strings.Contains(haystack, word) {
				matched++
			}
		}
		if matched == len(words) {
			return 35
		}
	}
	return 0
}

func locationCandidateToResult(candidate locationCandidate, score float64, hasUserLocation bool, userLat, userLon float64) LocationResult {
	parts := []string{candidate.Name}
	if candidate.City != "" && !strings.Contains(strings.ToLower(candidate.Name), strings.ToLower(candidate.City)) {
		parts = append(parts, candidate.City)
	}
	if candidate.Country != "" {
		parts = append(parts, candidate.Country)
	}
	displayName := strings.Join(parts, ", ")
	result := LocationResult{
		DisplayName: displayName,
		Lat:         strconv.FormatFloat(candidate.Lat, 'f', -1, 64),
		Lon:         strconv.FormatFloat(candidate.Lon, 'f', -1, 64),
		PlaceID:     "curated_" + slug(candidate.City+"_"+candidate.Name),
		Name:        candidate.Name,
		Source:      "curated",
		Score:       score,
	}
	if hasUserLocation {
		result.DistanceKm = haversineKm(userLat, userLon, candidate.Lat, candidate.Lon)
	}
	return result
}

func currentLocationResult(parent context.Context, lat, lon float64) LocationResult {
	name := "Current location"
	displayName := name

	ctx, cancel := context.WithTimeout(parent, 450*time.Millisecond)
	defer cancel()

	values := url.Values{}
	values.Set("format", "json")
	values.Set("lat", strconv.FormatFloat(lat, 'f', -1, 64))
	values.Set("lon", strconv.FormatFloat(lon, 'f', -1, 64))
	values.Set("zoom", "16")
	values.Set("addressdetails", "0")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://nominatim.openstreetmap.org/reverse?"+values.Encode(), nil)
	if err == nil {
		req.Header.Set("User-Agent", "UniPool-Backend/1.0")
		if resp, err := httpClient.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var payload struct {
					DisplayName string `json:"display_name"`
					Name        string `json:"name"`
				}
				if json.NewDecoder(resp.Body).Decode(&payload) == nil {
					if strings.TrimSpace(payload.Name) != "" {
						name = strings.TrimSpace(payload.Name)
					}
					if strings.TrimSpace(payload.DisplayName) != "" {
						displayName = strings.TrimSpace(payload.DisplayName)
					}
				}
			}
		}
	}

	return LocationResult{
		DisplayName: displayName,
		Lat:         strconv.FormatFloat(lat, 'f', -1, 64),
		Lon:         strconv.FormatFloat(lon, 'f', -1, 64),
		PlaceID:     fmt.Sprintf("current_%.5f_%.5f", lat, lon),
		Name:        name,
		Source:      "current",
		DistanceKm:  0,
		Score:       1000,
	}
}

func nearbyBoost(distanceKm float64) float64 {
	switch {
	case distanceKm <= 2:
		return 45
	case distanceKm <= 10:
		return 35
	case distanceKm <= 50:
		return 20
	case distanceKm <= 150:
		return 10
	default:
		return 0
	}
}

func readSearchCache(key string) ([]LocationResult, bool) {
	searchCacheMu.RLock()
	entry, ok := searchCache[key]
	searchCacheMu.RUnlock()
	if !ok || time.Now().After(entry.Expires) {
		return nil, false
	}
	return entry.Results, true
}

func writeSearchCache(key string, results []LocationResult, ttl time.Duration) {
	searchCacheMu.Lock()
	searchCache[key] = cacheEntry{Results: results, Expires: time.Now().Add(ttl)}
	if len(searchCache) > 500 {
		now := time.Now()
		for k, v := range searchCache {
			if now.After(v.Expires) {
				delete(searchCache, k)
			}
		}
	}
	searchCacheMu.Unlock()
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	lat1Rad := lat1 * math.Pi / 180
	lat2Rad := lat2 * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func firstPart(s string) string {
	parts := strings.Split(s, ",")
	return strings.TrimSpace(parts[0])
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			last := b.String()[b.Len()-1]
			if last != '_' {
				b.WriteByte('_')
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
