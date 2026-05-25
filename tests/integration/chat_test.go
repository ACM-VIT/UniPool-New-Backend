package integration

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
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")

	viewer := SeedUser(t, db, "Viewer", "viewer@vitstudent.ac.in", &inst.ID)
	otherHost := SeedUser(t, db, "Other Host", "other-host@vitstudent.ac.in", &inst.ID)

	// Viewer hosts one ride.
	hostedRide := SeedRide(t, db, viewer, RideOpts{StartLocation: "A", EndLocation: "B"})

	// Viewer is an accepted passenger on another ride.
	acceptedRide := SeedRide(t, db, otherHost, RideOpts{StartLocation: "C", EndLocation: "D"})
	if err := db.Create(&models.Booking{RideID: acceptedRide.ID, PassengerID: viewer.ID, RequestStatus: "accepted"}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	// Viewer is a pending passenger on a third ride.
	pendingRide := SeedRide(t, db, otherHost, RideOpts{StartLocation: "E", EndLocation: "F"})
	if err := db.Create(&models.Booking{RideID: pendingRide.ID, PassengerID: viewer.ID, RequestStatus: "pending"}).Error; err != nil {
		t.Fatalf("seed pending booking: %v", err)
	}

	// And a ride the viewer was rejected from — must NOT appear.
	rejectedRide := SeedRide(t, db, otherHost, RideOpts{StartLocation: "G", EndLocation: "H"})
	if err := db.Create(&models.Booking{RideID: rejectedRide.ID, PassengerID: viewer.ID, RequestStatus: "rejected"}).Error; err != nil {
		t.Fatalf("seed rejected booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/chats/me", nil, AsUser(viewer.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ChatRooms []struct {
			ID         string `json:"id"`
			ViewerRole string `json:"viewer_role"`
		} `json:"chat_rooms"`
	}
	ReadJSON(t, resp, &body)

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
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "msg-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Pax", "msg-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})
	if err := db.Create(&models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted"}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	// Host sends a message.
	resp := Do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/message",
		map[string]any{"content": "Hello, world"}, AsUser(host.Email))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("send: expected 201, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	// Passenger fetches /chats/me; the preview should carry the message.
	resp = Do(t, app, http.MethodGet, "/chats/me", nil, AsUser(passenger.Email))
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
	ReadJSON(t, resp, &listBody)

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
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "nmsg-host@vitstudent.ac.in", &inst.ID)
	intruder := SeedUser(t, db, "Intruder", "nmsg-intr@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})

	app := SetupTestApp(t)
	// Intruder has NO booking on this ride. Should be rejected with 403.
	resp := Do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/message",
		map[string]any{"content": "spy"}, AsUser(intruder.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	// And no message row should have landed.
	var count int64
	db.Model(&models.Message{}).Where("ride_id = ?", ride.ID).Count(&count)
	if count != 0 {
		t.Errorf("intruder's message persisted (count=%d)", count)
	}
}

func TestChatSendMessage_RejectsEmptyContent(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "emp-host@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})
	app := SetupTestApp(t)

	for _, content := range []string{"", "   ", "\t\n  "} {
		resp := Do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/message",
			map[string]any{"content": content}, AsUser(host.Email))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("content %q: expected 400, got %d", content, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}
}

// TestChatsMe_Ordering keeps the list deterministic. /chats/me orders
// by ride.start_time DESC — soonest-departing trips first. If a future
// change accidentally orders by created_at or message timestamp, the
// home chat list jumps around between sessions; this test pins that
// down.
func TestChatsMe_OrderingByStartTime(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "ord-host@vitstudent.ac.in", &inst.ID)

	// Three hosted rides — viewer_role = host so they all land in /chats/me.
	rideA := SeedRide(t, db, host, RideOpts{StartLocation: "A"})
	rideB := SeedRide(t, db, host, RideOpts{StartLocation: "B"})
	rideC := SeedRide(t, db, host, RideOpts{StartLocation: "C"})

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

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/chats/me", nil, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ChatRooms []struct {
			ID            string `json:"id"`
			StartLocation string `json:"start_location"`
		} `json:"chat_rooms"`
	}
	ReadJSON(t, resp, &body)

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

// ---------------------------------------------------------------------
// Mark-as-read membership guards
// ---------------------------------------------------------------------
// Pre-patch, MarkRideRead and MarkDMRead would happily upsert a
// chat_reads row for any authenticated caller against any ride_id or
// dm_room_id — a stranger who happened to know an ID could spam
// read-marks against arbitrary chats. The new code rejects callers
// who aren't members of the ride / aren't one of the two DM
// participants.

func TestMarkRideRead_RejectsNonMember(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "mr-host@vitstudent.ac.in", &inst.ID)
	intruder := SeedUser(t, db, "Intruder", "mr-intr@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/read", nil, AsUser(intruder.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}

func TestMarkRideRead_AcceptsHostAndPassenger(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "mr-host2@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "mr-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})
	if err := db.Create(&models.Booking{
		RideID: ride.ID, PassengerID: pax.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	for _, viewer := range []models.User{host, pax} {
		resp := Do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/read", nil, AsUser(viewer.Email))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for participant %s, got %d", viewer.Email, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}
}

func TestGetRideMessages_MarkReadQueryUpsertsCursor(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "mrg-host@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "mrg-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})
	if err := db.Create(&models.Booking{
		RideID: ride.ID, PassengerID: pax.ID, RequestStatus: "accepted",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/chat/"+ride.ID.String()+"/messages?mark_read=1", nil, AsUser(pax.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var read models.ChatRead
	if err := db.Where("user_id = ? AND ride_id = ?", pax.ID, ride.ID).First(&read).Error; err != nil {
		t.Fatalf("expected read cursor from message fetch: %v", err)
	}
	if read.LastReadAt.IsZero() {
		t.Fatalf("read cursor timestamp was not set")
	}
}

func TestMarkRideRead_AcceptsPendingPassenger(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "mr-host3@vitstudent.ac.in", &inst.ID)
	pax := SeedUser(t, db, "Pax", "mr-pend@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{})
	if err := db.Create(&models.Booking{
		RideID: ride.ID, PassengerID: pax.ID, RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	// Pending requesters live in a DM with the host before they're
	// accepted, but the read-mark for the GROUP chat (which they don't
	// see yet) still belongs to them so the pending->accepted
	// transition leaves them at the right unread badge. Allow.
	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/chat/"+ride.ID.String()+"/read", nil, AsUser(pax.Email))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for pending passenger, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}

func TestMarkDMRead_RejectsNonParticipant(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	a := SeedUser(t, db, "A", "dm-a@vitstudent.ac.in", &inst.ID)
	b := SeedUser(t, db, "B", "dm-b@vitstudent.ac.in", &inst.ID)
	intruder := SeedUser(t, db, "I", "dm-intr@vitstudent.ac.in", &inst.ID)

	// dm_room_id = "dm_<lower_uuid>_<higher_uuid>"
	ids := []string{a.ID.String(), b.ID.String()}
	sort.Strings(ids)
	dmRoomID := "dm_" + ids[0] + "_" + ids[1]

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/dm/"+dmRoomID+"/read", nil, AsUser(intruder.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-participant, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}

func TestMarkDMRead_AcceptsParticipant(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	a := SeedUser(t, db, "A", "dm-a2@vitstudent.ac.in", &inst.ID)
	b := SeedUser(t, db, "B", "dm-b2@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, a, RideOpts{})
	if err := db.Create(&models.Booking{
		RideID: ride.ID, PassengerID: b.ID, RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	ids := []string{a.ID.String(), b.ID.String()}
	sort.Strings(ids)
	dmRoomID := "dm_" + ids[0] + "_" + ids[1]

	app := SetupTestApp(t)
	for _, viewer := range []models.User{a, b} {
		resp := Do(t, app, http.MethodPost, "/dm/"+dmRoomID+"/read", nil, AsUser(viewer.Email))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for participant %s, got %d", viewer.Email, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}
}

func TestGetDMMessages_MarkReadQueryUpsertsCursor(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	a := SeedUser(t, db, "A", "dm-mrg-a@vitstudent.ac.in", &inst.ID)
	b := SeedUser(t, db, "B", "dm-mrg-b@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, a, RideOpts{})
	if err := db.Create(&models.Booking{
		RideID: ride.ID, PassengerID: b.ID, RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	ids := []string{a.ID.String(), b.ID.String()}
	sort.Strings(ids)
	dmRoomID := "dm_" + ids[0] + "_" + ids[1]

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodGet, "/dm/"+dmRoomID+"/messages?mark_read=1", nil, AsUser(b.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var read models.ChatRead
	if err := db.Where("user_id = ? AND dm_room_id = ?", b.ID, dmRoomID).First(&read).Error; err != nil {
		t.Fatalf("expected DM read cursor from message fetch: %v", err)
	}
	if read.LastReadAt.IsZero() {
		t.Fatalf("DM read cursor timestamp was not set")
	}
}

func TestDMMessages_RequireBookingRelationship(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "dm-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Passenger", "dm-pax@vitstudent.ac.in", &inst.ID)
	stranger := SeedUser(t, db, "Stranger", "dm-stranger@vitstudent.ac.in", &inst.ID)

	ids := []string{host.ID.String(), passenger.ID.String()}
	sort.Strings(ids)
	dmRoomID := "dm_" + ids[0] + "_" + ids[1]
	app := SetupTestApp(t)

	resp := Do(t, app, http.MethodPost, "/dm/"+dmRoomID+"/message",
		map[string]any{"content": "before booking"}, AsUser(host.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("send before booking: expected 403, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	ride := SeedRide(t, db, host, RideOpts{})
	if err := db.Create(&models.Booking{
		RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	resp = Do(t, app, http.MethodPost, "/dm/"+dmRoomID+"/message",
		map[string]any{"content": "hello"}, AsUser(host.Email))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("send after booking: expected 201, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	resp = Do(t, app, http.MethodGet, "/dm/"+dmRoomID+"/messages", nil, AsUser(passenger.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("passenger read: expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	resp = Do(t, app, http.MethodGet, "/dm/"+dmRoomID+"/messages", nil, AsUser(stranger.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger read: expected 403, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)
}

func TestMarkDMRead_RejectsMalformedRoomID(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	u := SeedUser(t, db, "U", "dm-mal@vitstudent.ac.in", &inst.ID)
	app := SetupTestApp(t)

	for _, bad := range []string{"dm_", "dm__abc", "dm_abc_", "dm_only-one-uuid"} {
		resp := Do(t, app, http.MethodPost, "/dm/"+bad+"/read", nil, AsUser(u.Email))
		if resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Errorf("malformed %q: expected 4xx, got %d", bad, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}
}
