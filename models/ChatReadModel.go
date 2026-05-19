package models

import (
	"time"

	"github.com/google/uuid"
)

// ChatRead records the last point in a chat that a user has read up
// to. One row per (user, ride) pair (or, in future, per DM room).
// Powers the unread-count badge on the chat list without forcing the
// client to track read state locally.
//
// The (UserID, RideID) tuple has a unique index so the API can upsert
// freely on every "user opened the chat" event.
type ChatRead struct {
	BaseModel
	UserID     uuid.UUID  `gorm:"not null;uniqueIndex:idx_chat_reads_user_ride" json:"user_id"`
	RideID     *uuid.UUID `gorm:"uniqueIndex:idx_chat_reads_user_ride" json:"ride_id,omitempty"`
	DMRoomID   *string    `gorm:"index" json:"dm_room_id,omitempty"`
	LastReadAt time.Time  `gorm:"not null" json:"last_read_at"`
}
