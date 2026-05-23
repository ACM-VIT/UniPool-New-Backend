package rides

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unipool-backend/helpers"
	"unipool-backend/models"
)

// MatchSignal is one row in a card's MatchSignals slice. The client
// renders these as pill-shaped chips under the ride card. `Kind`
// names the signal so clients can map it to an icon and a tone;
// `Label` is the short headline copy (already localised to en-IN);
// `Detail` is an optional secondary string (a distance, a delta, a
// host count). `Tone` is a small enum the client can map to colour
// without hardcoding kind→colour everywhere.
//
// The server sorts signals by descending priority before emitting,
// so a client that wants to show the top 3 chips can just slice the
// first three without re-sorting. Priority itself is NOT serialised
// — it's an implementation detail that may change.
type MatchSignal struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
	Tone     string `json:"tone"`
	priority int
}

// Signal tones — kept in one place so the frontend's chip palette can
// mirror the same set without divergence. Tone is intentionally
// chunkier than colour: it describes the *role* of the signal so the
// design system can repaint without a server change.
const (
	SignalTonePrimary  = "primary"  // top-rank match (exact time, exact destination, repeat route)
	SignalToneRoute    = "route"    // contextual / route-shape signal (on the way)
	SignalToneSpatial  = "spatial"  // pickup / drop-off proximity
	SignalToneSocial   = "social"   // host reputation
	SignalToneFresh    = "fresh"    // newness / urgency
	SignalToneTime     = "time"     // day / time-of-day grouping
	SignalToneCapacity = "capacity" // seats availability
	SignalToneValue    = "value"    // price affordability
)

type rideSearchScore struct {
	Score             float64
	MatchReason       string
	MatchType         string
	RouteDeviationKm  *float64
	RouteProgress     *float64
	TargetTimeDeltaM  *float64
	TargetDateDeltaD  *int
	DestinationScore  float64
	RouteOverlapScore float64
}

type geoPoint struct {
	Lat float64
	Lon float64
}

func scoreRideForSearch(ride models.Ride, params SearchParams, startDist, endDist *float64) rideSearchScore {
	score := rideSearchScore{
		Score:     0,
		MatchType: "possible",
	}

	score.Score += scoreDateFit(ride.StartTime, params, &score)
	score.Score += scoreTimeFit(ride.StartTime, params, &score)
	score.Score += scorePickupFit(params, startDist)

	destinationScore, destinationReason, destinationType := scoreDestinationFit(ride, params, endDist)
	score.DestinationScore = destinationScore
	score.Score += destinationScore
	if destinationType != "" {
		score.MatchType = destinationType
		score.MatchReason = destinationReason
	}

	routeScore, routeReason, deviationKm, progress := scoreRouteOverlap(ride, params)
	score.RouteOverlapScore = routeScore
	score.Score += routeScore
	if routeScore > destinationScore*0.8 && routeReason != "" {
		score.MatchReason = routeReason
		score.MatchType = "route_overlap"
		score.RouteDeviationKm = &deviationKm
		score.RouteProgress = &progress
	}

	score.Score += scoreTextFit(params.StartLocation, ride.StartLocation, 28)
	score.Score += scoreTextFit(params.EndLocation, ride.EndLocation, 42)
	score.Score += scoreAvailabilityAndPrice(ride, params)
	score.Score += scorePreferredTimeBucket(ride.StartTime, params)

	if score.MatchReason == "" {
		score.MatchReason = fallbackMatchReason(startDist, endDist, params)
	}

	return score
}

func scoreDateFit(rideTime time.Time, params SearchParams, out *rideSearchScore) float64 {
	loc := searchLocation()
	rideDay := dayStart(rideTime.In(loc), loc)

	var targetDay time.Time
	hasTargetDay := false
	if params.HasTargetTime {
		targetDay = dayStart(params.TargetTime.In(loc), loc)
		hasTargetDay = true
	} else if params.Date != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", params.Date, loc); err == nil {
			targetDay = dayStart(parsed, loc)
			hasTargetDay = true
		}
	}

	if !hasTargetDay {
		nowDay := dayStart(time.Now().In(loc), loc)
		daysFromToday := int(math.Round(rideDay.Sub(nowDay).Hours() / 24))
		switch {
		case daysFromToday == 0:
			return 120
		case daysFromToday == 1:
			return 90
		case daysFromToday <= 7:
			return 55 - float64(daysFromToday)*4
		default:
			return -float64(daysFromToday-7) * 10
		}
	}

	days := int(math.Round(rideDay.Sub(targetDay).Hours() / 24))
	absDays := absInt(days)
	out.TargetDateDeltaD = &days
	if absDays == 0 {
		return 720
	}
	if absDays == 1 {
		return -380
	}
	return -720 - float64(absDays-1)*180
}

