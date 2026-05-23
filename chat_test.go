package main

// Regression tests for the chat surface: list, send, read tracking.
// The chat list query touches three tables (rides + bookings +
// messages) with a join + window function, so a schema or query
// regression here cascades into a blank "Chats" tab in the app.

import (
	"net/http"
	"sort"
	"testing"

	"unipool-backend/models"
)

func TestChatsMe_IncludesHostedAndPendingAndAccepted(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")

	viewer := seedUser(t, db, "Viewer", "viewer@vitstudent.ac.in", &inst.ID)
	otherHost := seedUser(t, db, "Other Host", "other-host@vitstudent.ac.in", &inst.ID)

	// Viewer hosts one ride.
	hostedRide := seedRide(t, db, viewer, RideOpts{StartLocation: "A", EndLocation: "B"})

	// Viewer is an accepted passenger on another ride.
	acceptedRide := seedRide(t, db, otherHost, RideOpts{StartLocation: "C", EndLocation: "D"})
	if err := db.Create(&models.Booking{RideID: acceptedRide.ID, PassengerID: viewer.ID, RequestStatus: "accepted"}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	// Viewer is a pending passenger on a third ride.
	pendingRide := seedRide(t, db, otherHost, RideOpts{StartLocation: "E", EndLocation: "F"})
	if err := db.Create(&models.Booking{RideID: pendingRide.ID, PassengerID: viewer.ID, RequestStatus: "pending"}).Error; err != nil {
		t.Fatalf("seed pending booking: %v", err)
	}

	// And a ride the viewer was rejected from — must NOT appear.
	rejectedRide := seedRide(t, db, otherHost, RideOpts{StartLocation: "G", EndLocation: "H"})
	if err := db.Create(&models.Booking{RideID: rejectedRide.ID, PassengerID: viewer.ID, RequestStatus: "rejected"}).Error; err != nil {
		t.Fatalf("seed rejected booking: %v", err)
	}

	app := setupTestApp(t)
	resp := do(t, app, http.MethodGet, "/chats/me", nil, asUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ChatRooms []struct {
			ID         string `json:"id"`
			ViewerRole string `json:"viewer_role"`
		} `json:"chat_rooms"`
	}
	readJSON(t, resp, &body)

	gotRides := make(map[string]string)
	for _, r := range body.ChatRooms {
		gotRides[r.ID] = r.ViewerRole
	}
	if _, ok := gotRides[hostedRide.ID.String()]; !ok {
		t.Errorf("hosted ride %s missing from /chats/me", hostedRide.ID)
	}
	if _, ok := gotRides[acceptedRide.ID.String()]; !ok {
		t.Errorf("accepted ride %s missing from /chats/me", acceptedRide.ID)
	}
	if _, ok := gotRides[pendingRide.ID.String()]; !ok {
		t.Errorf("pending ride %s missing from /chats/me", pendingRide.ID)
	}
	if _, ok := gotRides[rejectedRide.ID.String()]; ok {
		t.Errorf("rejected ride %s should NOT be in chats list", rejectedRide.ID)
	}
}

func TestChatSendMessage_PersistsAndShowsInPreview(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "msg-host@vitstudent.ac.in", &inst.ID)
	passenger := seedUser(t, db, "Pax", "msg-pax@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})
	if err := db.Create(&models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted"}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := setupTestApp(t)
	// Host sends a message.
	resp := do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/message",
		map[string]any{"content": "Hello, world"}, asUser(host.Email))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("send: expected 201, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)

	// Passenger fetches /chats/me; the preview should carry the message.
	resp = do(t, app, http.MethodGet, "/chats/me", nil, asUser(passenger.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chats/me: expected 200, got %d", resp.StatusCode)
	}
	var listBody struct {
		ChatRooms []struct {
			ID          string `json:"id"`
			LastMessage *struct {
				Content string `json:"content"`
			} `json:"last_message"`
		} `json:"chat_rooms"`
	}
	readJSON(t, resp, &listBody)

	var room *struct {
		ID          string `json:"id"`
		LastMessage *struct {
			Content string `json:"content"`
		} `json:"last_message"`
	}
	for i := range listBody.ChatRooms {
		if listBody.ChatRooms[i].ID == ride.ID.String() {
			room = &listBody.ChatRooms[i]
			break
		}
	}
	if room == nil {
		t.Fatalf("ride %s missing from passenger's chat list", ride.ID)
	}
	if room.LastMessage == nil {
		t.Fatalf("last_message not populated on chat preview")
	}
	if room.LastMessage.Content != "Hello, world" {
		t.Errorf("preview content: got %q want %q", room.LastMessage.Content, "Hello, world")
	}
}

func TestChatSendMessage_NonMemberForbidden(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "nmsg-host@vitstudent.ac.in", &inst.ID)
	intruder := seedUser(t, db, "Intruder", "nmsg-intr@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})

	app := setupTestApp(t)
	// Intruder has NO booking on this ride. Should be rejected with 403.
	resp := do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/message",
		map[string]any{"content": "spy"}, asUser(intruder.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member, got %d", resp.StatusCode)
	}
	readJSON(t, resp, nil)

	// And no message row should have landed.
	var count int64
	db.Model(&models.Message{}).Where("ride_id = ?", ride.ID).Count(&count)
	if count != 0 {
		t.Errorf("intruder's message persisted (count=%d)", count)
	}
}

func TestChatSendMessage_RejectsEmptyContent(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "emp-host@vitstudent.ac.in", &inst.ID)
	ride := seedRide(t, db, host, RideOpts{})
	app := setupTestApp(t)

	for _, content := range []string{"", "   ", "\t\n  "} {
		resp := do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/message",
			map[string]any{"content": content}, asUser(host.Email))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("content %q: expected 400, got %d", content, resp.StatusCode)
		}
		readJSON(t, resp, nil)
	}
}

