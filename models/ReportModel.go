package models

import "github.com/google/uuid"

// Report captures a user-submitted report about another user, a chat,
// or a ride. Reasons are kept as free-form strings (e.g. "harassment",
// "spam", "safety") so the product can iterate on categories without
// migrating an enum; the moderation surface decides what to do with
// each report off the server.
type Report struct {
	BaseModel

	// Who filed the report.
	ReporterID uuid.UUID `gorm:"type:uuid;not null;index" json:"reporter_id"`
	Reporter   User      `gorm:"foreignKey:ReporterID" json:"reporter,omitempty"`

	// Who / what is being reported. ReportedUserID is the primary
	// target; RideID is optional context that scopes the report to a
	// specific trip (used when the user reports from a chat).
	ReportedUserID *uuid.UUID `gorm:"type:uuid;index" json:"reported_user_id,omitempty"`
	ReportedUser   *User      `gorm:"foreignKey:ReportedUserID" json:"reported_user,omitempty"`
	RideID         *uuid.UUID `gorm:"type:uuid;index" json:"ride_id,omitempty"`
	ChatRoomID     *string    `gorm:"type:varchar(128);index" json:"chat_room_id,omitempty"`

	// What the user told us.
	Reason  string `gorm:"type:varchar(40);not null" json:"reason"`
	Details string `gorm:"type:text" json:"details,omitempty"`

	// Moderation status — `pending` on create. Other values
	// (`reviewing`, `actioned`, `dismissed`) are set by admin tools.
	Status string `gorm:"type:varchar(20);not null;default:'pending'" json:"status"`
}
