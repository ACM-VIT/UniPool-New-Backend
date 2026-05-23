package helpers

import (
	"errors"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Notification categories. Keep these strings stable — they're the
// keys clients send/receive when toggling preferences, and any
// drift will silently leave users with broken settings until a
// migration patches their rows.
const (
	NotifChatMessages   = "chat_messages"
	NotifRideUpdates    = "ride_updates"
	NotifTripReminders  = "trip_reminders"
	NotifRatingPrompts  = "rating_prompts"
)

// AllNotifCategories is the canonical list the settings UI walks.
// Order is presentation order, not severity.
var AllNotifCategories = []string{
	NotifChatMessages,
	NotifRideUpdates,
	NotifTripReminders,
	NotifRatingPrompts,
}

// FilterAllowedRecipients runs the same gate as IsNotificationAllowed
// but for a slice of users in two batched queries (global category +
// per-ride overrides), instead of 2 queries per user. Returns the
// subset of `userIDs` whose pref resolves to allowed.
//
// Used by FCM fan-out (chat / ride-update) where N recipients
// otherwise meant 2N DB round-trips just to decide who to push to.
// Fails open: any DB error is treated as "allowed" (matching the
// single-user variant's posture — we'd rather over-notify than
// silently drop on a transient DB hiccup).
func FilterAllowedRecipients(userIDs []uuid.UUID, category string, rideID uuid.UUID) []uuid.UUID {
	if len(userIDs) == 0 || category == "" {
		return userIDs
	}
	// Default-allowed map. Each layer below tightens the decision
	// when a pref row exists; absence keeps the user allowed.
	decision := make(map[uuid.UUID]bool, len(userIDs))
	for _, id := range userIDs {
		decision[id] = true
	}

	type prefRow struct {
		UserID  uuid.UUID `gorm:"column:user_id"`
		Enabled bool      `gorm:"column:enabled"`
	}

	// 1) Global category prefs — applied first as baseline.
	var globals []prefRow
	if err := database.Database.Db.
		Table("notification_preferences").
		Select("user_id, enabled").
		Where("user_id IN ? AND category = ? AND ride_id IS NULL", userIDs, category).
		Scan(&globals).Error; err == nil {
		for _, g := range globals {
			decision[g.UserID] = g.Enabled
		}
	}

	// 2) Per-ride overrides — wins over global if present.
	if rideID != uuid.Nil {
		var perRide []prefRow
		if err := database.Database.Db.
			Table("notification_preferences").
			Select("user_id, enabled").
			Where("user_id IN ? AND category = ? AND ride_id = ?", userIDs, category, rideID).
			Scan(&perRide).Error; err == nil {
			for _, p := range perRide {
				decision[p.UserID] = p.Enabled
			}
		}
	}

	allowed := make([]uuid.UUID, 0, len(userIDs))
	for _, id := range userIDs {
		if decision[id] {
			allowed = append(allowed, id)
		}
	}
	return allowed
}

// IsNotificationAllowed resolves the user's preference for a given
// (category, ride) push. Per-ride row wins if present; otherwise
// the global row decides; absence of any row = allowed.
//
// `rideID` is optional — pass uuid.Nil for non-ride-scoped pushes
// (e.g. account-level notifications), and the per-ride lookup is
// skipped.
//
// Errors from the DB are surfaced via the second return so the
// caller can decide whether to fail open (send anyway, log) or
// fail closed (drop). Current call sites fail open — we'd rather
// over-notify than silently drop on a transient DB hiccup.
//
// For multi-recipient fan-outs prefer FilterAllowedRecipients —
// resolves N users in 2 batched queries instead of 2N.
func IsNotificationAllowed(userID uuid.UUID, category string, rideID uuid.UUID) (bool, error) {
	if userID == uuid.Nil || category == "" {
		return true, nil
	}

	// Per-ride override first.
	if rideID != uuid.Nil {
		var perRide models.NotificationPreference
		err := database.Database.Db.
			Where("user_id = ? AND category = ? AND ride_id = ?", userID, category, rideID).
			First(&perRide).Error
		if err == nil {
			return perRide.Enabled, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return true, err
		}
	}

	// Global category.
	var global models.NotificationPreference
	err := database.Database.Db.
		Where("user_id = ? AND category = ? AND ride_id IS NULL", userID, category).
		First(&global).Error
	if err == nil {
		return global.Enabled, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	return true, err
}
