package models

import (
	"database/sql/driver"
	"encoding/json"

	"github.com/google/uuid"
)

// MessageMetadata is a free-form JSON sidecar attached to a message.
// Non-'user' kinds (payment_marker, payment_ack, etc.) carry the
// structured data the chat renderer needs without parsing the
// Content string. For kind='user' messages this is always {}.
type MessageMetadata map[string]any

// Scan / Value let GORM round-trip the jsonb column as a Go map
// without having to drop down to raw json.RawMessage in handlers.
func (m *MessageMetadata) Scan(value any) error {
	if value == nil {
		*m = MessageMetadata{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	if len(bytes) == 0 {
		*m = MessageMetadata{}
		return nil
	}
	return json.Unmarshal(bytes, m)
}

func (m MessageMetadata) Value() (driver.Value, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// Kind constants — keep these stable. Clients dispatch rendering on
// the string value, so renaming any one of them silently breaks an
// app already in the wild.
const (
	MessageKindUser          = "user"
	MessageKindPaymentMarker = "payment_marker"
	MessageKindPaymentAck    = "payment_ack"
)

// Message model
type Message struct {
	BaseModel
	RideID   *uuid.UUID `gorm:"index" json:"ride_id,omitempty"`                                    // Nullable for DM messages
	DMRoomID *string    `gorm:"index" json:"dm_room_id,omitempty"`                                 // For DM messages (format: dm_userId1_userId2)
	SenderID uuid.UUID  `gorm:"not null" json:"sender_id" valid:"required~Sender ID is required"`
	Sender   User       `gorm:"foreignKey:SenderID;references:ID;constraint:OnDelete:CASCADE" json:"sender"`
	Content  string     `gorm:"type:text;not null" json:"content" valid:"required~Content is required"`

	// Kind controls how the client renders the message. 'user' is a
	// regular text bubble; everything else is a system card driven
	// by Metadata. Default 'user' so every existing message + every
	// new user-typed message lands in the default branch.
	Kind     string          `gorm:"type:varchar(32);not null;default:'user'" json:"kind"`
	Metadata MessageMetadata `gorm:"type:jsonb;not null;default:'{}'" json:"metadata"`
}
