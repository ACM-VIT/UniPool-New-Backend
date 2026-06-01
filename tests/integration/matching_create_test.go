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

	"unipool-backend/models"
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
	// Inside-window ride at +2h from "now" — comfortably under
	// the 3h default window (avoids float-rounded boundary cases).
	inWindow := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: now.Add(2 * time.Hour),
	})
	// Outside-window ride at +8h. Must NOT match the default 3h.
	_ = SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime: now.Add(8 * time.Hour),
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
	p1 := SeedUser(t, db, "P1", "full-p1@vitstudent.ac.in", &inst.ID)
	p2 := SeedUser(t, db, "P2", "full-p2@vitstudent.ac.in", &inst.ID)
	p3 := SeedUser(t, db, "P3", "full-p3@vitstudent.ac.in", &inst.ID)

	startT := time.Now().Add(2 * time.Hour).UTC()
	// 3-seat ride. We don't set BookedSeats — the matching query
	// no longer trusts that cached counter; it counts real
	// accepted bookings. Create three actual accepted bookings
	// so the live count fills the ride.
	full := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime:  startT,
		TotalSeats: 3,
	})
	for _, p := range []models.User{p1, p2, p3} {
		if err := db.Create(&models.Booking{
			RideID: full.ID, PassengerID: p.ID, RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}

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
		t.Errorf("expected 0 (full ride filtered by live accepted-count), got %d", body.Count)
	}
}

// TestMatchingCreate_IgnoresStaleBookedSeatsCounter explicitly
// guards the booked_seats-counter-drift bug we just fixed. A ride
// whose cached counter says "full" but has zero actual accepted
// bookings should STILL match — because the real-seats subquery
// is the source of truth.
func TestMatchingCreate_IgnoresStaleBookedSeatsCounter(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "drift-host@vitstudent.ac.in", &inst.ID)

	startT := time.Now().Add(2 * time.Hour).UTC()
	// Cached counter says full (3 / 3), but live accepted bookings are 0.
	target := SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
		EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
		StartTime:   startT,
		TotalSeats:  3,
		BookedSeats: 3, // intentionally drifted
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
			ID string `json:"id"`
		} `json:"matches"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Matches) != 1 || body.Matches[0].ID != target.ID.String() {
		t.Errorf("expected the drift-counter ride to match (live count = 0), got %v", body.Matches)
	}
}

// TestMatchingCreate_ExcludesRidesUserAlreadyBooked locks in the
// "don't nag the user with a ride they've already touched"
// safeguard. Whether they were accepted, are pending, or got
// rejected — they've seen this ride and shouldn't see it again
// from the create-ride suggestion surface.
func TestMatchingCreate_ExcludesRidesUserAlreadyBooked(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "dup-host@vitstudent.ac.in", &inst.ID)
	me := SeedUser(t, db, "Me", "dup-me@vitstudent.ac.in", &inst.ID)

	startT := time.Now().Add(2 * time.Hour).UTC()
	for _, status := range []string{"accepted", "pending", "rejected"} {
		r := SeedRide(t, db, host, RideOpts{
			StartLat: FloatPtr(12.9692), StartLon: FloatPtr(79.1559),
			EndLat: FloatPtr(12.9698), EndLon: FloatPtr(79.1370),
			StartTime: startT,
		})
		if err := db.Create(&models.Booking{
			RideID: r.ID, PassengerID: me.ID, RequestStatus: status,
		}).Error; err != nil {
			t.Fatalf("seed %s booking: %v", status, err)
		}
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet,
		"/ride/matching-create?start_lat=12.9692&start_lon=79.1559&end_lat=12.9698&end_lon=79.1370&start_time="+startT.Format(time.RFC3339),
		nil, AsUser(me.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Count int `json:"count"`
	}
	ReadJSON(t, resp, &body)
	if body.Count != 0 {
		t.Errorf("expected 0 (rides with my existing bookings filtered out), got %d", body.Count)
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
	// Farther match (~300m on each end, still inside default 500m).
	// Pre-tightening this was at ~870m for the 1km default; the
	// new 500m default makes that drift out of the radius.
	_ = SeedRide(t, db, host, RideOpts{
		StartLat: FloatPtr(12.9720), StartLon: FloatPtr(79.1585),
		EndLat: FloatPtr(12.9722), EndLon: FloatPtr(79.1347),
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
