package integration

import (
	"net/http"
	"testing"
	"time"
)

func TestRideCRUD_FetchAndAllCarryHostName(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Ride Host", "ride-crud-host@vitstudent.ac.in", &inst.ID)
	viewer := SeedUser(t, db, "Viewer", "ride-crud-viewer@vitstudent.ac.in", &inst.ID)
	first := SeedRide(t, db, host, RideOpts{
		StartLocation: "A",
		EndLocation:   "B",
		StartTime:     time.Now().Add(3 * time.Hour).UTC(),
	})
	second := SeedRide(t, db, host, RideOpts{
		StartLocation: "C",
		EndLocation:   "D",
		StartTime:     time.Now().Add(4 * time.Hour).UTC(),
	})

	app := SetupTestApp(t)
	fetchResp := Do(t, app, http.MethodGet, "/ride/fetch/"+first.ID.String(), nil, AsUser(viewer.Email))
	if fetchResp.StatusCode != http.StatusOK {
		t.Fatalf("fetch: expected 200, got %d", fetchResp.StatusCode)
	}
	var fetchBody struct {
		ID           string `json:"id"`
		HostUserID   string `json:"host_user_id"`
		HostUserName string `json:"host_user_name"`
	}
	ReadJSON(t, fetchResp, &fetchBody)
	if fetchBody.ID != first.ID.String() || fetchBody.HostUserID != host.ID.String() || fetchBody.HostUserName != host.Name {
		t.Fatalf("fetch projection lost fields: %+v", fetchBody)
	}

	allResp := Do(t, app, http.MethodGet, "/ride/all", nil, AsUser(viewer.Email))
	if allResp.StatusCode != http.StatusOK {
		t.Fatalf("all: expected 200, got %d", allResp.StatusCode)
	}
	var allBody []struct {
		ID           string `json:"id"`
		HostUserName string `json:"host_user_name"`
	}
	ReadJSON(t, allResp, &allBody)
	if len(allBody) != 2 {
		t.Fatalf("expected two rides, got %d: %+v", len(allBody), allBody)
	}
	seen := map[string]bool{}
	for _, ride := range allBody {
		seen[ride.ID] = true
		if ride.HostUserName != host.Name {
			t.Fatalf("all projection lost host name: %+v", ride)
		}
	}
	if !seen[first.ID.String()] || !seen[second.ID.String()] {
		t.Fatalf("all response missing seeded rides: %+v", allBody)
	}
}