func scoreTimeFit(rideTime time.Time, params SearchParams, out *rideSearchScore) float64 {
	if !params.HasTargetTime {
		return 0
	}

	diffM := rideTime.Sub(params.TargetTime).Minutes()
	out.TargetTimeDeltaM = &diffM
	absM := math.Abs(diffM)

	// Close departures should dominate, but for student travel a ride
	// slightly before the requested time is usually safer than a late one.
	base := 290 * math.Exp(-absM/95)
	if diffM <= 0 && absM <= 90 {
		base += 34
	}
	if diffM > 0 {
		base -= math.Min(110, diffM*1.35)
	}
	if diffM < -180 {
		base -= (absM - 180) * 0.55
	}
	return base
}

func scorePickupFit(params SearchParams, startDist *float64) float64 {
	if !params.HasStartCoord {
		return 0
	}
	if startDist == nil {
		return -180
	}
	d := *startDist
	switch {
	case d <= 0.35:
		return 230
	case d <= 1:
		return 205
	case d <= 2:
		return 175
	case d <= 5:
		return 120
	case d <= 10:
		return 55
	case d <= 20:
		return -20
	default:
		return -140
	}
}

func scoreDestinationFit(ride models.Ride, params SearchParams, endDist *float64) (float64, string, string) {
	score := 0.0
	reason := ""
	matchType := ""

	if params.HasEndCoord {
		if endDist == nil {
			score -= 160
		} else {
			d := *endDist
			tripKm := requestedTripDistanceKm(params)
			relative := 1.0
			if tripKm > 1 {
				relative = d / tripKm
			}

			switch {
			case d <= 0.35:
				score += 430
				reason = "Exact destination"
				matchType = "exact_destination"
			case d <= 1:
				score += 380
				reason = "Very close destination"
				matchType = "near_destination"
			case d <= 2.5:
				score += 300
				reason = "Nearby destination"
				matchType = "near_destination"
			case d <= 6:
				score += 135
				reason = "Same area"
				matchType = "same_area"
			case d <= 15:
				score += 70
				reason = "Nearby area"
				matchType = "same_area"
			case relative <= 0.12:
				score += 90
				reason = "Close for this route"
				matchType = "same_region"
			case relative <= 0.2:
				score += 45
				reason = "Same broad destination"
				matchType = "same_region"
			default:
				score -= math.Min(190, d*4)
			}
		}
	}

	textScore := scoreTextFit(params.EndLocation, ride.EndLocation, 70)
	if textScore >= 55 && score < 220 {
		score += textScore
		if reason == "" {
			reason = "Destination name match"
			matchType = "text_destination"
		}
	}

	return score, reason, matchType
}

func scoreRouteOverlap(ride models.Ride, params SearchParams) (score float64, reason string, deviationKm float64, progress float64) {
	if !params.HasStartCoord || !params.HasEndCoord {
		return 0, "", 0, 0
	}
	if !helpers.AreCoordinatesValid(ride.StartLatitude, ride.StartLongitude) ||
		!helpers.AreCoordinatesValid(ride.EndLatitude, ride.EndLongitude) {
		return 0, "", 0, 0
	}

	rideStart := geoPoint{Lat: *ride.StartLatitude, Lon: *ride.StartLongitude}
	rideEnd := geoPoint{Lat: *ride.EndLatitude, Lon: *ride.EndLongitude}
	queryEnd := geoPoint{Lat: params.EndLat, Lon: params.EndLon}

	rideTripKm := helpers.CalculateDistance(rideStart.Lat, rideStart.Lon, rideEnd.Lat, rideEnd.Lon)
	if rideTripKm < 1 {
		return 0, "", 0, 0
	}

	deviationKm, progress = pointSegmentDistanceKm(queryEnd, rideStart, rideEnd)
	if progress < 0.12 || progress > 0.96 {
		return 0, "", deviationKm, progress
	}

	corridorKm := routeCorridorKm(rideTripKm, requestedTripDistanceKm(params))
	if deviationKm > corridorKm {
		return 0, "", deviationKm, progress
	}

	viaKm := helpers.CalculateDistance(rideStart.Lat, rideStart.Lon, queryEnd.Lat, queryEnd.Lon) +
		helpers.CalculateDistance(queryEnd.Lat, queryEnd.Lon, rideEnd.Lat, rideEnd.Lon)
	detourRatio := (viaKm - rideTripKm) / math.Max(rideTripKm, 0.1)
	if detourRatio > 0.28 {
		return 0, "", deviationKm, progress
	}

	fit := 1 - (deviationKm / corridorKm)
	progressBonus := 24 * progress
	detourBonus := math.Max(0, 36-(detourRatio*120))
	return 170 + fit*120 + progressBonus + detourBonus, "On the way", deviationKm, progress
}

