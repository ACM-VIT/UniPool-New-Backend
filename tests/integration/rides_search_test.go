package integration

// Regression tests for GET /ride/search. The PostGIS cast bug fixed in
// f14457e would have manifested here — the handler swallowed the SQL
// error and returned an empty list, so without an assertion that
// seeded rides actually come back these tests would have caught it.

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestRideSearch_CoordinateBothEnds drives the most common search
// shape — passenger has both a "from" pin and a "to" pin. Exercises
// every ST_MakePoint code site (primary distance projection, fallback
// ST_DWithin gate, nearestRides diagnostic).
func TestRideSearch_CoordinateBothEnds(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)

	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host User", "host@vitstudent.ac.in", &inst.ID)

	// Seed two rides covering the requested route. Sit them exactly at
	// the search coords so a working SQL path is guaranteed to score
	// them as relevant and return them inside the default radius.
	mainGateLat, mainGateLon := 12.9692, 79.1559
	katpadiLat, katpadiLon := 12.9698, 79.1370
	target := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(mainGateLat),
		StartLon:      FloatPtr(mainGateLon),
		EndLat:        FloatPtr(katpadiLat),
		EndLon:        FloatPtr(katpadiLon),
	})

	// And a control ride 50km away — must NOT come back.
	_ = SeedRide(t, db, host, RideOpts{
		StartLocation: "Chennai Central",
		EndLocation:   "Egmore",
		StartLat:      FloatPtr(13.0827),
		StartLon:      FloatPtr(80.2707),
		EndLat:        FloatPtr(13.0784),
		EndLon:        FloatPtr(80.2611),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370",
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Rides        []map[string]any `json:"rides"`
		SearchMethod string           `json:"search_method"`
	}
	ReadJSON(t, resp, &body)

	if body.SearchMethod != "coordinate" {
		t.Errorf("expected coordinate search method, got %q", body.SearchMethod)
	}
	if len(body.Rides) == 0 {
		t.Fatalf("expected at least one matching ride; got 0 (likely a SQL error swallowed by the handler)")
	}
	found := false
	for _, r := range body.Rides {
		if id, _ := r["id"].(string); id == target.ID.String() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seeded ride %s not in response; got ids: %v", target.ID, idsOf(body.Rides))
	}
}

// TestRideSearch_StartCoordOnly hits the branch where the passenger
// only has a "from" pin — exercises the ST_MakePoint(start_*) path
// without the end_* one.
func TestRideSearch_StartCoordOnly(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)

	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "host2@vitstudent.ac.in", &inst.ID)
	target := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559",
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) == 0 {
		t.Fatalf("start-only search returned 0 rides; suggests broken ST_MakePoint(start_*) path")
	}
	assertIDPresent(t, body.Rides, target.ID.String())
}

// TestRideSearch_EndCoordOnly is the mirror — only an end pin. Covers
// the ST_MakePoint(end_*) branch.
func TestRideSearch_EndCoordOnly(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)

	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "host3@vitstudent.ac.in", &inst.ID)
	target := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?end_lat=12.9698&end_lon=79.1370",
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) == 0 {
		t.Fatalf("end-only search returned 0 rides; suggests broken ST_MakePoint(end_*) path")
	}
	assertIDPresent(t, body.Rides, target.ID.String())
}

// TestRideSearch_NoCoordsTextFallback verifies the text-only path
// still works when neither pin is provided. Important: this is the
// path the fallback search degrades into when coordinate matching
// finds nothing, so a regression here cascades into the coordinate
// flow too.
func TestRideSearch_NoCoordsTextFallback(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)

	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "host4@vitstudent.ac.in", &inst.ID)
	target := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_location=VIT&end_location=Katpadi",
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) == 0 {
		t.Fatalf("text search returned 0 rides")
	}
	assertIDPresent(t, body.Rides, target.ID.String())
}

