package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type RideSettings struct {
	ChatName           string `json:"chat_name,omitempty"`
	NotificationsMuted bool   `json:"notifications_muted,omitempty"`
}

func (rs *RideSettings) Scan(value interface{}) error {
	if value == nil {
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
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
	HostUserID     uuid.UUID    `gorm:"not null" json:"host_user_id" valid:"required~Host user ID is required"`
	HostUser       User         `gorm:"foreignKey:HostUserID;references:ID;constraint:OnDelete:CASCADE;" json:"host_user" valid:"-"`
	StartLocation  string       `gorm:"size:255;not null;" json:"start_location" valid:"required~Start location is required"`
	EndLocation    string       `gorm:"size:255;not null;" json:"end_location" valid:"required~End location is required"`
	StartLatitude  *float64     `gorm:"type:decimal(10,8);" json:"start_latitude,omitempty"`
	StartLongitude *float64     `gorm:"type:decimal(11,8);" json:"start_longitude,omitempty"`
	EndLatitude    *float64     `gorm:"type:decimal(10,8);" json:"end_latitude,omitempty"`
	EndLongitude   *float64     `gorm:"type:decimal(11,8);" json:"end_longitude,omitempty"`
	StartTime      time.Time    `gorm:"not null;" json:"start_time" valid:"required~Start time is required"`
	TotalSeats     uint         `gorm:"not null;" json:"total_seats" valid:"required~Total seats is required"`
	BookedSeats    uint         `gorm:"not null;" json:"booked_seats" valid:"required~Booked seats is required"`
	TotalPrice     uint         `gorm:"not null;" json:"total_price" valid:"required~Total price is required"`
	IsOngoing      uint         `gorm:"not null;default:0" json:"is_ongoing"`
	IsSameGender   uint         `gorm:"not null;default:0" json:"is_same_gender"`
	Settings       RideSettings `gorm:"type:jsonb;default:'{}'" json:"settings"`

	// Pickup-time vehicle ID. Free-text so the host can describe
	// the car however reads best — "Black Honda City, plate ends
	// 4321" or "Silver Activa scooter, sticker on the back".
	// Surfaced once the booking is accepted so passengers know
	// what to look for at the pickup point. Not gated by any
	// verification — it's a free safety/usability signal.
	VehicleInfo string `gorm:"type:varchar(200)" json:"vehicle_info,omitempty"`

	// Cooldown stamp for the "ride was updated" push. The
	// /ride/update handler skips fan-out if this is within the
	// last 5 minutes (host editing a typo three times in two
	// minutes shouldn't ping every passenger three times), and
	// stamps it on every successful fan-out. Migration:
	// 00006_email_dedup_rework.sql.
	UpdateNotifLastSentAt *time.Time `gorm:"column:update_notif_last_sent_at" json:"-"`
}

// TripTodayEmailSent records that a specific (ride, user) pair has
// already been emailed about the day-before trip. Replaces the
// earlier rides.trip_today_email_sent_at single-marker approach,
// which couldn't handle late-accepted passengers (a passenger
// accepted after the host's email already went out would be
// silently skipped on every subsequent tick).
//
// The PK is the dedup lease: scheduler INSERTs with ON CONFLICT
// DO NOTHING; a 1-row RowsAffected means we won the race and
// should send, 0 means someone (another tick / a previous run)
// already sent. See services/notification_scheduler.go.
type TripTodayEmailSent struct {
	RideID uuid.UUID `gorm:"primaryKey;column:ride_id"`
	UserID uuid.UUID `gorm:"primaryKey;column:user_id"`
	SentAt time.Time `gorm:"column:sent_at;autoCreateTime"`
}

// TableName pins the GORM name to match the migration above. Without
// this GORM would pluralise "trip_today_email_sents" with a typo'd
// ending. Explicit > implicit for cross-language ORMs.
func (TripTodayEmailSent) TableName() string { return "trip_today_emails_sent" }
