package integration

import (
	"net/http"
	"testing"
	"time"

	"unipool-backend/models"
)

func TestUserRides_UpcomingScopeReturnsHostedAndBookedOnly(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	viewer := SeedUser(t, db, "Viewer", "ur-scope-viewer@vitstudent.ac.in", &inst.ID)
	host := SeedUser(t, db, "Host", "ur-scope-host@vitstudent.ac.in", &inst.ID)
	other := SeedUser(t, db, "Other", "ur-scope-other@vitstudent.ac.in", &inst.ID)

	hosted := SeedRide(t, db, viewer, RideOpts{
		StartLocation: "Hosted A",
		EndLocation:   "Hosted B",
		StartTime:     time.Now().Add(4 * time.Hour).UTC(),
	})
	booked := SeedRide(t, db, host, RideOpts{
		StartLocation: "Booked A",
		EndLocation:   "Booked B",
		StartTime:     time.Now().Add(3 * time.Hour).UTC(),
	})
	past := SeedRide(t, db, host, RideOpts{
		StartLocation: "Past A",
		EndLocation:   "Past B",
		StartTime:     time.Now().Add(-48 * time.Hour).UTC(),
	})
	unrelated := SeedRide(t, db, other, RideOpts{
		StartLocation: "Unrelated A",
		EndLocation:   "Unrelated B",
		StartTime:     time.Now().Add(2 * time.Hour).UTC(),
	})
	if err := db.Create(&models.Booking{
		RideID:        booked.ID,
		PassengerID:   viewer.ID,
		RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed booked booking: %v", err)
	}
	if err := db.Create(&models.Booking{
		RideID:        past.ID,
		PassengerID:   viewer.ID,
		RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed past booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/user/rides?scope=upcoming", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body []struct {
		RideID        string `json:"ride_id"`
		HostUserName  string `json:"host_user_name"`
		IsUserHost    bool   `json:"is_user_host"`
		RequestStatus string `json:"request_status"`
		ViewerState   string `json:"viewer_state"`
	}
	ReadJSON(t, resp, &body)
	if len(body) != 2 {
		t.Fatalf("expected hosted + booked upcoming rides only, got %d: %+v", len(body), body)
	}

	seen := map[string]struct{}{}
	for _, ride := range body {
		seen[ride.RideID] = struct{}{}
		switch ride.RideID {
		case hosted.ID.String():
			if !ride.IsUserHost || ride.ViewerState != "host" {
				t.Fatalf("hosted ride state lost: %+v", ride)
			}
		case booked.ID.String():
			if ride.HostUserName != host.Name || ride.RequestStatus != "pending" || ride.ViewerState != "pending_passenger" {
				t.Fatalf("booked ride projection/state lost: %+v", ride)
			}
		default:
			t.Fatalf("unexpected ride in upcoming scope: %+v", ride)
		}
	}
	if _, ok := seen[unrelated.ID.String()]; ok {
		t.Fatalf("unrelated ride leaked into /user/rides")
	}
	if _, ok := seen[past.ID.String()]; ok {
		t.Fatalf("past ride leaked into upcoming scope")
	}
}
