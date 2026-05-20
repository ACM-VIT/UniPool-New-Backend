package models

import (
	"github.com/google/uuid"
)

// NotificationPreference is a per-user opt-out for a notification
// category, optionally scoped to a specific ride.
//
// Two modes of use:
//
//  1. Global category opt-out:
//     user_id = U, category = "chat_messages", ride_id = NULL, enabled = false
//     → user has muted ALL chat notifications across every ride.
//
//  2. Per-ride mute (the chat-settings "mute notifications" toggle):
//     user_id = U, category = "chat_messages", ride_id = R, enabled = false
//     → user has muted chat notifications for THIS ride only.
//
// The FCM sender resolves both: per-ride row wins if present,
// otherwise the global row. Absence of any row = enabled (default).
type NotificationPreference struct {
	BaseModel

	UserID uuid.UUID `gorm:"type:uuid;not null;index:idx_notif_pref_lookup,priority:1;uniqueIndex:idx_notif_pref_global,priority:1,where:ride_id IS NULL;uniqueIndex:idx_notif_pref_ride,priority:1,where:ride_id IS NOT NULL" json:"user_id"`

	// Stable string keys — see helpers/notifications.go for the
	// canonical list (chat_messages, ride_updates, trip_reminders,
	// rating_prompts).
	Category string `gorm:"type:varchar(40);not null;index:idx_notif_pref_lookup,priority:2;uniqueIndex:idx_notif_pref_global,priority:2,where:ride_id IS NULL;uniqueIndex:idx_notif_pref_ride,priority:2,where:ride_id IS NOT NULL" json:"category"`

	// Optional per-ride scope. Nil = the global default for this
	// category. Non-nil = override that beats the global setting for
	// pushes tied to that ride.
	RideID *uuid.UUID `gorm:"type:uuid;index:idx_notif_pref_lookup,priority:3;uniqueIndex:idx_notif_pref_ride,priority:3,where:ride_id IS NOT NULL" json:"ride_id,omitempty"`

	Enabled bool `gorm:"not null" json:"enabled"`
}
