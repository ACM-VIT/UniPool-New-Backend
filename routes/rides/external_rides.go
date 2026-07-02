package rides

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unipool-backend/helpers"
)

const (
	vigoProject    = "vigo1-d2128"
	vigoFirestore  = "https://firestore.googleapis.com/v1/projects/" + vigoProject + "/databases/(default)/documents"
	vigoCacheTTL   = 30 * time.Minute
	vigoFetchLimit = 300
)

type ExternalRideCard struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	SourceLabel   string `json:"source_label"`
	PickupPoint   string `json:"pickup_point"`
	Destination   string `json:"destination"`
	DepartureTime string `json:"departure_time"`
	HostName      string `json:"host_name"`
	HostPhone     string `json:"host_phone"`
	// HostEmail is the address the external ride is linked by. It is kept
	// server-side only (json:"-") so it is never exposed to clients; the
	// invite endpoint resolves it by ride id to send the "wants to UniPool
	// with you" email.
	HostEmail string `json:"-"`
	// HasHostEmail tells clients whether an invite email can be sent for this
	// ride WITHOUT leaking the address itself. Off-platform sources (e.g. Vigo)
	// often don't expose an email, so the UI hides the invite CTA and leads
	// with WhatsApp/phone instead of a dead-end "no email on file".
	HasHostEmail   bool   `json:"has_host_email"`
	VehicleType    string `json:"vehicle_type"`
	TotalSeats     int    `json:"total_seats"`
	AvailableSeats int    `json:"available_seats"`
	TotalPrice     *uint  `json:"total_price,omitempty"`
	// Present for /rides/nearby responses, where the pickup-point distance has
	// already been computed to filter and sort the external feed.
	PickupDistanceKm *float64 `json:"pickup_distance_km,omitempty"`
	JourneyNotes     string   `json:"journey_notes,omitempty"`
}

type firestoreField struct {
	StringValue    *string              `json:"stringValue,omitempty"`
	IntegerValue   *string              `json:"integerValue,omitempty"`
	DoubleValue    *float64             `json:"doubleValue,omitempty"`
	TimestampValue *string              `json:"timestampValue,omitempty"`
	ArrayValue     *firestoreArrayValue `json:"arrayValue,omitempty"`
}

type firestoreArrayValue struct {
	Values []json.RawMessage `json:"values,omitempty"`
}

type firestoreDoc struct {
	Name   string                    `json:"name"`
	Fields map[string]firestoreField `json:"fields"`
}

type firestorePage struct {
	Documents     []firestoreDoc `json:"documents"`
	NextPageToken string         `json:"nextPageToken"`
}

func fieldStr(fields map[string]firestoreField, key string) string {
	if f, ok := fields[key]; ok && f.StringValue != nil {
		return *f.StringValue
	}
	return ""
}