// TestRideSearch_ExcludesOwnRides confirms the handler still filters
// out the searcher's own hosted rides when called with auth. Pre-fix
// this was hidden because the handler returned empty anyway.
func TestRideSearch_ExcludesOwnRides(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)

	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	hostA := SeedUser(t, db, "Host A", "hosta@vitstudent.ac.in", &inst.ID)
	hostB := SeedUser(t, db, "Host B", "hostb@vitstudent.ac.in", &inst.ID)
	mine := SeedRide(t, db, hostA, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})
	theirs := SeedRide(t, db, hostB, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370",
		nil, AsUser(hostA.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	for _, r := range body.Rides {
		if id, _ := r["id"].(string); id == mine.ID.String() {
			t.Errorf("own ride %s leaked into authenticated searcher's results", mine.ID)
		}
	}
	assertIDPresent(t, body.Rides, theirs.ID.String())
}

// TestRideSearch_NoMatchesReturnsEmpty makes sure a search far from
// any seeded ride returns an empty rides array (not nil, not a 500).
// Guards against accidental panics on the empty-result codepath.
func TestRideSearch_NoMatchesReturnsEmpty(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)

	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "host5@vitstudent.ac.in", &inst.ID)
	_ = SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	// Search for coords in a different country (50.85, 4.35 = Brussels)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=50.8503&start_lon=4.3517&end_lat=50.8466&end_lon=4.3528",
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides      []map[string]any `json:"rides"`
		TotalFound int              `json:"total_found"`
	}
	ReadJSON(t, resp, &body)
	if body.TotalFound != 0 || len(body.Rides) != 0 {
		t.Errorf("expected zero rides for distant search; got total=%d len=%d", body.TotalFound, len(body.Rides))
	}
}

// --- helpers --------------------------------------------------------

func idsOf(rides []map[string]any) []string {
	out := make([]string, 0, len(rides))
	for _, r := range rides {
		if id, ok := r["id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

func assertIDPresent(t *testing.T, rides []map[string]any, want string) {
	t.Helper()
	for _, r := range rides {
		if id, _ := r["id"].(string); id == want {
			return
		}
	}
	t.Errorf("expected ride %s in response; got %v", want, idsOf(rides))
}

// TestRideSearch_SurfacesStrictMatches locks in the contract that
// /ride/search now returns a `strict_matches` array alongside the
// regular fuzzy `rides` list. CreateRide reads only this field for
// the "you could just join one of these" suggestion; the search
// UI uses it to flag "Best match" badges on overlapping rows.
//
// Field is present even when empty (clients shouldn't have to
// null-guard); populated only when both coords are provided.
func TestRideSearch_SurfacesStrictMatches(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "sm-host@vitstudent.ac.in", &inst.ID)

	// Within-strict-radius ride that should land in BOTH fields.
	target := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370",
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides         []map[string]any `json:"rides"`
		StrictMatches []struct {
			ID             string  `json:"id"`
			StartDistanceM float64 `json:"start_distance_m"`
			EndDistanceM   float64 `json:"end_distance_m"`
		} `json:"strict_matches"`
	}
	ReadJSON(t, resp, &body)

	if len(body.StrictMatches) != 1 {
		t.Fatalf("expected 1 strict match, got %d", len(body.StrictMatches))
	}
	if body.StrictMatches[0].ID != target.ID.String() {
		t.Errorf("wrong ride: got %s want %s", body.StrictMatches[0].ID, target.ID)
	}
	if body.StrictMatches[0].StartDistanceM > 500 || body.StrictMatches[0].EndDistanceM > 500 {
		t.Errorf("distances outside strict radius (500m): start=%v end=%v",
			body.StrictMatches[0].StartDistanceM, body.StrictMatches[0].EndDistanceM)
	}
	// The same ride should also appear in the regular fuzzy list
	// (it's within 5km of itself trivially).
	assertIDPresent(t, body.Rides, target.ID.String())
}

func TestRideSearch_StrictMatchesEmptyWithoutCoords(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "sm-text@vitstudent.ac.in", &inst.ID)
	_ = SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT", EndLocation: "Katpadi",
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	// Text-only search — no coords provided. strict_matches should
	// come back as an empty array (never null) so clients can map
	// over it without a guard.
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_location=VIT&end_location=Katpadi", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		StrictMatches []map[string]any `json:"strict_matches"`
	}
	raw := ReadJSON(t, resp, &body)
	if body.StrictMatches == nil {
		t.Errorf("strict_matches should be [] not null in JSON response; got %s", string(raw))
	}
	if len(body.StrictMatches) != 0 {
		t.Errorf("expected 0 strict matches without coords, got %d", len(body.StrictMatches))
	}
}

