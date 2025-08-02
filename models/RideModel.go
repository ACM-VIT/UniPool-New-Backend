package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type RideSettings struct {
	ChatName             string `json:"chat_name,omitempty"`
	NotificationsMuted   bool   `json:"notifications_muted,omitempty"`
}

func (rs *RideSettings) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	
	return json.Unmarshal(bytes, rs)
}

func (rs RideSettings) Value() (driver.Value, error) {
	return json.Marshal(rs)
}

// Ride struct
type Ride struct {
	BaseModel
	HostUserID    uuid.UUID     `gorm:"not null" json:"host_user_id" valid:"required~Host user ID is required"`
	HostUser      User          `gorm:"foreignKey:HostUserID;references:ID;constraint:OnDelete:SET NULL;" json:"host_user" valid:"-"`
	StartLocation string        `gorm:"size:255;not null;" json:"start_location" valid:"required~Start location is required"`
	EndLocation   string        `gorm:"size:255;not null;" json:"end_location" valid:"required~End location is required"`
	StartTime     time.Time     `gorm:"not null;" json:"start_time" valid:"required~Start time is required"`
	TotalSeats    uint          `gorm:"not null;" json:"total_seats" valid:"required~Total seats is required"`
	BookedSeats   uint          `gorm:"not null;" json:"booked_seats" valid:"required~Booked seats is required"`
	TotalPrice    uint          `gorm:"not null;" json:"total_price" valid:"required~Total price is required"`
	IsOngoing     uint          `gorm:"not null;default:0" json:"is_ongoing"`
	IsSameGender  uint          `gorm:"not null;default:0" json:"is_same_gender"`
	Settings      RideSettings  `gorm:"type:jsonb;default:'{}'" json:"settings"`
}