func scoreAvailabilityAndPrice(ride models.Ride, params SearchParams) float64 {
	score := 0.0
	seatsLeft := helpers.PassengerSeatsLeft(ride.TotalSeats, ride.BookedSeats)
	if ride.TotalSeats > 0 {
		score += 16 * (float64(seatsLeft) / math.Max(1, float64(helpers.PassengerCapacity(ride.TotalSeats))))
	}
	if seatsLeft > 1 {
		score += 8
	}
	if params.MinSeats != nil && seatsLeft >= *params.MinSeats {
		score += 12
	}
	if params.MaxPrice != nil && ride.TotalPrice <= *params.MaxPrice {
		score += 16
		if ride.TotalPrice <= *params.MaxPrice/2 {
			score += 8
		}
	}
	return score
}

func scorePreferredTimeBucket(rideTime time.Time, params SearchParams) float64 {
	if params.HasTargetTime {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(params.PreferredTime)) {
	case "morning":
		if inHourRange(rideTime, 6, 12) {
			return 34
		}
		return -16
	case "afternoon":
		if inHourRange(rideTime, 12, 17) {
			return 34
		}
		return -16
	case "evening":
		if inHourRange(rideTime, 17, 21) {
			return 34
		}
		return -16
	case "night":
		h := rideTime.In(searchLocation()).Hour()
		if h >= 21 || h < 6 {
			return 34
		}
		return -16
	default:
		return 0
	}
}

func scoreTextFit(query, value string, maxScore float64) float64 {
	q := normalizeLocationText(query)
	v := normalizeLocationText(value)
	if q == "" || v == "" {
		return 0
	}
	switch {
	case q == v:
		return maxScore
	case strings.Contains(v, q) || strings.Contains(q, v):
		return maxScore * 0.78
	}

	words := strings.Fields(q)
	if len(words) == 0 {
		return 0
	}
	matched := 0
	for _, word := range words {
		if len(word) >= 3 && strings.Contains(v, word) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return maxScore * 0.42 * (float64(matched) / float64(len(words)))
}

func fallbackMatchReason(startDist, endDist *float64, params SearchParams) string {
	if endDist != nil && *endDist <= 6 {
		return "Nearby destination"
	}
	if startDist != nil && *startDist <= 2 {
		return "Close pickup"
	}
	if params.HasTargetTime {
		return "Close time"
	}
	return ""
}

func requestedTripDistanceKm(params SearchParams) float64 {
	if !params.HasStartCoord || !params.HasEndCoord {
		return 0
	}
	return helpers.CalculateDistance(params.StartLat, params.StartLon, params.EndLat, params.EndLon)
}

func routeCorridorKm(rideTripKm, requestedTripKm float64) float64 {
	scale := math.Max(rideTripKm, requestedTripKm)
	corridor := scale * 0.08
	if corridor < 1.2 {
		corridor = 1.2
	}
	if corridor > 8 {
		corridor = 8
	}
	return corridor
}

func pointSegmentDistanceKm(point, start, end geoPoint) (float64, float64) {
	refLat := (point.Lat + start.Lat + end.Lat) / 3
	px, py := projectKm(point, refLat)
	ax, ay := projectKm(start, refLat)
	bx, by := projectKm(end, refLat)

	abx := bx - ax
	aby := by - ay
	apx := px - ax
	apy := py - ay
	ab2 := abx*abx + aby*aby
	if ab2 == 0 {
		return math.Hypot(px-ax, py-ay), 0
	}
	t := (apx*abx + apy*aby) / ab2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	cx := ax + t*abx
	cy := ay + t*aby
	return math.Hypot(px-cx, py-cy), t
}

func projectKm(p geoPoint, refLat float64) (float64, float64) {
	const kmPerDegreeLat = 110.574
	kmPerDegreeLon := 111.320 * math.Cos(refLat*math.Pi/180)
	return p.Lon * kmPerDegreeLon, p.Lat * kmPerDegreeLat
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	inLoc := t.In(loc)
	return time.Date(inLoc.Year(), inLoc.Month(), inLoc.Day(), 0, 0, 0, 0, loc)
}

func searchLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.UTC
	}
	return loc
}

