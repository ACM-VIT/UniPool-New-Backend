package models

import "github.com/google/uuid"

// Message model
type Message struct {
	BaseModel
	RideID   uuid.UUID `gorm:"not null" json:"ride_id" valid:"required~Ride ID is required"`
	SenderID uuid.UUID `gorm:"not null" json:"sender_id" valid:"required~Sender ID is required"`
	Sender   User      `gorm:"foreignKey:SenderID; constraint:OnDelete:SET NULL" json:"sender"`
	Content  string    `gorm:"type:text;not null" json:"content" valid:"required~Content is required"`
}