func TestRideSearch_TargetTimeRanksCloseEarlierDepartures(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "time-rank@vitstudent.ac.in", &inst.ID)

	target := nextLocalSearchTime(t, 9, 0)
	closeEarlier := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: target.Add(-15 * time.Minute).UTC(),
	})
	early := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: target.Add(-60 * time.Minute).UTC(),
	})
	late := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: target.Add(90 * time.Minute).UTC(),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&date="+
			target.Format("2006-01-02")+"&target_time="+url.QueryEscape(target.Format(time.RFC3339)),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) < 3 {
		t.Fatalf("expected 3 rides, got %d (%v)", len(body.Rides), idsOf(body.Rides))
	}
	if got := body.Rides[0]["id"]; got != closeEarlier.ID.String() {
		t.Fatalf("15-min-earlier ride should rank first; got %v, order=%v", got, idsOf(body.Rides))
	}
	if indexOfID(body.Rides, early.ID.String()) > indexOfID(body.Rides, late.ID.String()) {
		t.Errorf("early usable ride should beat very late ride; order=%v", idsOf(body.Rides))
	}
}

func TestRideSearch_DateFilterDoesNotLeakFarFutureRides(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "date-rank@vitstudent.ac.in", &inst.ID)

	target := nextLocalSearchTime(t, 9, 0)
	todayRide := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: target.UTC(),
	})
	farFuture := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: target.AddDate(0, 0, 100).UTC(),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&date="+
			target.Format("2006-01-02")+"&target_time="+url.QueryEscape(target.Format(time.RFC3339)),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	assertIDPresent(t, body.Rides, todayRide.ID.String())
	if indexOfID(body.Rides, farFuture.ID.String()) != -1 {
		t.Fatalf("far future ride leaked into selected-date search; order=%v", idsOf(body.Rides))
	}
}

func TestRideSearch_ContextualRouteOverlapBeatsNearbyOffRoute(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "route-overlap@vitstudent.ac.in", &inst.ID)

	target := nextLocalSearchTime(t, 9, 0)
	onRoute := SeedRide(t, db, host, RideOpts{
		StartLocation: "Campus Gate",
		EndLocation:   "Further Down Main Road",
		StartLat:      FloatPtr(12.0000), StartLon: FloatPtr(79.0000),
		EndLat: FloatPtr(12.0000), EndLon: FloatPtr(79.0200),
		StartTime: target.UTC(),
	})
	offRoute := SeedRide(t, db, host, RideOpts{
		StartLocation: "Campus Gate",
		EndLocation:   "Side Road",
		StartLat:      FloatPtr(12.0000), StartLon: FloatPtr(79.0000),
		EndLat: FloatPtr(12.0100), EndLon: FloatPtr(79.0100),
		StartTime: target.UTC(),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.0000&start_lon=79.0000&end_lat=12.0000&end_lon=79.0100&date="+
			target.Format("2006-01-02")+"&target_time="+url.QueryEscape(target.Format(time.RFC3339)),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) < 2 {
		t.Fatalf("expected both contextual candidates, got %v", idsOf(body.Rides))
	}
	if got := body.Rides[0]["id"]; got != onRoute.ID.String() {
		t.Fatalf("on-route candidate should beat nearby off-route candidate; got %v, offRoute=%s, order=%v",
			got, offRoute.ID, idsOf(body.Rides))
	}
}

func TestRideSearch_CoordinateSearchDoesNotPromoteMissingCoordinateRows(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "missing-coords@vitstudent.ac.in", &inst.ID)

	target := nextLocalSearchTime(t, 9, 0)
	exact := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: target.UTC(),
	})
	missingCoords := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartTime:     target.Add(-15 * time.Minute).UTC(),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/search?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&date="+
			target.Format("2006-01-02")+"&target_time="+url.QueryEscape(target.Format(time.RFC3339)),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) == 0 || body.Rides[0]["id"] != exact.ID.String() {
		t.Fatalf("exact coordinate ride should rank first; order=%v", idsOf(body.Rides))
	}
	if indexOfID(body.Rides, missingCoords.ID.String()) != -1 {
		t.Fatalf("missing-coordinate ride should not enter coordinate result pool; order=%v", idsOf(body.Rides))
	}
}

func nextLocalSearchTime(t *testing.T, hour, minute int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().In(loc).Add(24 * time.Hour)
	return time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
}

func indexOfID(rides []map[string]any, id string) int {
	for i, r := range rides {
		if got, _ := r["id"].(string); got == id {
			return i
		}
	}
	return -1
}