func inHourRange(t time.Time, startHour, endHour int) bool {
	h := t.In(searchLocation()).Hour()
	return h >= startHour && h < endHour
}

func normalizeLocationText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "junction", "jn")
	value = strings.ReplaceAll(value, "railway station", "station")
	value = strings.ReplaceAll(value, "bus stand", "busstand")
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// buildMatchSignals composes the structured signal list for a ride
// card. It reads from both the scoring pass (timing / route fit) and
// the surrounding context (host frequency, repeat route, freshness)
// so each chip carries the most concrete value we have — a deviation
// in km, a minute delta, a host's past-ride count.
//
// The returned slice is sorted high-priority-first so the client can
// slice the first 3 (or however many fit) without resorting. Final
// length is capped at 6 — anything past that is noise on a chip
// strip.
//
// Signals intentionally don't overlap with `viewer_state` (host /
// confirmed_passenger / etc.) which already drives the CTA — the
// chips only ever justify WHY this ride ranked where it did. CTAs
// stay on the card surface.
func buildMatchSignals(
	scored rideSearchScore,
	ride models.Ride,
	params SearchParams,
	startDist, endDist *float64,
	hostPastRides int64,
	repeatRoute bool,
	now time.Time,
) []MatchSignal {
	signals := make([]MatchSignal, 0, 8)
	loc := searchLocation()

	// --- Time / date fit -------------------------------------------------
	// "Exact time" only fires when the user gave us a target time AND the
	// ride is within ±90 minutes. Outside that band, surface the ride's
	// time as informational instead.
	if params.HasTargetTime && scored.TargetTimeDeltaM != nil {
		absM := math.Abs(*scored.TargetTimeDeltaM)
		rideClock := ride.StartTime.In(loc).Format("3:04 PM")
		switch {
		case absM <= 15:
			signals = append(signals, MatchSignal{
				Kind:     "exact_time",
				Label:    "Exact time",
				Detail:   "Departs " + rideClock,
				Tone:     SignalTonePrimary,
				priority: 100,
			})
		case absM <= 45:
			signals = append(signals, MatchSignal{
				Kind:     "close_time",
				Label:    "Close to your time",
				Detail:   minuteDeltaPhrase(*scored.TargetTimeDeltaM),
				Tone:     SignalTonePrimary,
				priority: 92,
			})
		case absM <= 120:
			signals = append(signals, MatchSignal{
				Kind:     "flexible_time",
				Label:    "Flexible by " + roundedHoursPhrase(absM),
				Detail:   "Departs " + rideClock,
				Tone:     SignalToneTime,
				priority: 64,
			})
		}
	}

	// Date fit — only emit when caller gave us a target date. Same-day is
	// the strong signal; ±1 day is a soft warning chip.
	if scored.TargetDateDeltaD != nil {
		d := *scored.TargetDateDeltaD
		switch {
		case d == 0:
			signals = append(signals, MatchSignal{
				Kind:     "selected_date",
				Label:    "Your selected date",
				Tone:     SignalTonePrimary,
				priority: 98,
			})
		case d == 1:
			signals = append(signals, MatchSignal{
				Kind:     "next_day",
				Label:    "Next day",
				Tone:     SignalToneTime,
				priority: 58,
			})
		case d == -1:
			signals = append(signals, MatchSignal{
				Kind:     "previous_day",
				Label:    "Day before",
				Tone:     SignalToneTime,
				priority: 58,
			})
		}
	}

	// --- Repeat route (strongest social signal) --------------------------
	if repeatRoute {
		signals = append(signals, MatchSignal{
			Kind:     "repeat_route",
			Label:    "Your usual route",
			Tone:     SignalTonePrimary,
			priority: 96,
		})
	}

	// --- Route overlap ("on the way") ------------------------------------
	// scoreRouteOverlap returns a non-zero score only when the requested
	// destination falls inside the ride's corridor. The deviation is the
	// perpendicular distance from the ride's route to the requested point.
	if scored.RouteOverlapScore > 0 && scored.RouteDeviationKm != nil {
		dev := *scored.RouteDeviationKm
		signals = append(signals, MatchSignal{
			Kind:     "on_the_way",
			Label:    "On the way",
			Detail:   fmt.Sprintf("%s off route", formatKm(dev)),
			Tone:     SignalToneRoute,
			priority: 90,
		})
	}

	// --- Endpoint proximity ----------------------------------------------
	// Drop-off chip wins over pickup chip when both qualify, since the
	// destination is the more decision-affecting endpoint for the rider.
	if endDist != nil {
		d := *endDist
		switch {
		case d <= 0.35:
			signals = append(signals, MatchSignal{
				Kind:     "exact_destination",
				Label:    "Exact drop-off",
				Tone:     SignalTonePrimary,
				priority: 88,
			})
		case d <= 1:
			signals = append(signals, MatchSignal{
				Kind:     "near_destination",
				Label:    "Very close drop-off",
				Detail:   formatKm(d),
				Tone:     SignalToneSpatial,
				priority: 84,
			})
		case d <= 2.5:
			signals = append(signals, MatchSignal{
				Kind:     "nearby_destination",
				Label:    "Nearby drop-off",
				Detail:   formatKm(d),
				Tone:     SignalToneSpatial,
				priority: 72,
			})
		case d <= 6:
			signals = append(signals, MatchSignal{
				Kind:     "same_area_destination",
				Label:    "Same area",
				Detail:   formatKm(d),
				Tone:     SignalToneSpatial,
				priority: 50,
			})
		}
	}

	if startDist != nil {
		d := *startDist
		switch {
		case d <= 0.35:
			signals = append(signals, MatchSignal{
				Kind:     "exact_pickup",
				Label:    "Pickup at your spot",
				Tone:     SignalTonePrimary,
				priority: 86,
			})
		case d <= 1:
			signals = append(signals, MatchSignal{
				Kind:     "near_pickup",
				Label:    "Very close pickup",
				Detail:   formatKm(d),
				Tone:     SignalToneSpatial,
				priority: 78,
			})
		case d <= 3:
			signals = append(signals, MatchSignal{
				Kind:     "nearby_pickup",
				Label:    "Nearby pickup",
				Detail:   formatKm(d),
				Tone:     SignalToneSpatial,
				priority: 60,
			})
		}
	}

	// --- Host trust ------------------------------------------------------
	switch {
	case hostPastRides >= 10:
		signals = append(signals, MatchSignal{
			Kind:     "top_host",
			Label:    "Top host",
			Detail:   fmt.Sprintf("%d rides hosted", hostPastRides),
			Tone:     SignalToneSocial,
			priority: 75,
		})
	case hostPastRides >= 3:
		signals = append(signals, MatchSignal{
			Kind:     "trusted_host",
			Label:    "Trusted host",
			Detail:   fmt.Sprintf("%d rides hosted", hostPastRides),
			Tone:     SignalToneSocial,
			priority: 68,
		})
	}

	// --- Freshness -------------------------------------------------------
	// "Just listed" wins when the ride is < 60min old. Mutually exclusive
	// with the date chips — we only need one urgency signal.
	if !ride.CreatedAt.IsZero() && now.Sub(ride.CreatedAt) < 60*time.Minute {
		signals = append(signals, MatchSignal{
			Kind:     "just_listed",
			Label:    "Just listed",
			Tone:     SignalToneFresh,
			priority: 70,
		})
	}

	// --- Day-of bucket (only when no explicit target date matched) -------
	// If the user didn't give us a date, scoreDateFit doesn't fill
	// TargetDateDeltaD — so the only date framing they get is from this
	// bucket pass. Today / Tomorrow / This week, in that order of weight.
	if scored.TargetDateDeltaD == nil && !params.HasTargetTime {
		nowDay := dayStart(now.In(loc), loc)
		rideDay := dayStart(ride.StartTime.In(loc), loc)
		days := int(math.Round(rideDay.Sub(nowDay).Hours() / 24))
		switch {
		case days == 0:
			diffH := ride.StartTime.Sub(now).Hours()
			if diffH >= 0 && diffH < 1.5 {
				signals = append(signals, MatchSignal{
					Kind:     "leaving_soon",
					Label:    "Leaving soon",
					Detail:   "Today " + ride.StartTime.In(loc).Format("3:04 PM"),
					Tone:     SignalToneFresh,
					priority: 80,
				})
			} else {
				signals = append(signals, MatchSignal{
					Kind:     "today",
					Label:    "Today's ride",
					Detail:   ride.StartTime.In(loc).Format("3:04 PM"),
					Tone:     SignalToneTime,
					priority: 62,
				})
			}
		case days == 1:
			signals = append(signals, MatchSignal{
				Kind:     "tomorrow",
				Label:    "Tomorrow",
				Detail:   ride.StartTime.In(loc).Format("3:04 PM"),
				Tone:     SignalToneTime,
				priority: 56,
			})
		case days > 1 && days <= 7:
			signals = append(signals, MatchSignal{
				Kind:     "this_week",
				Label:    "This week",
				Detail:   ride.StartTime.In(loc).Format("Mon 3:04 PM"),
				Tone:     SignalToneTime,
				priority: 42,
			})
		}
	}

	// --- Capacity --------------------------------------------------------
	seatsLeft := helpers.PassengerSeatsLeft(ride.TotalSeats, ride.BookedSeats)
	if params.MinSeats != nil && seatsLeft >= *params.MinSeats && *params.MinSeats >= 2 {
		signals = append(signals, MatchSignal{
			Kind:     "fits_group",
			Label:    fmt.Sprintf("Fits %d", *params.MinSeats),
			Tone:     SignalToneCapacity,
			priority: 54,
		})
	} else if seatsLeft >= 3 {
		signals = append(signals, MatchSignal{
			Kind:     "multiple_seats",
			Label:    fmt.Sprintf("%d seats left", seatsLeft),
			Tone:     SignalToneCapacity,
			priority: 38,
		})
	} else if seatsLeft == 1 {
		signals = append(signals, MatchSignal{
			Kind:     "last_seat",
			Label:    "Last seat",
			Tone:     SignalToneFresh,
			priority: 66,
		})
	}

	// --- Value -----------------------------------------------------------
	if params.MaxPrice != nil {
		half := *params.MaxPrice / 2
		switch {
		case ride.TotalPrice <= half:
			perSeat := uint(0)
			if ride.TotalSeats > 0 {
				perSeat = ride.TotalPrice / ride.TotalSeats
			}
			detail := ""
			if perSeat > 0 {
				detail = fmt.Sprintf("₹%d a seat", perSeat)
			}
			signals = append(signals, MatchSignal{
				Kind:     "great_price",
				Label:    "Great price",
				Detail:   detail,
				Tone:     SignalToneValue,
				priority: 52,
			})
		case ride.TotalPrice <= *params.MaxPrice:
			signals = append(signals, MatchSignal{
				Kind:     "within_budget",
				Label:    "Within budget",
				Tone:     SignalToneValue,
				priority: 36,
			})
		}
	}

	// Sort high-priority first, stable on label to keep the order
	// deterministic across requests.
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].priority != signals[j].priority {
			return signals[i].priority > signals[j].priority
		}
		return signals[i].Label < signals[j].Label
	})

	// Cap at 6. The frontend will render only the first 3-4 in the
	// compact shelf; the rest are available if a card grows into an
	// expanded view later.
	if len(signals) > 6 {
		signals = signals[:6]
	}
	return signals
}

