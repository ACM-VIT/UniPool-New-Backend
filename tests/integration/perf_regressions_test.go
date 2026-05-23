package integration

// Regression tests for the perf refactors landed in 89771c9.
//
// These specifically guard the *behavioral* surface of three helpers
// that were rewritten to collapse round-trips:
//
//   - BuildActiveTripCard          (single bucketed JOIN, was 6 round-trips)
//   - BuildPendingRatings          (two batched queries, was 2N+1)
//   - GetRideDetailsComplete       (host LEFT JOIN institute, was 4 sequential)
//
// The risk after each refactor is subtle: SQL bucketing logic that
// picks the wrong row, GROUP BY that loses zero-rated rides, LEFT JOIN
// that NULL-explodes for hosts without an institute. These tests pin
// the contract end-to-end so a future "small tweak" can't quietly
// regress what the refactor preserved.

import (
	"net/http"
	"testing"
	"time"

	"unipool-backend/models"
)

// --- /trip-card/active -----------------------------------------------

func TestActiveTripCard_PrefersUpcomingBucket(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "tc-host@vitstudent.ac.in", &inst.ID)
	viewer := SeedUser(t, db, "Viewer", "tc-viewer@vitstudent.ac.in", &inst.ID)

	// One ride 2 days ago (in the "recent" bucket), one 2 hours away
	// (in the "upcoming" bucket). Both have an accepted booking for
	// the viewer. The new bucketed query MUST pick upcoming first.
	pastRide := SeedRide(t, db, host, RideOpts{
		StartLocation: "A", EndLocation: "B",
		StartTime: time.Now().Add(-48 * time.Hour).UTC(),
	})
	soonRide := SeedRide(t, db, host, RideOpts{
		StartLocation: "C", EndLocation: "D",
		StartTime: time.Now().Add(2 * time.Hour).UTC(),
	})
	for _, r := range []models.Ride{pastRide, soonRide} {
		if err := db.Create(&models.Booking{
			RideID: r.ID, PassengerID: viewer.ID, RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/trip-card/active", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		TripCard struct {
			RideID string `json:"ride_id"`
			Stage  string `json:"stage"`
		} `json:"trip_card"`
	}
	ReadJSON(t, resp, &body)
	if body.TripCard.RideID != soonRide.ID.String() {
		t.Errorf("upcoming bucket should win: got ride %s, want %s", body.TripCard.RideID, soonRide.ID)
	}
	if body.TripCard.Stage != "upcoming" {
		t.Errorf("stage: got %q want upcoming", body.TripCard.Stage)
	}
}

func TestActiveTripCard_FallsBackToRecentUndismissed(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "tc-host2@vitstudent.ac.in", &inst.ID)
	viewer := SeedUser(t, db, "Viewer", "tc-viewer2@vitstudent.ac.in", &inst.ID)

	// One ride 6 hours ago (in recent bucket, undismissed). NO upcoming
	// rides at all — recent bucket should win.
	r := SeedRide(t, db, host, RideOpts{
		StartLocation: "X", EndLocation: "Y",
		StartTime: time.Now().Add(-6 * time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: viewer.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/trip-card/active", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		TripCard struct {
			RideID string `json:"ride_id"`
			Stage  string `json:"stage"`
		} `json:"trip_card"`
	}
	ReadJSON(t, resp, &body)
	if body.TripCard.RideID != r.ID.String() {
		t.Errorf("recent bucket: got %s want %s", body.TripCard.RideID, r.ID)
	}
	// Within 24h => in_window (between -24h and now).
	if body.TripCard.Stage != "in_window" {
		t.Errorf("stage: got %q want in_window", body.TripCard.Stage)
	}
}

func TestActiveTripCard_RecentBucketSkipsDismissed(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "tc-host3@vitstudent.ac.in", &inst.ID)
	viewer := SeedUser(t, db, "Viewer", "tc-viewer3@vitstudent.ac.in", &inst.ID)

	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-6 * time.Hour).UTC(),
	})
	now := time.Now()
	if err := db.Create(&models.Booking{
		RideID:          r.ID,
		PassengerID:     viewer.ID,
		RequestStatus:   "accepted",
		DismissedAt:     &now, // already dismissed
		DismissalSignal: "paid",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/trip-card/active", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusNoContent {
		// readJSON to drain body if 200 was returned
		raw := ReadJSON(t, resp, nil)
		t.Errorf("expected 204 (dismissed -> no card), got %d body=%s", resp.StatusCode, string(raw))
	}
}

