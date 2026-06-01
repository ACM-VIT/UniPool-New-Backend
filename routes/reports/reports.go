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

// allowedReasons keeps moderation categories explicit on the API boundary.
var allowedReasons = map[string]bool{
	"safety":        true,
	"harassment":    true,
	"spam":          true,
	"inappropriate": true,
	"scam":          true,
	"other":         true,
}

// CreateReport accepts a user-submitted moderation report.
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

	// Reports need at least one target to be actionable.
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
		// A report cannot target its own reporter.
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
