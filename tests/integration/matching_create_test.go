package integration

// Regression tests for GET /ride/matching-create. The endpoint
// powers the "already going there?" suggestion on CreateRide; if it
// stops matching (false negatives) duplicate rides proliferate; if
// it over-matches (false positives) users get nagged away from
// posting legitimate new rides.

import (
	"net/http"
	"testing"
	"time"
)

func TestMatchingCreate_StrictBothEndsWithinRadius(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "match-host@vitstudent.ac.in", &inst.ID)

	// Mainline match: VIT Main Gate → Katpadi Junction, starting in 2h.
	startT := time.Now().Add(2 * time.Hour).UTC()
	target := SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(12.9692),
		StartLon:      FloatPtr(79.1559),
		EndLat:        FloatPtr(12.9698),
		EndLon:        FloatPtr(79.1370),
		StartTime:     startT,
	})

	// Wrong-start ride: starts 3km away (outside 1km radius), same
	// end. Must NOT match.
	_ = SeedRide(t, db, host, RideOpts{
		StartLocation: "Chittoor Road",
		EndLocation:   "Katpadi Junction",
		StartLat:      FloatPtr(12.9450),
		StartLon:      FloatPtr(79.1300),
		EndLat:        FloatPtr(12.9698),
		EndLon:        FloatPtr(79.1370),
		StartTime:     startT,
	})

	// Wrong-end ride: same start, 5km-away end. Must NOT match.
	_ = SeedRide(t, db, host, RideOpts{
		StartLocation: "VIT Main Gate",
		EndLocation:   "Sripuram",
		StartLat:      FloatPtr(12.9692),
		StartLon:      FloatPtr(79.1559),
		EndLat:        FloatPtr(13.0150),
		EndLon:        FloatPtr(79.1050),
		StartTime:     startT,
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&radius_m=1000&start_time="+startT.Format(time.RFC3339),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Matches []struct {
			ID             string  `json:"id"`
			StartDistanceM float64 `json:"start_distance_m"`
			EndDistanceM   float64 `json:"end_distance_m"`
		} `json:"matches"`
		Count int `json:"count"`
	}
	ReadJSON(t, resp, &body)

	if body.Count != 1 {
		t.Fatalf("expected 1 strict match, got %d", body.Count)
	}
	if body.Matches[0].ID != target.ID.String() {
		t.Errorf("returned wrong ride: got %s want %s", body.Matches[0].ID, target.ID)
	}
	if body.Matches[0].StartDistanceM > 1000 || body.Matches[0].EndDistanceM > 1000 {
		t.Errorf("distance fields outside the 1km gate: start=%v end=%v",
			body.Matches[0].StartDistanceM, body.Matches[0].EndDistanceM)
	}
}

func TestMatchingCreate_RespectsTimeWindow(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "win-host@vitstudent.ac.in", &inst.ID)

	now := time.Now().UTC()
	// Inside-window ride at +3h from "now" requested time, well
	// under the 4h default window.
	inWindow := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: now.Add(3 * time.Hour),
	})
	// Outside-window ride at +10h. Must NOT match the default 4h.
	_ = SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: now.Add(10 * time.Hour),
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&start_time="+now.Format(time.RFC3339),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Matches []struct {
			ID string `json:"id"`
		} `json:"matches"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Matches) != 1 || body.Matches[0].ID != inWindow.ID.String() {
		ids := make([]string, 0, len(body.Matches))
		for _, m := range body.Matches {
			ids = append(ids, m.ID)
		}
		t.Errorf("expected 1 in-window match, got %v", ids)
	}
}

func TestMatchingCreate_ExcludesOwnRides(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	me := SeedUser(t, db, "Me", "match-me@vitstudent.ac.in", &inst.ID)
	other := SeedUser(t, db, "Other", "match-other@vitstudent.ac.in", &inst.ID)

	startT := time.Now().Add(2 * time.Hour).UTC()
	// My own ride at the same route — should NOT be suggested back
	// to me.
	_ = SeedRide(t, db, me, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: startT,
	})
	// Someone else's ride at the same route — SHOULD show up.
	theirs := SeedRide(t, db, other, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: startT,
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&start_time="+startT.Format(time.RFC3339),
		nil, AsUser(me.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Matches []struct {
			ID string `json:"id"`
		} `json:"matches"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Matches) != 1 || body.Matches[0].ID != theirs.ID.String() {
		ids := make([]string, 0, len(body.Matches))
		for _, m := range body.Matches {
			ids = append(ids, m.ID)
		}
		t.Errorf("expected only the other host's ride; got %v", ids)
	}
}

func TestMatchingCreate_SkipsFullRides(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "full-host@vitstudent.ac.in", &inst.ID)

	startT := time.Now().Add(2 * time.Hour).UTC()
	// Ride with no open seats. Should NOT match — a full ride is
	// no use as a "join instead" suggestion.
	_ = SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime:   startT,
		TotalSeats:  3,
		BookedSeats: 3,
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&start_time="+startT.Format(time.RFC3339),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Count int `json:"count"`
	}
	ReadJSON(t, resp, &body)
	if body.Count != 0 {
		t.Errorf("expected 0 (full ride filtered), got %d", body.Count)
	}
}

func TestMatchingCreate_OrdersByCombinedDistance(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "ord-host@vitstudent.ac.in", &inst.ID)

	startT := time.Now().Add(2 * time.Hour).UTC()
	// Closer match (~50m on each end)
	closer := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9695), StartLon: FloatPtr(79.1562),
		EndLat: FloatPtr(12.9700), EndLon: FloatPtr(79.1373),
		StartTime: startT,
	})
	// Farther match (~700m on each end, still inside 1km radius)
	_ = SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9755), StartLon: FloatPtr(79.1625),
		EndLat: FloatPtr(12.9760), EndLon: FloatPtr(79.1305),
		StartTime: startT,
	})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&start_time="+startT.Format(time.RFC3339),
		nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Matches []struct {
			ID             string  `json:"id"`
			StartDistanceM float64 `json:"start_distance_m"`
			EndDistanceM   float64 `json:"end_distance_m"`
		} `json:"matches"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Matches) < 2 {
		t.Fatalf("expected 2 matches, got %d", len(body.Matches))
	}
	if body.Matches[0].ID != closer.ID.String() {
		t.Errorf("closer match should be first; got order %s, %s", body.Matches[0].ID, body.Matches[1].ID)
	}
	if body.Matches[0].StartDistanceM+body.Matches[0].EndDistanceM >
		body.Matches[1].StartDistanceM+body.Matches[1].EndDistanceM {
		t.Errorf("ordering not by combined distance: %v",
			[]float64{
				body.Matches[0].StartDistanceM, body.Matches[0].EndDistanceM,
				body.Matches[1].StartDistanceM, body.Matches[1].EndDistanceM,
			})
	}
}

func TestMatchingCreate_RejectsMissingCoords(t *testing.T) {
	_ = ConnectTestDB(t)
	ResetDB(t)
	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.97&start_lon=79.15",
		nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 missing end coords, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}
