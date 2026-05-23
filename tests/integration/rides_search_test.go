package integration

// Regression tests for GET /ride/search. The PostGIS cast bug fixed in
// f14457e would have manifested here — the handler swallowed the SQL
// error and returned an empty list, so without an assertion that
// seeded rides actually come back these tests would have caught it.

import (
	"net/http"
	"testing"
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
		StartLocation:  "VIT Main Gate",
		EndLocation:    "Katpadi Junction",
		StartLat:       FloatPtr(mainGateLat),
		StartLon:       FloatPtr(mainGateLon),
		EndLat:         FloatPtr(katpadiLat),
		EndLon:         FloatPtr(katpadiLon),
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
	var body struct{ Rides []map[string]any `json:"rides"` }
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
	var body struct{ Rides []map[string]any `json:"rides"` }
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
	var body struct{ Rides []map[string]any `json:"rides"` }
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
	var body struct{ Rides []map[string]any `json:"rides"` }
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
