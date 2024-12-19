package models

import "github.com/google/uuid"

// Message model
type Message struct {
	BaseModel
	RideID   uuid.UUID `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"ride_id" valid:"required~Ride ID is required"`
	Ride     Ride      `gorm:"foreignKey:RideID;references:ID" json:"ride" valid:"-"`
	SenderID uuid.UUID `gorm:"foreignKey:UserID;references:ID" json:"user" valid:"-"`
	Sender   User      `gorm:"foreignKey:PassengerID;references:ID" json:"passenger" valid:"-"`
	Content  string    `json:"content"`
}
