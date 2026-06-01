package models

import "github.com/google/uuid"

// Report captures a user-submitted moderation report about a user, chat, or ride.
type Report struct {
	BaseModel

	ReporterID uuid.UUID `gorm:"type:uuid;not null;index" json:"reporter_id"`
	Reporter   User      `gorm:"foreignKey:ReporterID" json:"reporter,omitempty"`

	// Optional target context. ReportedUserID is the primary target.
	ReportedUserID *uuid.UUID `gorm:"type:uuid;index" json:"reported_user_id,omitempty"`
	ReportedUser   *User      `gorm:"foreignKey:ReportedUserID" json:"reported_user,omitempty"`
	RideID         *uuid.UUID `gorm:"type:uuid;index" json:"ride_id,omitempty"`
	ChatRoomID     *string    `gorm:"type:varchar(128);index" json:"chat_room_id,omitempty"`

	Reason  string `gorm:"type:varchar(40);not null" json:"reason"`
	Details string `gorm:"type:text" json:"details,omitempty"`

	// Moderation status. New reports start as pending.
	Status string `gorm:"type:varchar(20);not null;default:'pending'" json:"status"`
}
