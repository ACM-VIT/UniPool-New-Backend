package helpers

import (
	"unipool-backend/database"

	"github.com/google/uuid"
)

// Notification category strings are persisted and exchanged with clients.
const (
	NotifChatMessages   = "chat_messages"
	NotifDirectMessages = "direct_messages"
	NotifRideUpdates    = "ride_updates"
	NotifTripReminders  = "trip_reminders"
	NotifRatingPrompts  = "rating_prompts"
	NotifBookingEmails  = "booking_emails"
)

// AllNotifCategories is the settings presentation order.
var AllNotifCategories = []string{
	NotifChatMessages,
	NotifDirectMessages,
	NotifRideUpdates,
	NotifTripReminders,
	NotifRatingPrompts,
	NotifBookingEmails,
}

// FilterAllowedRecipients resolves notification preferences for many users in
// batched queries.
//
// It fails open to match the single-user variant's delivery posture.
func FilterAllowedRecipients(userIDs []uuid.UUID, category string, rideID uuid.UUID) []uuid.UUID {
	if len(userIDs) == 0 || category == "" {
		return userIDs
	}
	// Absence of a preference row means notifications are allowed.
	decision := make(map[uuid.UUID]bool, len(userIDs))
	for _, id := range userIDs {
		decision[id] = true
	}

	type prefRow struct {
		UserID  uuid.UUID `gorm:"column:user_id"`
		Enabled bool      `gorm:"column:enabled"`
	}

	// Global category prefs are the baseline.
	var globals []prefRow
	if err := database.Database.Db.
		Table("notification_preferences").
		Select("user_id, enabled").
		Where("user_id IN ? AND category = ? AND ride_id IS NULL", userIDs, category).
		Scan(&globals).Error; err == nil {
		for _, g := range globals {
			decision[g.UserID] = g.Enabled
		}
	}

	// Per-ride overrides win over global prefs.
	if rideID != uuid.Nil {
		var perRide []prefRow
		if err := database.Database.Db.
			Table("notification_preferences").
			Select("user_id, enabled").
			Where("user_id IN ? AND category = ? AND ride_id = ?", userIDs, category, rideID).
			Scan(&perRide).Error; err == nil {
			for _, p := range perRide {
				decision[p.UserID] = p.Enabled
			}
		}
	}

	allowed := make([]uuid.UUID, 0, len(userIDs))
	for _, id := range userIDs {
		if decision[id] {
			allowed = append(allowed, id)
		}
	}
	return allowed
}

// IsNotificationAllowed resolves one user's preference for a category/ride push.
// Per-ride row wins, then global row, then default allowed.
//
// `rideID` is optional — pass uuid.Nil for non-ride-scoped pushes
// (e.g. account-level notifications), and the per-ride lookup is
// skipped.
//
// DB errors are returned so call sites can choose fail-open or fail-closed.
//
// For multi-recipient fan-outs prefer FilterAllowedRecipients —
// resolves N users in 2 batched queries instead of 2N.
func IsNotificationAllowed(userID uuid.UUID, category string, rideID uuid.UUID) (bool, error) {
	if userID == uuid.Nil || category == "" {
		return true, nil
	}

	if rideID != uuid.Nil {
		var enabled bool
		err := database.Database.Db.Raw(`
			SELECT COALESCE(rp.enabled, gp.enabled, TRUE) AS enabled
			  FROM (SELECT 1) seed
			  LEFT JOIN notification_preferences rp
			    ON rp.user_id = ?
			   AND rp.category = ?
			   AND rp.ride_id = ?
			  LEFT JOIN notification_preferences gp
			    ON gp.user_id = ?
			   AND gp.category = ?
			   AND gp.ride_id IS NULL
			 LIMIT 1
		`, userID, category, rideID, userID, category).Scan(&enabled).Error
		return enabled, err
	}

	var enabled bool
	err := database.Database.Db.Raw(`
		SELECT COALESCE(gp.enabled, TRUE) AS enabled
		  FROM (SELECT 1) seed
		  LEFT JOIN notification_preferences gp
		    ON gp.user_id = ?
		   AND gp.category = ?
		   AND gp.ride_id IS NULL
		 LIMIT 1
	`, userID, category).Scan(&enabled).Error
	return enabled, err
}
