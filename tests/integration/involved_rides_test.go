package integration

import (
	"net/http"
	"testing"
	"time"

	"unipool-backend/models"
)

func TestInvolvedRides_ReturnsHostedAndBookedWithHostProjection(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	viewer := SeedUser(t, db, "Viewer", "involved-viewer@vitstudent.ac.in", &inst.ID)
	host := SeedUser(t, db, "Host", "involved-host@vitstudent.ac.in", &inst.ID)
	other := SeedUser(t, db, "Other", "involved-other@vitstudent.ac.in", &inst.ID)

	hosted := SeedRide(t, db, viewer, RideOpts{
		StartLocation: "Hosted A",
		EndLocation:   "Hosted B",
		StartTime:     time.Now().Add(3 * time.Hour).UTC(),
	})
	booked := SeedRide(t, db, host, RideOpts{
		StartLocation: "Booked A",
		EndLocation:   "Booked B",
		StartTime:     time.Now().Add(2 * time.Hour).UTC(),
	})
	unrelated := SeedRide(t, db, other, RideOpts{
		StartLocation: "Unrelated A",
		EndLocation:   "Unrelated B",
		StartTime:     time.Now().Add(time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID:        booked.ID,
		PassengerID:   viewer.ID,
		RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/rides/involved", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Count int `json:"count"`
		Rides []struct {
			ID            string `json:"id"`
			HostUserID    string `json:"host_user_id"`
			StartLocation string `json:"start_location"`
			HostUser      struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"host_user"`
		} `json:"rides"`
	}
	ReadJSON(t, resp, &body)
	if body.Count != 2 || len(body.Rides) != 2 {
		t.Fatalf("expected 2 involved rides, got count=%d len=%d", body.Count, len(body.Rides))
	}

	seen := map[string]struct{}{}
	for _, ride := range body.Rides {
		seen[ride.ID] = struct{}{}
		if ride.ID == booked.ID.String() && ride.HostUser.Name != host.Name {
			t.Fatalf("booked ride host projection lost: got %q want %q", ride.HostUser.Name, host.Name)
		}
		if ride.ID == hosted.ID.String() && ride.HostUser.Name != viewer.Name {
			t.Fatalf("hosted ride host projection lost: got %q want %q", ride.HostUser.Name, viewer.Name)
		}
	}
	if _, ok := seen[hosted.ID.String()]; !ok {
		t.Fatalf("hosted ride missing from involved rides")
	}
	if _, ok := seen[booked.ID.String()]; !ok {
		t.Fatalf("booked ride missing from involved rides")
	}
	if _, ok := seen[unrelated.ID.String()]; ok {
		t.Fatalf("unrelated ride leaked into involved rides")
	}
}
