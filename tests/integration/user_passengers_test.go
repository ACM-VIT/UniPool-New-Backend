package integration

import (
	"net/http"
	"testing"
	"time"

	"unipool-backend/models"
)

func TestGetPassengers_ReturnsCompletedAcceptedCoRiders(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "passengers-host@vitstudent.ac.in", &inst.ID)
	passengerOne := SeedUser(t, db, "Passenger One", "passenger-one@vitstudent.ac.in", &inst.ID)
	passengerTwo := SeedUser(t, db, "Passenger Two", "passenger-two@vitstudent.ac.in", &inst.ID)
	pendingPassenger := SeedUser(t, db, "Pending Passenger", "pending-passenger@vitstudent.ac.in", &inst.ID)

	ride := SeedRide(t, db, host, RideOpts{
		StartTime:   time.Now().Add(-2 * time.Hour).UTC(),
		BookedSeats: 2,
	})
	for _, passenger := range []models.User{passengerOne, passengerTwo} {
		if err := db.Create(&models.Booking{
			RideID:        ride.ID,
			PassengerID:   passenger.ID,
			RequestStatus: "accepted",
		}).Error; err != nil {
			t.Fatalf("seed accepted booking: %v", err)
		}
	}
	if err := db.Create(&models.Booking{
		RideID:        ride.ID,
		PassengerID:   pendingPassenger.ID,
		RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed pending booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/passengers", nil, AsUser(passengerOne.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("passenger view: expected 200, got %d", resp.StatusCode)
	}
	var passengerView []struct {
		ID string `json:"id"`
	}
	ReadJSON(t, resp, &passengerView)
	passengerIDs := idsFromPassengerRows(passengerView)
	if !passengerIDs[host.ID.String()] {
		t.Fatalf("passenger view missing host")
	}
	if !passengerIDs[passengerTwo.ID.String()] {
		t.Fatalf("passenger view missing co-passenger")
	}
	if passengerIDs[passengerOne.ID.String()] {
		t.Fatalf("passenger view included self")
	}
	if passengerIDs[pendingPassenger.ID.String()] {
		t.Fatalf("passenger view included pending passenger")
	}

	resp = Do(t, app, http.MethodGet, "/user/passengers", nil, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host view: expected 200, got %d", resp.StatusCode)
	}
	var hostView []struct {
		ID string `json:"id"`
	}
	ReadJSON(t, resp, &hostView)
	hostIDs := idsFromPassengerRows(hostView)
	if !hostIDs[passengerOne.ID.String()] || !hostIDs[passengerTwo.ID.String()] {
		t.Fatalf("host view missing accepted passengers: %#v", hostIDs)
	}
	if hostIDs[pendingPassenger.ID.String()] {
		t.Fatalf("host view included pending passenger")
	}
}

func idsFromPassengerRows(rows []struct {
	ID string `json:"id"`
}) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.ID] = true
	}
	return out
}