func TestActiveTripCard_HostProfileFieldsIncluded(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host Bob", "tc-host4@vitstudent.ac.in", &inst.ID)
	db.Model(&models.User{}).Where("id = ?", host.ID).Updates(map[string]any{
		"profile_picture_url": "https://example.com/host.png",
		"upi_vpa":             "host@upi",
	})
	viewer := SeedUser(t, db, "Viewer", "tc-viewer4@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{
		StartTime:  time.Now().Add(2 * time.Hour).UTC(),
		TotalPrice: 250,
	})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: viewer.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/trip-card/active", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		TripCard struct {
			HostName          string `json:"host_name"`
			HostProfilePicURL string `json:"host_profile_picture_url"`
			HostUPIVPA        string `json:"host_upi_vpa"`
			TotalPrice        int    `json:"total_price"`
		} `json:"trip_card"`
	}
	ReadJSON(t, resp, &body)
	if body.TripCard.HostName != "Host Bob" {
		t.Errorf("host_name: got %q", body.TripCard.HostName)
	}
	if body.TripCard.HostProfilePicURL != "https://example.com/host.png" {
		t.Errorf("host_profile_picture_url lost in JOIN: got %q", body.TripCard.HostProfilePicURL)
	}
	if body.TripCard.HostUPIVPA != "host@upi" {
		t.Errorf("host_upi_vpa lost in JOIN: got %q", body.TripCard.HostUPIVPA)
	}
	if body.TripCard.TotalPrice != 250 {
		t.Errorf("total_price lost: got %d", body.TripCard.TotalPrice)
	}
}

// --- /user/pending-ratings -------------------------------------------

func TestPendingRatings_HostWithMultiplePassengers(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pr-host@vitstudent.ac.in", &inst.ID)
	p1 := SeedUser(t, db, "P1", "pr-p1@vitstudent.ac.in", &inst.ID)
	p2 := SeedUser(t, db, "P2", "pr-p2@vitstudent.ac.in", &inst.ID)
	p3 := SeedUser(t, db, "P3", "pr-p3@vitstudent.ac.in", &inst.ID)

	// Ride started 14 hours ago — past the 12h "rating opens" threshold
	// but well inside the 30d window.
	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-14 * time.Hour).UTC(),
	})
	for _, p := range []models.User{p1, p2, p3} {
		if err := db.Create(&models.Booking{
			RideID: r.ID, PassengerID: p.ID, RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/pending-ratings", nil, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []struct {
			RideID       string `json:"ride_id"`
			PendingCount int    `json:"pending_count"`
		} `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) != 1 {
		t.Fatalf("expected 1 ride, got %d", len(body.Rides))
	}
	if body.Rides[0].PendingCount != 3 {
		t.Errorf("pending_count: got %d want 3 (passengers p1, p2, p3 all unrated)", body.Rides[0].PendingCount)
	}
}

func TestPendingRatings_DropsToZeroWhenAllRated(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pr-host2@vitstudent.ac.in", &inst.ID)
	p1 := SeedUser(t, db, "P1", "pr-p4@vitstudent.ac.in", &inst.ID)
	p2 := SeedUser(t, db, "P2", "pr-p5@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-15 * time.Hour).UTC(), // past 12h opens-threshold
	})
	for _, p := range []models.User{p1, p2} {
		if err := db.Create(&models.Booking{
			RideID: r.ID, PassengerID: p.ID, RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}
	// Host has already rated both passengers — pending should be 0
	// and the ride should be filtered out entirely.
	for _, p := range []models.User{p1, p2} {
		if err := db.Create(&models.RideRating{
			RideID:      r.ID,
			RaterUserID: host.ID,
			RatedUserID: p.ID,
			Stars:       5,
		}).Error; err != nil {
			t.Fatalf("seed rating: %v", err)
		}
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/pending-ratings", nil, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) != 0 {
		t.Errorf("expected empty rides (all rated), got %d", len(body.Rides))
	}
}

func TestPendingRatings_PassengerOnlyHostCounterpart(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pr-host3@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "pr-pax@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-14 * time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: pax.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/pending-ratings", nil, AsUser(pax.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []struct {
			PendingCount int `json:"pending_count"`
		} `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) != 1 || body.Rides[0].PendingCount != 1 {
		t.Errorf("passenger should see 1 pending (rate host); got %d rides, count=%v",
			len(body.Rides), body.Rides)
	}
}