// TestChatsMe_Ordering keeps the list deterministic. /chats/me orders
// by ride.start_time DESC — soonest-departing trips first. If a future
// change accidentally orders by created_at or message timestamp, the
// home chat list jumps around between sessions; this test pins that
// down.
func TestChatsMe_OrderingByStartTime(t *testing.T) {
	db := connectTestDB(t)
	resetDB(t)
	inst := seedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := seedUser(t, db, "Host", "ord-host@vitstudent.ac.in", &inst.ID)

	// Three hosted rides — viewer_role = host so they all land in /chats/me.
	rideA := seedRide(t, db, host, RideOpts{StartLocation: "A"})
	rideB := seedRide(t, db, host, RideOpts{StartLocation: "B"})
	rideC := seedRide(t, db, host, RideOpts{StartLocation: "C"})

	// Set deterministic start times so we can predict the order.
	if err := db.Model(&models.Ride{}).Where("id = ?", rideA.ID).Update("start_time", rideA.StartTime.Add(1)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Ride{}).Where("id = ?", rideB.ID).Update("start_time", rideB.StartTime.Add(2*60*60*1000_000_000)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Ride{}).Where("id = ?", rideC.ID).Update("start_time", rideC.StartTime.Add(4*60*60*1000_000_000)).Error; err != nil {
		t.Fatal(err)
	}

	app := setupTestApp(t)
	resp := do(t, app, http.MethodGet, "/chats/me", nil, asUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ChatRooms []struct {
			ID            string `json:"id"`
			StartLocation string `json:"start_location"`
		} `json:"chat_rooms"`
	}
	readJSON(t, resp, &body)

	if len(body.ChatRooms) != 3 {
		t.Fatalf("expected 3 chat rooms, got %d", len(body.ChatRooms))
	}
	gotOrder := make([]string, 0, 3)
	for _, r := range body.ChatRooms {
		gotOrder = append(gotOrder, r.StartLocation)
	}
	// C should be first (latest start_time), then B, then A.
	want := []string{"C", "B", "A"}
	if !equalSlice(gotOrder, want) {
		t.Errorf("ordering: got %v want %v (DESC by start_time)", gotOrder, want)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Tiny sanity helper used by debug prints if a test fails — keeps the
// import section honest about unused packages.
var _ = sort.Strings
