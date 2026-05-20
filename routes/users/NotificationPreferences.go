package users

import (
	"context"
	"log"
	"time"

	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// GetNotificationPreferences returns the user's global category
// toggles. Missing rows are returned as `enabled: true` (default),
// so the settings UI always sees all categories regardless of how
// many opt-outs the user has actually saved.
//
// Per-ride overrides are intentionally NOT part of this response —
// those are listed via a separate endpoint or read inline on the
// chat-settings sheet (it knows which ride it's about).
func GetNotificationPreferences(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var rows []models.NotificationPreference
	if err := database.Database.Db.WithContext(ctx).
		Where("user_id = ? AND ride_id IS NULL", user.ID).
		Find(&rows).Error; err != nil {
		log.Printf("GetNotificationPreferences: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}

	stored := map[string]bool{}
	for _, r := range rows {
		stored[r.Category] = r.Enabled
	}

	type entry struct {
		Category string `json:"category"`
		Enabled  bool   `json:"enabled"`
	}
	out := make([]entry, 0, len(helpers.AllNotifCategories))
	for _, cat := range helpers.AllNotifCategories {
		enabled, ok := stored[cat]
		if !ok {
			enabled = true
		}
		out = append(out, entry{Category: cat, Enabled: enabled})
	}
	return c.JSON(fiber.Map{"preferences": out})
}

// SetNotificationPreference upserts a single global category
// toggle. Body: { "category": "chat_messages", "enabled": false }.
func SetNotificationPreference(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var body struct {
		Category string `json:"category"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil || body.Enabled == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing category or enabled"})
	}
	if !isKnownCategory(body.Category) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown category"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Database.Db.WithContext(ctx).Exec(`
		INSERT INTO notification_preferences (id, user_id, category, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, now(), now())
		ON CONFLICT (user_id, category) WHERE ride_id IS NULL
		DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()
	`, uuid.New(), user.ID, body.Category, *body.Enabled).Error; err != nil {
		log.Printf("SetNotificationPreference upsert: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "save failed"})
	}
	return c.JSON(fiber.Map{"category": body.Category, "enabled": *body.Enabled})
}

// SetRideChatMute is the per-ride scoped form used by the chat
// settings sheet. Body: { "muted": true } toggles the chat-messages
// category for that specific ride.
//
// URL: PUT /ride/:ride_id/chat-mute
func SetRideChatMute(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	rideID, err := uuid.Parse(c.Params("ride_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}
	var body struct {
		Muted *bool `json:"muted"`
	}
	if err := c.BodyParser(&body); err != nil || body.Muted == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing muted flag"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	enabled := !*body.Muted // muted=true => enabled=false

	if err := database.Database.Db.WithContext(ctx).Exec(`
		INSERT INTO notification_preferences (id, user_id, category, ride_id, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, now(), now())
		ON CONFLICT (user_id, category, ride_id) WHERE ride_id IS NOT NULL
		DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()
	`, uuid.New(), user.ID, helpers.NotifChatMessages, rideID, enabled).Error; err != nil {
		log.Printf("SetRideChatMute upsert: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "save failed"})
	}
	return c.JSON(fiber.Map{"ride_id": rideID, "muted": *body.Muted})
}

// GetRideChatMute returns the per-ride mute state so the chat
// settings sheet can render the correct initial toggle position.
func GetRideChatMute(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	rideID, err := uuid.Parse(c.Params("ride_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}
	allowed, err := helpers.IsNotificationAllowed(user.ID, helpers.NotifChatMessages, rideID)
	if err != nil {
		log.Printf("GetRideChatMute: %v", err)
	}
	return c.JSON(fiber.Map{"ride_id": rideID, "muted": !allowed})
}

func isKnownCategory(c string) bool {
	for _, k := range helpers.AllNotifCategories {
		if k == c {
			return true
		}
	}
	return false
}
