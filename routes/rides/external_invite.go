package rides

import (
	"os"
	"strings"

	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// inviteRequest is the body for POST /external/invite.
//
// external_ride_id identifies the external ride the user tapped Contact on.
// The route/host context fields are optional client fallbacks used only when
// the ride has aged out of the server cache; the host email is never accepted
// from the client and is always resolved server-side.
type inviteRequest struct {
	ExternalRideID string `json:"external_ride_id"`
	StartLocation  string `json:"start_location"`
	EndLocation    string `json:"end_location"`
	HostName       string `json:"host_name"`
}

// SendExternalInvite emails an external ride host a "wants to UniPool with you"
// invite on behalf of the signed-in user. The host's email is resolved from the
// cached external ride (never sent by the client). Set UNIPOOL_INVITE_TEST_EMAIL
// to redirect every invite to one inbox while testing.
func SendExternalInvite(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok || strings.TrimSpace(user.Name) == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "Sign in to invite a host.")
	}

	var req inviteRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "Invalid request.")
	}

	hostName := strings.TrimSpace(req.HostName)
	start := strings.TrimSpace(req.StartLocation)
	end := strings.TrimSpace(req.EndLocation)
	hostEmail := ""

	if ride, found := FindExternalRideByID(req.ExternalRideID); found {
		hostEmail = ride.HostEmail
		if hostName == "" {
			hostName = ride.HostName
		}
		if start == "" {
			start = ride.PickupPoint
		}
		if end == "" {
			end = ride.Destination
		}
	}

	// Testing override: when set, every invite goes to this inbox instead of
	// the real host. Unset it in production to email actual hosts.
	recipient := strings.TrimSpace(hostEmail)
	if testTo := strings.TrimSpace(os.Getenv("UNIPOOL_INVITE_TEST_EMAIL")); testTo != "" {
		recipient = testTo
	}
	if recipient == "" {
		// We have no email for this host; the client falls back to WhatsApp.
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"sent":  false,
			"error": "no_email",
		})
	}

	rideID := strings.TrimSpace(req.ExternalRideID)
	if rideID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "Missing ride.")
	}

	// Dedup lease: one invite per (inviter, external ride). The insert IS the
	// lease. RowsAffected == 1 means this request owns the send; 0 means the
	// invite already went out and we must not send a second email.
	lease := database.Database.Db.Exec(
		`INSERT INTO external_invites_sent (inviter_user_id, external_ride_id, sent_at)
		 VALUES (?, ?, NOW())
		 ON CONFLICT (inviter_user_id, external_ride_id) DO NOTHING`,
		user.ID, rideID,
	)
	if lease.Error != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"sent":  false,
			"error": "server",
		})
	}
	if lease.RowsAffected == 0 {
		// Already invited for this ride, so report success without re-sending.
		return c.JSON(fiber.Map{"sent": true, "already": true})
	}

	if err := helpers.SendUnipoolInvite(helpers.InviteEmailParams{
		ToEmail:       recipient,
		InviterName:   user.Name,
		InviterFirst:  helpers.FirstNameOf(user.Name),
		StartLocation: start,
		EndLocation:   end,
		JoinURL:       "https://unipool.in/download",
	}); err != nil {
		// Release the lease so the user can try again.
		database.Database.Db.Exec(
			`DELETE FROM external_invites_sent WHERE inviter_user_id = ? AND external_ride_id = ?`,
			user.ID, rideID,
		)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"sent":  false,
			"error": "send_failed",
		})
	}

	return c.JSON(fiber.Map{"sent": true})
}
