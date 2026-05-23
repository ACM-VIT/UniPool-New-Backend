package main

// Regression tests for the booking lifecycle: request -> accept/reject.
// The lifecycle spans three endpoints (request, accept, reject) and
// two distinct user roles, so single-handler unit tests would miss
// the cross-handler invariants (only host can accept, double-request
// blocked, status transitions consistent).

import (
	"net/http"
	"testing"

	"unipool-backend/models"
)

func TestBooking_RequestCreatesPending(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "book-host@vitstudent.ac.in", &inst.ID)
	passenger := seedUser(t, db, "Passenger", "book-pax@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})

	app := setupTestApp(t)
	resp := do(t, app, http.MethodPost, "/bookings/request",
		map[string]any{"ride_id": ride.ID, "request_status": "pending"},
		asUser(passenger.Email))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var body struct {
		ID            string `json:"id"`
		RideID        string `json:"ride_id"`
		RequestStatus string `json:"request_status"`
	}
	readJSON(t, resp, &body)
	if body.RequestStatus != "pending" {
		t.Errorf("expected pending, got %q", body.RequestStatus)
	}
	if body.RideID != ride.ID.String() {
		t.Errorf("ride_id mismatch: got %s want %s", body.RideID, ride.ID)
	}

	// Verify the row landed with the right passenger attribution.
	var saved models.Booking
	if err := db.Where("ride_id = ? AND passenger_id = ?", ride.ID, passenger.ID).First(&saved).Error; err != nil {
		t.Fatalf("booking not persisted: %v", err)
	}
	if saved.RequestStatus != "pending" {
		t.Errorf("DB row status %q, want pending", saved.RequestStatus)
	}
}

func TestBooking_DuplicateRequestRejected(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "book-host2@vitstudent.ac.in", &inst.ID)
	passenger := seedUser(t, db, "Passenger", "book-pax2@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})

	app := setupTestApp(t)
	first := do(t, app, http.MethodPost, "/bookings/request",
		map[string]any{"ride_id": ride.ID, "request_status": "pending"},
		asUser(passenger.Email))
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d", first.StatusCode)
	}
	readJSON(t, first, nil)

	dup := do(t, app, http.MethodPost, "/bookings/request",
		map[string]any{"ride_id": ride.ID, "request_status": "pending"},
		asUser(passenger.Email))
	if dup.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate: expected 400, got %d", dup.StatusCode)
	}

	var count int64
	db.Model(&models.Booking{}).Where("ride_id = ? AND passenger_id = ?", ride.ID, passenger.ID).Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 booking row, got %d", count)
	}
}

func TestBooking_HostCanAccept(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "accept-host@vitstudent.ac.in", &inst.ID)
	passenger := seedUser(t, db, "Passenger", "accept-pax@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "pending"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := setupTestApp(t)
	resp := do(t, app, http.MethodPut, "/bookings/accept/"+booking.ID.String(), nil, asUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)

	var refreshed models.Booking
	if err := db.First(&refreshed, booking.ID).Error; err != nil {
		t.Fatalf("reload booking: %v", err)
	}
	if refreshed.RequestStatus != "accepted" {
		t.Errorf("status: got %q want accepted", refreshed.RequestStatus)
	}

	// Booked seats should reflect the new accepted booking.
	var afterRide models.Ride
	if err := db.First(&afterRide, ride.ID).Error; err != nil {
		t.Fatalf("reload ride: %v", err)
	}
	if afterRide.BookedSeats < 1 {
		t.Errorf("booked_seats should have incremented; got %d", afterRide.BookedSeats)
	}
}

func TestBooking_NonHostCannotAccept(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "naccept-host@vitstudent.ac.in", &inst.ID)
	passenger := seedUser(t, db, "Passenger", "naccept-pax@vitstudent.ac.in", &inst.ID)
	intruder := seedUser(t, db, "Intruder", "naccept-intr@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "pending"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := setupTestApp(t)
	// Passenger tries to self-accept.
	resp := do(t, app, http.MethodPut, "/bookings/accept/"+booking.ID.String(), nil, asUser(passenger.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("passenger self-accept: expected 403, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)

	// A third user tries.
	resp = do(t, app, http.MethodPut, "/bookings/accept/"+booking.ID.String(), nil, asUser(intruder.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("intruder accept: expected 403, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)

	var refreshed models.Booking
	db.First(&refreshed, booking.ID)
	if refreshed.RequestStatus != "pending" {
		t.Errorf("status drifted under unauthorized accepts: got %q", refreshed.RequestStatus)
	}
}

func TestBooking_HostCanReject(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "rej-host@vitstudent.ac.in", &inst.ID)
	passenger := seedUser(t, db, "Passenger", "rej-pax@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "pending"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := setupTestApp(t)
	resp := do(t, app, http.MethodPut, "/bookings/reject/"+booking.ID.String(), nil, asUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)
	var refreshed models.Booking
	if err := db.First(&refreshed, booking.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if refreshed.RequestStatus != "rejected" {
		t.Errorf("expected rejected, got %q", refreshed.RequestStatus)
	}
}
