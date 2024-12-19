package models

import (
	"time"

	"github.com/google/uuid"
)

// Ride struct
type Ride struct {
	BaseModel
	HostUserID    uuid.UUID `gorm:"not null" json:"host_user_id" valid:"required~Host user ID is required"`
	HostUser      User      `gorm:"foreignKey:HostUserID;references:ID" json:"host_user" valid:"-"`
	StartLocation string    `gorm:"size:255;not null;" json:"start_location" valid:"required~Start location is required"`
	EndLocation   string    `gorm:"size:255;not null;" json:"end_location" valid:"required~End location is required"`
	StartTime     time.Time `gorm:"not null;" json:"start_time" valid:"required~Start time is required"`
	TotalSeats    uint      `gorm:"not null;" json:"total_seats" valid:"required~Total seats is required"`
	BookedSeats   uint      `gorm:"not null;" json:"booked_seats" valid:"required~Booked seats is required"`
	TotalPrice    uint      `gorm:"not null;" json:"total_price" valid:"required~Total price is required"`
	RideStatus    string    `gorm:"not null;default:'ongoing'" json:"ride_status"`
	GenderPref    string    `gorm:"not null;default:'any'" json:"gender_preference"`
}
