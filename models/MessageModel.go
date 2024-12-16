package models

import (
	"gorm.io/gorm"
)

// Message model
type Message struct {
	gorm.Model
	RideID   uint   `gorm:"not null;uniqueIndex:idx_booking_ride_passenger" json:"ride_id" valid:"required~Ride ID is required"`
	Ride     Ride   `gorm:"foreignKey:RideID;references:ID" json:"ride" valid:"-"`
	SenderID uint   `gorm:"foreignKey:UserID;references:ID" json:"user" valid:"-"`
	Sender   User   `gorm:"foreignKey:PassengerID;references:ID" json:"passenger" valid:"-"`
	Content  string `json:"content"`
}