func TestPendingRatings_DropsAfterPromptWindow(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pr-old-host@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "pr-old-pax@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-8 * 24 * time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: pax.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/pending-ratings", nil, AsUser(pax.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Rides []map[string]any `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Rides) != 0 {
		t.Fatalf("expected no stale rating prompts after 7 days, got %d", len(body.Rides))
	}
}

func TestUserRides_PastRideCarriesCanRateAction(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "ur-rate-host@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "ur-rate-pax@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-30 * time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: pax.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/rides?scope=past", nil, AsUser(pax.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body []struct {
		RideID  string `json:"ride_id"`
		Actions struct {
			CanRate bool `json:"can_rate"`
		} `json:"actions"`
	}
	ReadJSON(t, resp, &body)
	if len(body) != 1 || body[0].RideID != r.ID.String() {
		t.Fatalf("unexpected /user/rides payload: %+v", body)
	}
	if !body[0].Actions.CanRate {
		t.Fatalf("expected /user/rides actions.can_rate for unrated past trip")
	}
}

// --- /ride/details/:id -----------------------------------------------

func TestRideDetails_PastRideCarriesCanRateAction(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "rd-rate-host@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "rd-rate-pax@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{
		StartTime: time.Now().Add(-30 * time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: pax.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/ride/details/"+r.ID.String(), nil, AsUser(pax.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ViewerState string `json:"viewer_state"`
		Actions     struct {
			CanRate bool `json:"can_rate"`
		} `json:"actions"`
	}
	ReadJSON(t, resp, &body)
	if body.ViewerState != "past" {
		t.Fatalf("viewer_state: got %q want past", body.ViewerState)
	}
	if !body.Actions.CanRate {
		t.Fatalf("expected actions.can_rate for unrated past trip")
	}
}

func TestRideDetails_HostWithInstitute(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT Vellore", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "rd-host@vitstudent.ac.in", &inst.ID)
	viewer := SeedUser(t, db, "Viewer", "rd-viewer@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/ride/details/"+r.ID.String(), nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// HostInstituteName is emitted at the TOP LEVEL, not nested under
	// host. Same with host_same_institute_as_viewer.
	var body struct {
		Host struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"host"`
		HostInstituteName         *string `json:"host_institute_name"`
		HostSameInstituteAsViewer bool    `json:"host_same_institute_as_viewer"`
	}
	ReadJSON(t, resp, &body)
	if body.Host.ID != host.ID.String() {
		t.Errorf("host.id: got %s want %s", body.Host.ID, host.ID)
	}
	if body.HostInstituteName == nil || *body.HostInstituteName != "VIT Vellore" {
		t.Errorf("host_institute_name lost in LEFT JOIN: got %v", body.HostInstituteName)
	}
	if !body.HostSameInstituteAsViewer {
		t.Errorf("host_same_institute_as_viewer: expected true (both at VIT)")
	}
}

func TestRideDetails_HostWithoutInstituteIsHandled(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	// Host has no InstituteID — verify the LEFT JOIN returns NULL
	// institute_name without exploding the row.
	host := SeedUser(t, db, "Indie Host", "rd-indie@gmail.com", nil)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	viewer := SeedUser(t, db, "Viewer", "rd-viewer2@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/ride/details/"+r.ID.String(), nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
		HostInstituteName         *string `json:"host_institute_name"`
		HostSameInstituteAsViewer bool    `json:"host_same_institute_as_viewer"`
	}
	ReadJSON(t, resp, &body)
	if body.Host.ID != host.ID.String() {
		t.Errorf("host.id: got %s want %s", body.Host.ID, host.ID)
	}
	// host_institute_name omitempty + nil -> absent in JSON -> nil
	// after unmarshal. That's the expected shape for an unverified
	// host with no institute_id.
	if body.HostInstituteName != nil {
		t.Errorf("expected nil host_institute_name for unverified host; got %q", *body.HostInstituteName)
	}
	if body.HostSameInstituteAsViewer {
		t.Errorf("host_same_institute_as_viewer: expected false (host has no institute)")
	}
}

