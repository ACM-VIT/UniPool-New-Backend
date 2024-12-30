package models

import "github.com/google/uuid"

// Message model
type Message struct {
	BaseModel
	RideID   uuid.UUID `gorm:"not null;uniqueIndex:idx_message_ride_sender" json:"ride_id" valid:"required~Ride ID is required"`
	Ride     Ride      `gorm:"foreignKey:RideID;references:ID" json:"ride" valid:"-"`
	SenderID uuid.UUID `gorm:"not null;uniqueIndex:idx_message_ride_sender" json:"sender_id" valid:"required~Passenger ID is required"`
	Sender   User      `gorm:"foreignKey:SenderID;references:ID" json:"passenger" valid:"-"`
	Content  string    `json:"content"`
}
