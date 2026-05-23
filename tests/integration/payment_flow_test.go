package integration

// Regression tests for the payment-confirmation flow:
//
//   passenger dismisses trip card with signal=paid
//     -> bookings.payment_status flips to "pending"
//     -> a payment_marker system message lands in the ride group chat
//
//   host POSTs /booking/:id/payment-ack with ack=received
//     -> bookings.payment_status flips to "confirmed"
//     -> bookings.payment_confirmed_at stamped
//     -> a payment_ack system message lands in the ride group chat
//
// The chat client renders these system messages as cards (kind !=
// 'user' is the dispatch); these tests pin the server-side contract.

import (
	"net/http"
	"testing"

	"unipool-backend/models"
)

func TestPaymentFlow_DismissPaidPostsMarker(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pay-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Riya", "pay-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 250})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/trip-card/dismiss",
		map[string]any{"booking_id": booking.ID.String(), "signal": "paid"},
		AsUser(passenger.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss: expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	// Booking row should be pending.
	var refreshed models.Booking
	if err := db.First(&refreshed, booking.ID).Error; err != nil {
		t.Fatalf("reload booking: %v", err)
	}
	if refreshed.PaymentStatus != "pending" {
		t.Errorf("payment_status: got %q want pending", refreshed.PaymentStatus)
	}
	if refreshed.DismissalSignal != "paid" {
		t.Errorf("dismissal_signal: got %q want paid", refreshed.DismissalSignal)
	}

	// A payment_marker system message should sit in the ride chat,
	// attributed to the passenger, carrying the booking_id and
	// amount in metadata.
	var msgs []models.Message
	if err := db.Where("ride_id = ? AND kind = ?", ride.ID, models.MessageKindPaymentMarker).Find(&msgs).Error; err != nil {
		t.Fatalf("query messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 payment_marker, got %d", len(msgs))
	}
	m := msgs[0]
	if m.SenderID != passenger.ID {
		t.Errorf("sender: got %s want %s (passenger)", m.SenderID, passenger.ID)
	}
	if bid, _ := m.Metadata["booking_id"].(string); bid != booking.ID.String() {
		t.Errorf("metadata.booking_id: got %v want %s", m.Metadata["booking_id"], booking.ID)
	}
	if got, _ := m.Metadata["amount"].(float64); got != 250 {
		t.Errorf("metadata.amount: got %v want 250", m.Metadata["amount"])
	}
	if m.Content == "" {
		t.Errorf("content empty; expected human-readable summary")
	}
}

func TestPaymentFlow_DismissPaidPostsMarkerOnlyOnce(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pay-dedupe-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Riya", "pay-dedupe-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 250})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	for i := 0; i < 2; i++ {
		resp := Do(t, app, http.MethodPost, "/trip-card/dismiss",
			map[string]any{"booking_id": booking.ID.String(), "signal": "paid"},
			AsUser(passenger.Email))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("dismiss %d: expected 200, got %d", i+1, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}

	var count int64
	db.Model(&models.Message{}).
		Where("ride_id = ? AND kind = ?", ride.ID, models.MessageKindPaymentMarker).
		Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 payment_marker after re-dismiss, got %d", count)
	}
}

func TestPaymentFlow_DismissNoShowDoesNotPostMarker(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "pay-h2@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Riya", "pay-p2@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 250})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/trip-card/dismiss",
		map[string]any{"booking_id": booking.ID.String(), "signal": "no_show"},
		AsUser(passenger.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss: expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var refreshed models.Booking
	db.First(&refreshed, booking.ID)
	if refreshed.PaymentStatus != "none" {
		t.Errorf("payment_status: got %q want none (no_show shouldn't flip)", refreshed.PaymentStatus)
	}
	var count int64
	db.Model(&models.Message{}).Where("ride_id = ? AND kind = ?", ride.ID, models.MessageKindPaymentMarker).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 payment_markers for no_show signal, got %d", count)
	}
}

