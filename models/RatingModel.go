package models

import (
	"github.com/google/uuid"
)

// RideRating is one user's post-trip rating of another user for a
// specific ride. The same ride generates multiple rating rows: the
// host rates each passenger they actually carried, and each
// passenger rates the host. Passengers do NOT rate each other.
//
// Constraint: one row per (ride_id, rater_user_id, rated_user_id)
// — prevents double-rating and lets the rater amend by re-POSTing
// (handler does upsert). Indexed on rater for the "what do I still
// need to rate?" lookup that powers the in-app prompt + 12-hour
// notification.
type RideRating struct {
	BaseModel

	RideID       uuid.UUID `gorm:"type:uuid;not null;index:idx_ratings_ride;uniqueIndex:idx_ratings_ride_pair" json:"ride_id"`
	RaterUserID  uuid.UUID `gorm:"type:uuid;not null;index:idx_ratings_rater;uniqueIndex:idx_ratings_ride_pair" json:"rater_user_id"`
	RatedUserID  uuid.UUID `gorm:"type:uuid;not null;index:idx_ratings_rated;uniqueIndex:idx_ratings_ride_pair" json:"rated_user_id"`

	// 1-5. We render as stars on the client but store the raw int.
	Stars int `gorm:"not null" json:"stars"`

	// Optional one-line note from rater. Capped short so the form
	// stays "quick" — a paragraph of feedback would slow the 5-second
	// flow.
	Comment string `gorm:"type:varchar(240)" json:"comment,omitempty"`
}
