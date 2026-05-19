package reports

import (
	"context"
	"log"
	"strings"
	"time"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// allowedReasons keeps the report categories explicit so the wire
// stays clean. New categories require a code change — intentional,
// since the moderation team has to know what each category means.
var allowedReasons = map[string]bool{
	"safety":      true,
	"harassment":  true,
	"spam":        true,
	"inappropriate": true,
	"scam":        true,
	"other":       true,
}

// CreateReport accepts a user-submitted report from the chat settings
// sheet. Saved to the `reports` table with status=`pending` so the
// moderation tools can pick it up off the server.
//
// Body shape:
//
//	{
//	  "reported_user_id": "uuid",       // optional
//	  "ride_id":          "uuid",       // optional, scopes to a trip
//	  "chat_room_id":     "string",     // optional, ride id or dm_*
//	  "reason":           "harassment", // required, one of allowedReasons
//	  "details":          "free text"   // optional
//	}
func CreateReport(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Unauthorized",
		})
	}

	type payload struct {
		ReportedUserID string `json:"reported_user_id"`
		RideID         string `json:"ride_id"`
		ChatRoomID     string `json:"chat_room_id"`
		Reason         string `json:"reason"`
		Details        string `json:"details"`
	}
	var body payload
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid JSON body",
		})
	}

	body.Reason = strings.ToLower(strings.TrimSpace(body.Reason))
	body.Details = strings.TrimSpace(body.Details)

	if !allowedReasons[body.Reason] {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid reason. Use one of: safety, harassment, spam, inappropriate, scam, other.",
		})
	}

	// Require at least *something* to report — a user, a ride, or a
	// chat room. Otherwise the report has no target and is noise.
	if body.ReportedUserID == "" && body.RideID == "" && body.ChatRoomID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Report needs a target (reported_user_id, ride_id, or chat_room_id).",
		})
	}

	report := models.Report{
		ReporterID: user.ID,
		Reason:     body.Reason,
		Details:    body.Details,
		Status:     "pending",
	}

	if body.ReportedUserID != "" {
		uid, err := uuid.Parse(body.ReportedUserID)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid reported_user_id",
			})
		}
		// Self-reports are almost always a bug or abuse of the form —
		// reject loudly so the client can show a clear error.
		if uid == user.ID {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "You can't report yourself.",
			})
		}
		report.ReportedUserID = &uid
	}
	if body.RideID != "" {
		rid, err := uuid.Parse(body.RideID)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid ride_id",
			})
		}
		report.RideID = &rid
	}
	if body.ChatRoomID != "" {
		room := body.ChatRoomID
		report.ChatRoomID = &room
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Database.Db.WithContext(ctx).Create(&report).Error; err != nil {
		log.Printf("reports: failed to create report from %s: %v", user.ID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to file report",
		})
	}

	log.Printf("reports: user=%s filed reason=%s target_user=%v ride=%v",
		user.ID, report.Reason, report.ReportedUserID, report.RideID)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"status":    "OK",
		"report_id": report.ID.String(),
	})
}