func fieldInt(fields map[string]firestoreField, key string) (int, bool) {
	if f, ok := fields[key]; ok && f.IntegerValue != nil {
		v := 0
		if _, err := fmt.Sscanf(*f.IntegerValue, "%d", &v); err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

func fieldTimestamp(fields map[string]firestoreField, key string) string {
	if f, ok := fields[key]; ok && f.TimestampValue != nil {
		return *f.TimestampValue
	}
	return ""
}

func fieldArrayLen(fields map[string]firestoreField, key string) int {
	if f, ok := fields[key]; ok && f.ArrayValue != nil {
		return len(f.ArrayValue.Values)
	}
	return 0
}

func externalRideAvailableSeats(fields map[string]firestoreField) int {
	if availableSeats, ok := fieldInt(fields, "availableSeats"); ok {
		return availableSeats
	}

	totalSeats, _ := fieldInt(fields, "totalSeats")
	passengerCount := fieldArrayLen(fields, "passengerIds")
	if totalSeats <= 1 {
		return 0
	}
	availableSeats := totalSeats - 1 - passengerCount
	if availableSeats < 0 {
		return 0
	}
	return availableSeats
}

func externalRideTotalPrice(fields map[string]firestoreField) *uint {
	for _, key := range []string{"totalPrice", "totalFare", "total_price", "total_fare", "price", "fare"} {
		if price, ok := fieldInt(fields, key); ok && price >= 0 {
			value := uint(price)
			return &value
		}
		if f, ok := fields[key]; ok && f.DoubleValue != nil && *f.DoubleValue >= 0 {
			value := uint(*f.DoubleValue)
			return &value
		}
	}
	return nil
}

// externalRideEmail pulls the host's email out of whichever field the source
// document uses. External sources are not perfectly consistent, so we try the
// common spellings and take the first that looks like an email.
func externalRideEmail(fields map[string]firestoreField) string {
	for _, key := range []string{
		"driverEmail", "email", "hostEmail", "userEmail",
		"createdByEmail", "createdBy", "driver_email",
	} {
		if v := fieldStr(fields, key); strings.Contains(v, "@") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// FindExternalRideByID returns the cached external ride with the given id.
// Used by the invite endpoint to resolve the host email server-side without
// ever handing it to the client.
func FindExternalRideByID(id string) (ExternalRideCard, bool) {
	if id == "" {
		return ExternalRideCard{}, false
	}
	cached, _ := getUsableVigoCache()
	for _, r := range cached {
		if r.ID == id {
			return r, true
		}
	}
	return ExternalRideCard{}, false
}

type externalLocationPoint struct {
	lat float64
	lng float64
}

type externalLocationAlias struct {
	label string
	point externalLocationPoint
}

var externalLocationAliasList = []externalLocationAlias{
	{label: "vit vellore main gate", point: externalLocationPoint{lat: 12.969193, lng: 79.155968}},
	{label: "vit main gate", point: externalLocationPoint{lat: 12.969193, lng: 79.155968}},
	{label: "vellore institute of technology", point: externalLocationPoint{lat: 12.969193, lng: 79.155968}},
	{label: "vit vellore", point: externalLocationPoint{lat: 12.969193, lng: 79.155968}},
	{label: "vit", point: externalLocationPoint{lat: 12.969193, lng: 79.155968}},
	{label: "katpadi railway station", point: externalLocationPoint{lat: 12.972153, lng: 79.137637}},
	{label: "katpadi junction", point: externalLocationPoint{lat: 12.972153, lng: 79.137637}},
	{label: "katpadi", point: externalLocationPoint{lat: 12.972153, lng: 79.137637}},
	{label: "chennai international airport", point: externalLocationPoint{lat: 12.993374, lng: 80.172587}},
	{label: "chennai airport", point: externalLocationPoint{lat: 12.993374, lng: 80.172587}},
	{label: "kempegowda international airport", point: externalLocationPoint{lat: 13.197605, lng: 77.707486}},
	{label: "bangalore kempegowda airport", point: externalLocationPoint{lat: 13.197605, lng: 77.707486}},
	{label: "bangalore airport", point: externalLocationPoint{lat: 13.197605, lng: 77.707486}},
	{label: "bengaluru airport", point: externalLocationPoint{lat: 13.197605, lng: 77.707486}},
	{label: "vellore new bus stand", point: externalLocationPoint{lat: 12.934693, lng: 79.136977}},
	{label: "vellore bus stand", point: externalLocationPoint{lat: 12.934693, lng: 79.136977}},
	{label: "new bus stand", point: externalLocationPoint{lat: 12.934693, lng: 79.136977}},
	{label: "vellore bypass near dtdc", point: externalLocationPoint{lat: 12.931215, lng: 79.134108}},
	{label: "vellore bypass", point: externalLocationPoint{lat: 12.931215, lng: 79.134108}},
	{label: "near dtdc", point: externalLocationPoint{lat: 12.931215, lng: 79.134108}},
}

var externalLocationAliases = func() map[string]externalLocationPoint {
	aliases := make(map[string]externalLocationPoint, len(externalLocationAliasList))
	for _, alias := range externalLocationAliasList {
		aliases[alias.label] = alias.point
	}
	return aliases
}()

func normalizeExternalLocation(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func externalLocationCoords(label string) (float64, float64, bool) {
	normalized := normalizeExternalLocation(label)
	if normalized == "" {
		return 0, 0, false
	}
	if point, ok := externalLocationAliases[normalized]; ok {
		return point.lat, point.lng, true
	}
	bestScore := -1
	bestIndex := -1
	queryIsSpecific := len(strings.Fields(normalized)) >= 2
	for i, alias := range externalLocationAliasList {
		score := -1
		if strings.Contains(normalized, alias.label) {
			score = 1000 + len(alias.label)
		} else if queryIsSpecific && strings.Contains(alias.label, normalized) {
			score = len(normalized)
		}
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	if bestIndex >= 0 {
		point := externalLocationAliasList[bestIndex].point
		return point.lat, point.lng, true
	}
	return 0, 0, false
}

var (
	vigoCache           []ExternalRideCard
	vigoCacheTime       time.Time
	vigoCacheMu         sync.RWMutex
	vigoRefreshMu       sync.Mutex
	vigoRefreshCond     = sync.NewCond(&vigoRefreshMu)
	vigoRefreshInFlight bool
	fetchVigoRides      = fetchVigoRidesFromFirestore
)

func getVigoCacheSnapshot() ([]ExternalRideCard, bool, bool) {
	vigoCacheMu.RLock()
	defer vigoCacheMu.RUnlock()

	if vigoCacheTime.IsZero() {
		return nil, false, false
	}

	cached := append([]ExternalRideCard(nil), vigoCache...)
	return cached, time.Since(vigoCacheTime) < vigoCacheTTL, true
}

func getFreshVigoCache() ([]ExternalRideCard, bool) {
	cached, fresh, ok := getVigoCacheSnapshot()
	if !ok || !fresh {
		return nil, false
	}
	return cached, true
}

func getUsableVigoCache() ([]ExternalRideCard, bool) {
	if os.Getenv("UNIPOOL_DISABLE_EXTERNAL_RIDES") == "1" {
		return []ExternalRideCard{}, true
	}

	cached, fresh, ok := getVigoCacheSnapshot()
	if !ok {
		return refreshVigoCacheSynchronously()
	}
	if !fresh {
		refreshVigoCacheInBackground()
	}
	return cached, true
}

func runVigoRefresh() {
	all, err := fetchVigoRides()
	if err != nil {
		log.Printf("external rides fetch failed: %v", err)
	} else {
		vigoCacheMu.Lock()
		vigoCache = all
		vigoCacheTime = time.Now()
		vigoCacheMu.Unlock()
	}

	vigoRefreshMu.Lock()
	vigoRefreshInFlight = false
	vigoRefreshCond.Broadcast()
	vigoRefreshMu.Unlock()
}

func refreshVigoCacheSynchronously() ([]ExternalRideCard, bool) {
	vigoRefreshMu.Lock()
	if vigoRefreshInFlight {
		for vigoRefreshInFlight {
			vigoRefreshCond.Wait()
		}
		vigoRefreshMu.Unlock()
		cached, _, ok := getVigoCacheSnapshot()
		if !ok {
			return []ExternalRideCard{}, false
		}
		return cached, true
	}
	vigoRefreshInFlight = true
	vigoRefreshMu.Unlock()

	runVigoRefresh()
	cached, _, ok := getVigoCacheSnapshot()
	if !ok {
		return []ExternalRideCard{}, false
	}
	return cached, true
}

func refreshVigoCacheInBackground() {
	vigoRefreshMu.Lock()
	if vigoRefreshInFlight {
		vigoRefreshMu.Unlock()
		return
	}
	vigoRefreshInFlight = true
	vigoRefreshMu.Unlock()

	go runVigoRefresh()
}

func fetchVigoRidesFromFirestore() ([]ExternalRideCard, error) {
	all := make([]ExternalRideCard, 0)
	pageToken := ""
	client := &http.Client{Timeout: 8 * time.Second}

	for {
		requestURL := fmt.Sprintf("%s/rides?pageSize=100", vigoFirestore)
		if pageToken != "" {
			requestURL += "&pageToken=" + neturl.QueryEscape(pageToken)
		}

		resp, err := client.Get(requestURL)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("firestore returned status %d", resp.StatusCode)
		}

		var page firestorePage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		now := time.Now()

		for _, doc := range page.Documents {
			depStr := fieldTimestamp(doc.Fields, "departureTime")
			if depStr == "" {
				continue
			}
			dep, err := time.Parse(time.RFC3339, depStr)
			if err != nil {
				dep, err = time.Parse(time.RFC3339Nano, depStr)
				if err != nil {
					continue
				}
			}
			if dep.Before(now) {
				continue
			}

			totalSeats, _ := fieldInt(doc.Fields, "totalSeats")
			availableSeats := externalRideAvailableSeats(doc.Fields)
			if availableSeats <= 0 {
				continue
			}

			parts := strings.Split(doc.Name, "/")
			id := parts[len(parts)-1]

			vehicleType := fieldStr(doc.Fields, "vehicleType")
			if vehicleType == "" {
				vehicleType = "Car"
			}

			email := externalRideEmail(doc.Fields)

			all = append(all, ExternalRideCard{
				ID:             id,
				Source:         "external",
				SourceLabel:    "External",
				PickupPoint:    fieldStr(doc.Fields, "pickupPoint"),
				Destination:    fieldStr(doc.Fields, "destination"),
				DepartureTime:  depStr,
				HostName:       fieldStr(doc.Fields, "driverName"),
				HostPhone:      fieldStr(doc.Fields, "driverPhone"),
				HostEmail:      email,
				HasHostEmail:   email != "",
				VehicleType:    vehicleType,
				TotalSeats:     totalSeats,
				AvailableSeats: availableSeats,
				TotalPrice:     externalRideTotalPrice(doc.Fields),
				JourneyNotes:   fieldStr(doc.Fields, "journeyNotes"),
			})

			if len(all) >= vigoFetchLimit {
				break
			}
		}

		pageToken = page.NextPageToken
		if pageToken == "" || len(all) >= vigoFetchLimit {
			break
		}
	}

	return all, nil
}

func filterExternalRides(rides []ExternalRideCard, startLocation, endLocation string) []ExternalRideCard {
	if rides == nil {
		rides = []ExternalRideCard{}
	}
	if startLocation == "" && endLocation == "" {
		return rides
	}

	normalize := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(s), ""))
	}

	startNorm := normalize(startLocation)
	endNorm := normalize(endLocation)
	matches := func(value, query string) bool {
		if value == "" || query == "" {
			return false
		}
		return strings.Contains(value, query) || strings.Contains(query, value)
	}

	filtered := make([]ExternalRideCard, 0)
	for _, r := range rides {
		pickup := normalize(r.PickupPoint)
		dest := normalize(r.Destination)

		startMatch := matches(pickup, startNorm)
		endMatch := matches(dest, endNorm)

		switch {
		case startNorm != "" && endNorm != "":
			if startMatch && endMatch {
				filtered = append(filtered, r)
			}
		case startNorm != "":
			if startMatch {
				filtered = append(filtered, r)
			}
		case endNorm != "":
			if endMatch {
				filtered = append(filtered, r)
			}
		default:
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func limitExternalRides(rides []ExternalRideCard, limit int) []ExternalRideCard {
	if rides == nil {
		return []ExternalRideCard{}
	}
	if limit < 0 {
		limit = 0
	}
	if len(rides) <= limit {
		return rides
	}
	return rides[:limit]
}

type ExternalRideSearchParams struct {
	StartLocation string
	EndLocation   string
	StartLat      float64
	StartLon      float64
	EndLat        float64
	EndLon        float64
	HasStartCoord bool
	HasEndCoord   bool
	RadiusKm      float64
	HasDateFilter bool
	DateStart     time.Time
	DateEnd       time.Time
	MaxPrice      *uint
	MinSeats      *uint
}

func parseExternalDepartureTime(raw string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func externalRideIsUpcoming(ride ExternalRideCard, now time.Time) bool {
	departure, ok := parseExternalDepartureTime(ride.DepartureTime)
	return ok && departure.After(now)
}

func externalSearchRadiusKm(params ExternalRideSearchParams) float64 {
	if params.RadiusKm <= 0 {
		return 10
	}
	return NormalizeNearbyRadius(params.RadiusKm*1000) / 1000
}

func externalLocationTextMatch(value, query string) bool {
	normalize := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(s), ""))
	}
	valueNorm := normalize(value)
	queryNorm := normalize(query)
	if valueNorm == "" || queryNorm == "" {
		return false
	}
	return strings.Contains(valueNorm, queryNorm) || strings.Contains(queryNorm, valueNorm)
}

func externalEndpointMatches(label, query string, hasCoord bool, lat, lng, radiusKm float64) bool {
	if hasCoord {
		pointLat, pointLng, ok := externalLocationCoords(label)
		if !ok {
			return false
		}
		return helpers.CalculateDistance(lat, lng, pointLat, pointLng) <= radiusKm
	}
	return externalLocationTextMatch(label, query)
}

func filterExternalRidesForSearch(rides []ExternalRideCard, params ExternalRideSearchParams) []ExternalRideCard {
	if rides == nil {
		rides = []ExternalRideCard{}
	}
	radiusKm := externalSearchRadiusKm(params)
	hasStartFilter := params.StartLocation != "" || params.HasStartCoord
	hasEndFilter := params.EndLocation != "" || params.HasEndCoord
	now := time.Now()
	filtered := make([]ExternalRideCard, 0, len(rides))

	for _, ride := range rides {
		if !externalRideIsUpcoming(ride, now) {
			continue
		}
		if hasStartFilter && !externalEndpointMatches(ride.PickupPoint, params.StartLocation, params.HasStartCoord, params.StartLat, params.StartLon, radiusKm) {
			continue
		}
		if hasEndFilter && !externalEndpointMatches(ride.Destination, params.EndLocation, params.HasEndCoord, params.EndLat, params.EndLon, radiusKm) {
			continue
		}
		if params.HasDateFilter {
			departure, ok := parseExternalDepartureTime(ride.DepartureTime)
			if !ok || departure.Before(params.DateStart) || departure.After(params.DateEnd) {
				continue
			}
		}
		if params.MinSeats != nil && ride.AvailableSeats < int(*params.MinSeats) {
			continue
		}
		if params.MaxPrice != nil {
			if ride.TotalPrice == nil || *ride.TotalPrice > *params.MaxPrice {
				continue
			}
		}
		filtered = append(filtered, ride)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		iTime, iOK := parseExternalDepartureTime(filtered[i].DepartureTime)
		jTime, jOK := parseExternalDepartureTime(filtered[j].DepartureTime)
		switch {
		case iOK && jOK && !iTime.Equal(jTime):
			return iTime.Before(jTime)
		case iOK != jOK:
			return iOK
		case filtered[i].PickupPoint != filtered[j].PickupPoint:
			return filtered[i].PickupPoint < filtered[j].PickupPoint
		case filtered[i].Destination != filtered[j].Destination:
			return filtered[i].Destination < filtered[j].Destination
		default:
			return filtered[i].ID < filtered[j].ID
		}
	})

	return filtered
}

func FetchAllExternalRides() []ExternalRideCard {
	all, ok := getUsableVigoCache()
	if !ok {
		return []ExternalRideCard{}
	}
	now := time.Now()
	filtered := make([]ExternalRideCard, 0, len(all))
	for _, ride := range all {
		if externalRideIsUpcoming(ride, now) {
			filtered = append(filtered, ride)
		}
	}
	return filtered
}

func FetchExternalRidesForNearby(lat, lng, radiusMeters float64) []ExternalRideCard {
	all, ok := getUsableVigoCache()
	if !ok {
		return []ExternalRideCard{}
	}

	radiusKm := NormalizeNearbyRadius(radiusMeters) / 1000
	type nearbyExternalRide struct {
		ride       ExternalRideCard
		distanceKm float64
	}
	matches := make([]nearbyExternalRide, 0)
	now := time.Now()
	for _, ride := range all {
		if !externalRideIsUpcoming(ride, now) {
			continue
		}
		pickupLat, pickupLng, ok := externalLocationCoords(ride.PickupPoint)
		if !ok {
			continue
		}
		distanceKm := helpers.CalculateDistance(lat, lng, pickupLat, pickupLng)
		if distanceKm <= radiusKm {
			matches = append(matches, nearbyExternalRide{ride: ride, distanceKm: distanceKm})
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].distanceKm != matches[j].distanceKm {
			return matches[i].distanceKm < matches[j].distanceKm
		}
		if matches[i].ride.DepartureTime != matches[j].ride.DepartureTime {
			return matches[i].ride.DepartureTime < matches[j].ride.DepartureTime
		}
		return matches[i].ride.ID < matches[j].ride.ID
	})

	out := make([]ExternalRideCard, 0, len(matches))
	for _, match := range matches {
		ride := match.ride
		distanceKm := match.distanceKm
		ride.PickupDistanceKm = &distanceKm
		out = append(out, ride)
	}
	return out
}

func FetchExternalRidesForSearch(params ExternalRideSearchParams) []ExternalRideCard {
	all, ok := getUsableVigoCache()
	if !ok {
		return []ExternalRideCard{}
	}
	return filterExternalRidesForSearch(all, params)
}
