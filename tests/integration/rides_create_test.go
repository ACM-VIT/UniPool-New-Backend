package integration

// Regression tests for POST /ride/create. Catches a few classes of bug:
//   - Auth gate enforcement (no session = 401)
//   - HostUserID injection (the handler must set this from c.Locals,
//     never trust the request body)
//   - Past-start-time rejection
//   - DB column type compatibility (Ride struct uses decimal(10,8)
//     and decimal(11,8) for lat/lng — a future change to numeric or
//     float would silently break the schema, this catches the
//     round-trip)

import (
	"net/http"
	"testing"
	"time"

	"unipool-backend/models"

	"github.com/google/uuid"
)

func TestCreateRide_AuthRequired(t *testing.T) {
	_ = ConnectTestDB(t)
	ResetDB(t)
	app := SetupTestApp(t)

	body := map[string]any{
		"start_location": "VIT Main Gate",
		"end_location":   "Katpadi Junction",
		"start_time":     time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"total_seats":    3,
		"booked_seats":   0,
		"total_price":    150,
	}
	resp := Do(t, app, http.MethodPost, "/ride/create", body, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestCreateRide_PersistsAndAttributesHost(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Driver", "driver@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	body := map[string]any{
		"start_location":  "VIT Main Gate",
		"end_location":    "Katpadi Junction",
		"start_latitude":  12.9692,
		"start_longitude": 79.1559,
		"end_latitude":    12.9698,
		"end_longitude":   79.1370,
		"start_time":      time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"total_seats":     3,
		"booked_seats":    0,
		"total_price":     150,
	}
	resp := Do(t, app, http.MethodPost, "/ride/create", body, AsUser(host.Email))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200/201, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var saved models.Ride
	if err := db.Where("host_user_id = ?", host.ID).First(&saved).Error; err != nil {
		t.Fatalf("ride not persisted: %v", err)
	}
	if saved.HostUserID != host.ID {
		t.Errorf("host attribution wrong: got %s want %s", saved.HostUserID, host.ID)
	}
	if saved.TotalSeats != 3 {
		t.Errorf("total_seats lost in round trip: got %d", saved.TotalSeats)
	}
	if saved.StartLatitude == nil || saved.StartLongitude == nil ||
		saved.EndLatitude == nil || saved.EndLongitude == nil {
		t.Fatalf("coordinates lost in round trip")
	}
	if *saved.StartLatitude < 12.96 || *saved.StartLatitude > 12.98 {
		t.Errorf("start_latitude column drift: got %v", *saved.StartLatitude)
	}
}

// TestCreateRide_RejectsPastStart guards a tiny but high-impact check:
// the handler refuses to schedule a ride in the past. A regression here
// would silently let users post ghost trips that the search UI then
// surfaces alongside real ones.
func TestCreateRide_RejectsPastStart(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Driver", "driver-past@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	body := map[string]any{
		"start_location": "VIT",
		"end_location":   "Katpadi",
		"start_time":     time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		"total_seats":    2,
		"booked_seats":   0,
		"total_price":    50,
	}
	resp := Do(t, app, http.MethodPost, "/ride/create", body, AsUser(host.Email))
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("expected 4xx for past start time, got %d", resp.StatusCode)
	}
	var count int64
	db.Model(&models.Ride{}).Where("host_user_id = ?", host.ID).Count(&count)
	if count != 0 {
		t.Errorf("ride persisted despite past start_time (count=%d)", count)
	}
}

// TestCreateRide_IgnoresClientHostID is the security regression — even
// if a caller stuffs someone else's host_user_id into the body, the
// server must overwrite it with the authenticated user's ID.
func TestCreateRide_IgnoresClientHostID(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Driver", "driver-spoof@vitstudent.ac.in", &inst.ID)
	other := SeedUser(t, db, "Other", "other-spoof@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	body := map[string]any{
		"host_user_id":   other.ID, // attempt to impersonate
		"start_location": "VIT",
		"end_location":   "Katpadi",
		"start_time":     time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"total_seats":    3,
		"booked_seats":   0,
		"total_price":    100,
	}
	resp := Do(t, app, http.MethodPost, "/ride/create", body, AsUser(host.Email))
	if resp.StatusCode >= 400 {
		t.Fatalf("expected ride creation to succeed for authed user, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var saved []models.Ride
	if err := db.Where("start_location = ?", "VIT").Find(&saved).Error; err != nil {
		t.Fatalf("listing rides: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected exactly 1 ride, got %d", len(saved))
	}
	if saved[0].HostUserID != host.ID {
		t.Errorf("host injection failed: ride attributed to %s (expected %s, attacker tried %s)",
			saved[0].HostUserID, host.ID, other.ID)
	}
}

// Quick guard to ensure uuid.Nil doesn't slip through if the handler
// path ever stops setting HostUserID; surfaces as a 500 today, but the
// assertion here is direct.
func TestCreateRide_HostUserIDIsNotNil(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Driver", "driver-nil@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	body := map[string]any{
		"start_location": "VIT",
		"end_location":   "Katpadi",
		"start_time":     time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339),
		"total_seats":    4,
		"booked_seats":   0,
		"total_price":    80,
	}
	resp := Do(t, app, http.MethodPost, "/ride/create", body, AsUser(host.Email))
	if resp.StatusCode >= 400 {
		t.Fatalf("expected success, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
	var saved models.Ride
	if err := db.Where("host_user_id = ?", host.ID).First(&saved).Error; err != nil {
		t.Fatalf("ride not found by host: %v", err)
	}
	if saved.HostUserID == (uuid.UUID{}) {
		t.Fatalf("host_user_id is the zero UUID")
	}
}
