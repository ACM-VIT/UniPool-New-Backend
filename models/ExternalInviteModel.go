package models

import (
	"time"

	"github.com/google/uuid"
)

// ExternalInviteSent records that a user has already emailed a given external
// ride host ("wants to ride with you"). The composite primary key is the dedup
// lease: one invite per (inviter, external ride). The handler INSERTs with
// ON CONFLICT DO NOTHING so a repeat tap never sends a second email.
type ExternalInviteSent struct {
	InviterUserID  uuid.UUID `gorm:"primaryKey;column:inviter_user_id"`
	ExternalRideID string    `gorm:"primaryKey;column:external_ride_id"`
	SentAt         time.Time `gorm:"column:sent_at;autoCreateTime"`
}

// TableName pins the GORM name to match the migration.
func (ExternalInviteSent) TableName() string { return "external_invites_sent" }