// minuteDeltaPhrase turns a signed minute delta into "10 min earlier"
// or "15 min later" copy for use as a chip detail line.
func minuteDeltaPhrase(diffM float64) string {
	abs := int(math.Round(math.Abs(diffM)))
	if abs == 0 {
		return "On time"
	}
	if diffM < 0 {
		return fmt.Sprintf("%d min earlier", abs)
	}
	return fmt.Sprintf("%d min later", abs)
}

// roundedHoursPhrase returns "1 hr" / "2 hrs" / "90 min" depending on
// magnitude. Used in the flexible-time chip.
func roundedHoursPhrase(absMinutes float64) string {
	if absMinutes < 60 {
		return fmt.Sprintf("%d min", int(math.Round(absMinutes)))
	}
	hours := absMinutes / 60
	rounded := math.Round(hours)
	if rounded <= 1.5 {
		return "1 hr"
	}
	if rounded == 2 {
		return "2 hrs"
	}
	return fmt.Sprintf("%.0f hrs", rounded)
}

// formatKm renders a distance for chip-sized text. Sub-1km values get
// metres for precision; everything else rounds to one decimal.
func formatKm(km float64) string {
	if km < 0.05 {
		return "<50 m"
	}
	if km < 1 {
		return fmt.Sprintf("%d m", int(math.Round(km*1000/10)*10))
	}
	if km < 10 {
		return fmt.Sprintf("%.1f km", km)
	}
	return fmt.Sprintf("%.0f km", km)
}