func TestRideDetails_IncludesBookingsAndPassengers(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "rd-host3@vitstudent.ac.in", &inst.ID)
	p1 := SeedUser(t, db, "Pax One", "rd-p1@vitstudent.ac.in", &inst.ID)
	p2 := SeedUser(t, db, "Pax Two", "rd-p2@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{})
	for _, p := range []models.User{p1, p2} {
		if err := db.Create(&models.Booking{
			RideID: r.ID, PassengerID: p.ID, RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/ride/details/"+r.ID.String(), nil, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Bookings []struct {
			PassengerID   string `json:"passenger_id"`
			PassengerName string `json:"passenger_name"`
			RequestStatus string `json:"request_status"`
		} `json:"bookings"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Bookings) != 2 {
		t.Fatalf("expected 2 bookings, got %d", len(body.Bookings))
	}
	gotNames := map[string]bool{}
	for _, b := range body.Bookings {
		gotNames[b.PassengerName] = true
		if b.RequestStatus != "accepted" {
			t.Errorf("status: got %q", b.RequestStatus)
		}
	}
	for _, want := range []string{"Pax One", "Pax Two"} {
		if !gotNames[want] {
			t.Errorf("missing passenger %q from bookings", want)
		}
	}
}

func TestRideDetails_NonHostOnlySeesOwnBooking(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "rd-privacy-host@vitstudent.ac.in", &inst.ID)
	p1 := SeedUser(t, db, "Pax One", "rd-privacy-p1@vitstudent.ac.in", &inst.ID)
	p2 := SeedUser(t, db, "Pax Two", "rd-privacy-p2@vitstudent.ac.in", &inst.ID)
	r := SeedRide(t, db, host, RideOpts{})
	for _, p := range []models.User{p1, p2} {
		if err := db.Create(&models.Booking{
			RideID: r.ID, PassengerID: p.ID, RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/ride/details/"+r.ID.String(), nil, AsUser(p1.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Bookings []struct {
			PassengerID string `json:"passenger_id"`
		} `json:"bookings"`
	}
	ReadJSON(t, resp, &body)
	if len(body.Bookings) != 1 {
		t.Fatalf("expected only viewer's booking, got %d", len(body.Bookings))
	}
	if body.Bookings[0].PassengerID != p1.ID.String() {
		t.Fatalf("expected p1 booking, got %s", body.Bookings[0].PassengerID)
	}
}

func TestRideDetails_UsesPassengerCapacityForFullState(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "rd-full-host@vitstudent.ac.in", &inst.ID)
	taker := SeedUser(t, db, "Taker", "rd-full-taker@vitstudent.ac.in", &inst.ID)
	viewer := SeedUser(t, db, "Viewer", "rd-full-viewer@vitstudent.ac.in", &inst.ID)
	// total_seats includes host. total=2 with booked=1 means the only
	// passenger slot is already taken, so unrelated viewers must see
	// viewer_state=full.
	r := SeedRide(t, db, host, RideOpts{TotalSeats: 2, BookedSeats: 0})
	if err := db.Create(&models.Booking{
		RideID: r.ID, PassengerID: taker.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/ride/details/"+r.ID.String(), nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ViewerState string `json:"viewer_state"`
		Actions     struct {
			CanRequestSeat bool `json:"can_request_seat"`
		} `json:"actions"`
	}
	ReadJSON(t, resp, &body)
	if body.ViewerState != "full" {
		t.Fatalf("viewer_state: got %q want full", body.ViewerState)
	}
	if body.Actions.CanRequestSeat {
		t.Fatalf("can_request_seat should be false for a full ride")
	}
}
