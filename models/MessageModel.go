package models

import "github.com/google/uuid"

// Message model
type Message struct {
	BaseModel
	RideID   *uuid.UUID `gorm:"index" json:"ride_id,omitempty"`                                    // Nullable for DM messages
	DMRoomID *string    `gorm:"index" json:"dm_room_id,omitempty"`                                 // For DM messages (format: dm_userId1_userId2)
	SenderID uuid.UUID  `gorm:"not null" json:"sender_id" valid:"required~Sender ID is required"`
	Sender   User       `gorm:"foreignKey:SenderID; constraint:OnDelete:SET NULL" json:"sender"`
	Content  string     `gorm:"type:text;not null" json:"content" valid:"required~Content is required"`
}
