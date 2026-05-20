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
