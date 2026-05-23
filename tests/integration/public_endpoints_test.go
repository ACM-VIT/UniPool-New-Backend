package integration

// Regression tests for public (unauthenticated) endpoints used by the
// HomeScreen map and the verify-academic-status sheet. Same PostGIS
// risk surface as /ride/search — NearbyRides hits ST_DWithin too.

import (
	"net/http"
	"testing"
)

func TestNearbyRidesCount_ReturnsCountInRadius(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "near-host@vitstudent.ac.in", &inst.ID)
	// Seed two rides near VIT and one in Chennai.
	SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})
	SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9710), StartLon: FloatPtr(79.1580),
		EndLat: FloatPtr(12.9720), EndLon: FloatPtr(79.1430),
	})
	SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(13.0827), StartLon: FloatPtr(80.2707),
		EndLat: FloatPtr(13.0784), EndLon: FloatPtr(80.2611),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/rides/nearby-count?lat=12.9692&lng=79.1559&radius=2000", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Count int `json:"count"`
	}
	ReadJSON(t, resp, &body)
	if body.Count != 2 {
		t.Errorf("expected 2 nearby rides, got %d", body.Count)
	}
}

func TestNearbyRides_ReturnsPinsForMap(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pin-host@vitstudent.ac.in", &inst.ID)
	target := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/rides/nearby?lat=12.9692&lng=79.1559&radius=1000", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
		Count int              `json:"count"`
	}
	ReadJSON(t, resp, &body)
	if body.Count == 0 {
		t.Fatalf("expected at least 1 nearby ride; suggests PostGIS regression")
	}
	found := false
	for _, r := range body.Rides {
		if id, _ := r["id"].(string); id == target.ID.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("seeded ride %s not in nearby pin list", target.ID)
	}
}

func TestNearbyRides_RejectsMissingCoords(t *testing.T) {
	_ = ConnectTestDB(t)
	ResetDB(t)
	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/rides/nearby?radius=1000", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 without lat/lng, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}

func TestInstitutes_PublicListing(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	SeedInstitute(t, db, "IIT Madras", "India", "smail.iitm.ac.in")

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/institutes", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw []byte = ReadJSON(t, resp, nil)
	if len(raw) == 0 {
		t.Errorf("empty response")
	}
}

func TestInstitutesSearch_MatchByName(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	SeedInstitute(t, db, "IIT Madras", "India", "smail.iitm.ac.in")

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/institutes/search?q=vit", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	raw := ReadJSON(t, resp, nil)
	// Just check the response is non-empty JSON and contains VIT — the
	// specific shape (whether it returns an array or a wrapped object)
	// is left flexible so this test doesn't break when the response
	// shape evolves.
	if len(raw) == 0 {
		t.Errorf("empty response")
	}
	if !contains(string(raw), "VIT") {
		t.Errorf("expected VIT in response; got %s", string(raw))
	}
	if contains(string(raw), "IIT Madras") {
		t.Errorf("IIT Madras should not match q=vit; got %s", string(raw))
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