func TestPaymentAck_HostConfirmsReceived(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "ack-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Pax", "ack-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 180})
	booking := models.Booking{
		RideID:        ride.ID,
		PassengerID:   passenger.ID,
		RequestStatus: "accepted",
		PaymentStatus: "pending",
	}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/booking/"+booking.ID.String()+"/payment-ack",
		map[string]any{"ack": "received"}, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack: expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		PaymentStatus string `json:"payment_status"`
	}
	ReadJSON(t, resp, &body)
	if body.PaymentStatus != "confirmed" {
		t.Errorf("response payment_status: got %q want confirmed", body.PaymentStatus)
	}

	var refreshed models.Booking
	if err := db.First(&refreshed, booking.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if refreshed.PaymentStatus != "confirmed" {
		t.Errorf("DB payment_status: got %q want confirmed", refreshed.PaymentStatus)
	}
	if refreshed.PaymentConfirmedAt == nil {
		t.Errorf("payment_confirmed_at should be stamped")
	}

	var msgs []models.Message
	db.Where("ride_id = ? AND kind = ?", ride.ID, models.MessageKindPaymentAck).Find(&msgs)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 payment_ack message, got %d", len(msgs))
	}
	if ack, _ := msgs[0].Metadata["ack"].(string); ack != "received" {
		t.Errorf("metadata.ack: got %v want received", msgs[0].Metadata["ack"])
	}
	if msgs[0].SenderID != host.ID {
		t.Errorf("payment_ack sender should be host; got %s", msgs[0].SenderID)
	}
}

func TestPaymentAck_HostDisputes(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "disp-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Pax", "disp-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 90})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted", PaymentStatus: "pending"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	resp := Do(t, app, http.MethodPost, "/booking/"+booking.ID.String()+"/payment-ack",
		map[string]any{"ack": "missing"}, AsUser(host.Email))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack: expected 200, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var refreshed models.Booking
	db.First(&refreshed, booking.ID)
	if refreshed.PaymentStatus != "disputed" {
		t.Errorf("payment_status: got %q want disputed", refreshed.PaymentStatus)
	}
	var count int64
	db.Model(&models.Message{}).Where("ride_id = ? AND kind = ?", ride.ID, models.MessageKindPaymentAck).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 payment_ack (disputed), got %d", count)
	}
}

func TestPaymentAck_NonHostForbidden(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "f-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Pax", "f-pax@vitstudent.ac.in", &inst.ID)
	intruder := SeedUser(t, db, "Intruder", "f-intr@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 100})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted", PaymentStatus: "pending"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	// Passenger trying to self-confirm.
	resp := Do(t, app, http.MethodPost, "/booking/"+booking.ID.String()+"/payment-ack",
		map[string]any{"ack": "received"}, AsUser(passenger.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("passenger self-ack: expected 403, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	// Third-party stranger.
	resp = Do(t, app, http.MethodPost, "/booking/"+booking.ID.String()+"/payment-ack",
		map[string]any{"ack": "received"}, AsUser(intruder.Email))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("intruder ack: expected 403, got %d", resp.StatusCode)
	}
	ReadJSON(t, resp, nil)

	var refreshed models.Booking
	db.First(&refreshed, booking.ID)
	if refreshed.PaymentStatus != "pending" {
		t.Errorf("payment_status drifted under unauthorized acks: got %q", refreshed.PaymentStatus)
	}
}

func TestPaymentAck_RejectsInvalidAckValue(t *testing.T) {
	db := ConnectTestDB(t)
	ResetDB(t)
	inst := SeedInstitute(t, db, "VIT", "India", "vitstudent.ac.in")
	host := SeedUser(t, db, "Host", "i-host@vitstudent.ac.in", &inst.ID)
	passenger := SeedUser(t, db, "Pax", "i-pax@vitstudent.ac.in", &inst.ID)
	ride := SeedRide(t, db, host, RideOpts{TotalPrice: 100})
	booking := models.Booking{RideID: ride.ID, PassengerID: passenger.ID, RequestStatus: "accepted", PaymentStatus: "pending"}
	if err := db.Create(&booking).Error; err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	app := SetupTestApp(t)
	// "received " with trailing whitespace IS allowed — the handler
	// trims before comparison. Bad values are anything that doesn't
	// normalise to "received" or "missing".
	for _, bad := range []string{"", "yes", "no", "ok", "RECIEVED"} {
		resp := Do(t, app, http.MethodPost, "/booking/"+booking.ID.String()+"/payment-ack",
			map[string]any{"ack": bad}, AsUser(host.Email))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("ack=%q: expected 400, got %d", bad, resp.StatusCode)
		}
		ReadJSON(t, resp, nil)
	}
}
