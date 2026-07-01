package helpers

import (
	"os"
	"testing"

	"github.com/joho/godotenv"
)

// TestSendUnipoolInviteLive actually sends the invite email via SES so the
// template + copy can be reviewed in a real inbox. It is opt-in so normal
// `go test ./...` runs stay offline and never send live email by accident.
//
// Run it explicitly:
//
//	UNIPOOL_RUN_LIVE_EMAIL_TEST=1 go test ./helpers -run TestSendUnipoolInviteLive -v
func TestSendUnipoolInviteLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live SES send in -short mode")
	}
	if os.Getenv("UNIPOOL_RUN_LIVE_EMAIL_TEST") != "1" {
		t.Skip("set UNIPOOL_RUN_LIVE_EMAIL_TEST=1 to send the live SES invite")
	}
	// Load AWS + app config the same way the server does.
	_ = godotenv.Load("../.env")

	err := SendUnipoolInvite(InviteEmailParams{
		ToEmail:       "ishaanforschool@gmail.com",
		InviterName:   "Ishaan Samdani",
		InviterFirst:  "Ishaan",
		StartLocation: "VIT Vellore",
		EndLocation:   "Chennai Airport",
		JoinURL:       "https://unipool.in/download",
	})
	if err != nil {
		t.Fatalf("SendUnipoolInvite failed: %v", err)
	}
	t.Log("invite email sent to ishaanforschool@gmail.com")
}
