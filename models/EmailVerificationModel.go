package models

import (
	"time"

	"github.com/google/uuid"
)

// EmailVerification holds one pending institute-email ownership challenge.
// Codes are hashed, short-lived, attempt-limited, and consumed on success.
type EmailVerification struct {
	BaseModel

	// Who is trying to prove ownership.
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`

	// The target address being verified — usually NOT the user's
	// Firebase email. Indexed so we can look up "is there a pending
	// challenge for this email right now?" cheaply.
	Email string `gorm:"type:varchar(120);not null;index" json:"email"`

	// SHA-256(code + per-row salt) so a DB dump doesn't leak active
	// codes. Plaintext lives only in the SES outbox + the user's
	// inbox.
	CodeHash string `gorm:"type:varchar(64);not null" json:"-"`
	Salt     string `gorm:"type:varchar(32);not null" json:"-"`

	// Single-use magic-link token. Generated alongside the code so
	// the user can either type the code or tap a `unipool://verify`
	// deeplink in the email — same row consumed either way. Indexed
	// because the link-confirm path looks up by token only.
	Token string `gorm:"type:varchar(64);not null;uniqueIndex:idx_email_verifications_token;column:token" json:"-"`

	// 10-minute expiry from creation. Past this, /verify/confirm
	// rejects the code regardless of correctness.
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`

	// Wrong-code submissions. After 5 we mark the row dead.
	Attempts int `gorm:"default:0" json:"attempts"`

	// Set when the user successfully verifies — prevents reuse of
	// the same code if a row is leaked.
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}
