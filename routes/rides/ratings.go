package rides

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Ratings open 12h after a ride starts. Direct rating remains available for
// 30 days, while persistent prompts disappear after 7 days.
const (
	ratingEligibleAfter = 12 * time.Hour
	ratingEligibleUntil = 30 * 24 * time.Hour
	ratingPromptUntil   = 7 * 24 * time.Hour
)

type pendingRateTarget struct {
	UserID            uuid.UUID `json:"user_id"`
	Name              string    `json:"name"`
	ProfilePictureURL string    `json:"profile_picture_url,omitempty"`
	Role              string    `json:"role"` // "host" or "passenger" (the relationship of the rated user to the rater)
}

type ratingEligibilityResponse struct {
	RideID        uuid.UUID           `json:"ride_id"`
	StartTime     time.Time           `json:"start_time"`
	StartLocation string              `json:"start_location"`
	EndLocation   string              `json:"end_location"`
	Eligible      bool                `json:"eligible"`
	OpensAt       time.Time           `json:"opens_at"`
	Targets       []pendingRateTarget `json:"targets"`
}

type ratingSummaryResponse struct {
	UserID  uuid.UUID `json:"user_id"`
	Average *float64  `json:"average"`
	Count   int64     `json:"count"`
}

type PendingRatingRide struct {
	RideID        uuid.UUID `json:"ride_id"`
	StartLocation string    `json:"start_location"`
	EndLocation   string    `json:"end_location"`
	StartTime     time.Time `json:"start_time"`
	PendingCount  int       `json:"pending_count"`
}

// GetUserRatingSummary returns the public aggregate for one user.
// Individual comments stay private; public surfaces only need average + count.
func GetUserRatingSummary(c *fiber.Ctx) error {
	userID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid user id"})
	}

	var summary struct {
		Average *float64
		Count   int64
	}
	if err := database.Database.Db.
		Model(&models.RideRating{}).
		Select("AVG(stars)::float8 AS average, COUNT(*) AS count").
		Where("rated_user_id = ?", userID).
		Scan(&summary).Error; err != nil {
		log.Printf("GetUserRatingSummary failed for user %s: %v", userID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "rating lookup failed"})
	}

	return c.JSON(ratingSummaryResponse{
		UserID:  userID,
		Average: summary.Average,
		Count:   summary.Count,
	})
}

// GetRideRatingEligibility tells the client whether the calling
// user can rate someone on this ride, and if so, who. Drives both
// the 12h-after-trip in-app prompt and the post-trip rating screen.
//
// Returns:
//
//	{ eligible: true,  targets: [<who to rate>] }  if the trip is
//	  done, the 12h window has opened, the user actually participated
//	  (host with carried passengers, or accepted passenger), and they
//	  have at least one unrated counterpart on this trip.
//	{ eligible: false, opens_at: <ts> } otherwise — `opens_at` lets
//	  the client schedule a local-notification or hide the surface.
func GetRideRatingEligibility(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	rideID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var ride models.Ride
	if err := database.Database.Db.WithContext(ctx).
		Select("id, start_time, start_location, end_location, host_user_id").
		Where("id = ?", rideID).
		First(&ride).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "ride not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}

	opensAt := ride.StartTime.Add(ratingEligibleAfter)
	expiresAt := ride.StartTime.Add(ratingEligibleUntil)
	now := time.Now()

	resp := ratingEligibilityResponse{
		RideID:        ride.ID,
		StartTime:     ride.StartTime,
		StartLocation: ride.StartLocation,
		EndLocation:   ride.EndLocation,
		OpensAt:       opensAt,
		Targets:       []pendingRateTarget{},
	}

	if now.Before(opensAt) || now.After(expiresAt) {
		return c.JSON(resp)
	}

	// Determine whether the caller was the host or a confirmed
	// passenger. Anything else (pending booking, rejected, no
	// involvement) is ineligible.
	isHost := ride.HostUserID == user.ID

	var callerWasAccepted bool
	if !isHost {
		var count int64
		database.Database.Db.WithContext(ctx).
			Model(&models.Booking{}).
			Where("ride_id = ? AND passenger_id = ? AND request_status = ?", rideID, user.ID, "accepted").
			Count(&count)
		callerWasAccepted = count > 0
	}
	if !isHost && !callerWasAccepted {
		return c.JSON(resp)
	}

	// Collect candidate targets: host → all accepted passengers;
	// accepted passenger → the host (single target).
	type candidate struct {
		ID                uuid.UUID
		Name              string
		ProfilePictureURL string
		Role              string
	}
	var candidates []candidate

	if isHost {
		type row struct {
			ID                uuid.UUID
			Name              string
			ProfilePictureURL string
		}
		var rows []row
		if err := database.Database.Db.WithContext(ctx).
			Table("bookings").
			Select("users.id, users.name, users.profile_picture_url").
			Joins("JOIN users ON users.id = bookings.passenger_id").
			Where("bookings.ride_id = ? AND bookings.request_status = ?", rideID, "accepted").
			Scan(&rows).Error; err != nil {
			log.Printf("GetRideRatingEligibility: accepted passengers query failed: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
		}
		for _, r := range rows {
			// Mirror SubmitRideRating's allowedTargets exclusion. Test
			// users (and the occasional real one who taps "request" on
			// their own ride before realising it) can end up as both
			// host and passenger on the same trip; without this guard
			// the eligibility endpoint surfaces them as their own
			// rating target, the rating screen renders a card, and then
			// the submit endpoint rejects with "no valid rating targets"
			// because it filters self out. Filtering at the eligibility
			// layer means the screen just shows "All set" instead.
			if r.ID == user.ID {
				continue
			}
			candidates = append(candidates, candidate{
				ID:                r.ID,
				Name:              r.Name,
				ProfilePictureURL: r.ProfilePictureURL,
				Role:              "passenger",
			})
		}
	} else {
		var host models.User
		if err := database.Database.Db.WithContext(ctx).
			Select("id, name, profile_picture_url").
			Where("id = ?", ride.HostUserID).
			First(&host).Error; err == nil {
			candidates = append(candidates, candidate{
				ID:                host.ID,
				Name:              host.Name,
				ProfilePictureURL: host.ProfilePictureURL,
				Role:              "host",
			})
		}
	}
	if len(candidates) == 0 {
		return c.JSON(resp)
	}

	// Filter out anyone the caller has already rated for this ride.
	candidateIDs := make([]uuid.UUID, 0, len(candidates))
	for _, c := range candidates {
		candidateIDs = append(candidateIDs, c.ID)
	}
	var alreadyRated []models.RideRating
	database.Database.Db.WithContext(ctx).
		Select("rated_user_id").
		Where("ride_id = ? AND rater_user_id = ? AND rated_user_id IN ?", rideID, user.ID, candidateIDs).
		Find(&alreadyRated)
	rated := map[uuid.UUID]bool{}
	for _, r := range alreadyRated {
		rated[r.RatedUserID] = true
	}

	for _, cand := range candidates {
		if rated[cand.ID] {
			continue
		}
		resp.Targets = append(resp.Targets, pendingRateTarget{
			UserID:            cand.ID,
			Name:              cand.Name,
			ProfilePictureURL: cand.ProfilePictureURL,
			Role:              cand.Role,
		})
	}
	resp.Eligible = len(resp.Targets) > 0
	return c.JSON(resp)
}

// GetPendingRatings is the aggregate "do I have any rating prompts
// waiting for me?" lookup. Powers the in-app prompt that fires on
// HomeScreen focus and the badge surface on the Trip History page.
//
// Returns the user's rides that are inside the prompt window (12h
// past start, < 7 days) AND where they participated AND haven't
// rated at least one counterpart yet.
//
// Response:
//
//	{ "rides": [ { "ride_id":"…","start_location":"…","end_location":"…","start_time":"…","pending_count":3 }, … ] }
//
// `pending_count` = how many people they still need to rate on
// that trip; useful for "3 ratings left" copy.
func GetPendingRatings(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	out, err := BuildPendingRatings(user.ID)
	if err != nil {
		log.Printf("GetPendingRatings: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}

	return c.JSON(fiber.Map{"rides": out})
}

func BuildPendingRatings(userID uuid.UUID) ([]PendingRatingRide, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	now := time.Now()
	windowFloor := now.Add(-ratingPromptUntil)  // earliest start_time we still prompt for
	windowCeil := now.Add(-ratingEligibleAfter) // latest start_time that has opened the window

	// Single set query for the /app/state hot path. Pre-fix this did
	// three DB statements: candidate rides, hosted-ride passenger
	// targets, and already-rated counts. The CTE form lets the
	// planner use the host and passenger booking indexes separately,
	// expands counterpart targets, filters already-rated pairs, then
	// returns the same pending_count per ride in one round-trip.
	type pendingRow struct {
		RideID        uuid.UUID `gorm:"column:ride_id"`
		StartLocation string    `gorm:"column:start_location"`
		EndLocation   string    `gorm:"column:end_location"`
		StartTime     time.Time `gorm:"column:start_time"`
		PendingCount  int       `gorm:"column:pending_count"`
	}
	var rows []pendingRow
	if err := database.Database.Db.WithContext(ctx).Raw(`
		WITH viewer_rides AS (
			SELECT
				r.id AS ride_id,
				r.start_location,
				r.end_location,
				r.start_time,
				r.host_user_id
			  FROM rides r
			 WHERE r.host_user_id = ?
			   AND r.deleted_at IS NULL
			   AND r.start_time BETWEEN ? AND ?

			UNION

			SELECT
				r.id AS ride_id,
				r.start_location,
				r.end_location,
				r.start_time,
				r.host_user_id
			  FROM bookings b
			  JOIN rides r ON r.id = b.ride_id
			 WHERE b.passenger_id = ?
			   AND b.request_status = 'accepted'
			   AND b.deleted_at IS NULL
			   AND r.deleted_at IS NULL
			   AND r.start_time BETWEEN ? AND ?
		),
		rating_targets AS (
			SELECT
				vr.ride_id,
				vr.start_location,
				vr.end_location,
				vr.start_time,
				b.passenger_id AS target_user_id
			  FROM viewer_rides vr
			  JOIN bookings b ON b.ride_id = vr.ride_id
			 WHERE vr.host_user_id = ?
			   AND b.request_status = 'accepted'
			   AND b.deleted_at IS NULL

			UNION ALL

			SELECT
				vr.ride_id,
				vr.start_location,
				vr.end_location,
				vr.start_time,
				vr.host_user_id AS target_user_id
			  FROM viewer_rides vr
			 WHERE vr.host_user_id <> ?
		),
		unrated_targets AS (
			SELECT
				t.ride_id,
				t.start_location,
				t.end_location,
				t.start_time,
				t.target_user_id
			  FROM rating_targets t
			  LEFT JOIN ride_ratings rr
			    ON rr.ride_id = t.ride_id
			   AND rr.rater_user_id = ?
			   AND rr.rated_user_id = t.target_user_id
			 WHERE rr.id IS NULL
		)
		SELECT
			ride_id,
			start_location,
			end_location,
			start_time,
			COUNT(*)::int AS pending_count
		  FROM unrated_targets
		 GROUP BY ride_id, start_location, end_location, start_time
		 ORDER BY start_time DESC
	`, userID, windowFloor, windowCeil, userID, windowFloor, windowCeil, userID, userID, userID).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]PendingRatingRide, 0, len(rows))
	for _, r := range rows {
		out = append(out, PendingRatingRide{
			RideID:        r.RideID,
			StartLocation: r.StartLocation,
			EndLocation:   r.EndLocation,
			StartTime:     r.StartTime,
			PendingCount:  r.PendingCount,
		})
	}
	return out, nil
}

func BuildPendingRatingSet(userID uuid.UUID) (map[uuid.UUID]bool, error) {
	pending, err := BuildPendingRatings(userID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(pending))
	for _, r := range pending {
		out[r.RideID] = true
	}
	return out, nil
}

type submitRatingItem struct {
	RatedUserID uuid.UUID `json:"rated_user_id"`
	Stars       int       `json:"stars"`
	Comment     string    `json:"comment,omitempty"`
}

// SubmitRideRating accepts a batch of {rated_user_id, stars, comment}
// from the post-trip rating screen. Upserts each one so the same
// payload can be re-sent idempotently (e.g. flaky network on submit).
//
// Body:
//
//	{ "ratings": [ {"rated_user_id":"...","stars":5,"comment":"..."}, ... ] }
func SubmitRideRating(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	rideID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}

	var body struct {
		Ratings []submitRatingItem `json:"ratings"`
	}
	if err := c.BodyParser(&body); err != nil || len(body.Ratings) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing or invalid ratings array"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Re-verify eligibility window, participation, and target set
	// before persisting. The client is allowed to submit only the
	// caller's real counterpart(s): host -> accepted passengers, or
	// accepted passenger -> host.
	var ride models.Ride
	if err := database.Database.Db.WithContext(ctx).
		Select("id, start_time, host_user_id").
		Where("id = ?", rideID).
		First(&ride).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "ride not found"})
	}
	now := time.Now()
	if now.Before(ride.StartTime.Add(ratingEligibleAfter)) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "rating opens 12 hours after the trip starts"})
	}
	if now.After(ride.StartTime.Add(ratingEligibleUntil)) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "rating window has closed for this trip"})
	}

	isHost := ride.HostUserID == user.ID
	allowedTargets := map[uuid.UUID]struct{}{}
	if !isHost {
		var count int64
		database.Database.Db.WithContext(ctx).
			Model(&models.Booking{}).
			Where("ride_id = ? AND passenger_id = ? AND request_status = ?", rideID, user.ID, "accepted").
			Count(&count)
		if count == 0 {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "you weren't on this trip"})
		}
		allowedTargets[ride.HostUserID] = struct{}{}
	} else {
		type acceptedPassenger struct {
			PassengerID uuid.UUID
		}
		var passengers []acceptedPassenger
		if err := database.Database.Db.WithContext(ctx).
			Model(&models.Booking{}).
			Select("passenger_id").
			Where("ride_id = ? AND request_status = ?", rideID, "accepted").
			Find(&passengers).Error; err != nil {
			log.Printf("SubmitRideRating: accepted passengers query failed: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
		}
		for _, passenger := range passengers {
			if passenger.PassengerID != user.ID {
				allowedTargets[passenger.PassengerID] = struct{}{}
			}
		}
	}
	if len(allowedTargets) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no valid rating targets for this trip"})
	}

	for _, item := range body.Ratings {
		if item.Stars < 1 || item.Stars > 5 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "stars must be between 1 and 5"})
		}
		if _, ok := allowedTargets[item.RatedUserID]; !ok {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "rated user was not on this trip"})
		}
	}

	rowsByTarget := make(map[uuid.UUID]models.RideRating, len(body.Ratings))
	targetOrder := make([]uuid.UUID, 0, len(body.Ratings))
	for _, item := range body.Ratings {
		comment := strings.TrimSpace(item.Comment)
		if len(comment) > 240 {
			comment = comment[:240]
		}
		if _, seen := rowsByTarget[item.RatedUserID]; !seen {
			targetOrder = append(targetOrder, item.RatedUserID)
		}
		rowsByTarget[item.RatedUserID] = models.RideRating{
			RideID:      rideID,
			RaterUserID: user.ID,
			RatedUserID: item.RatedUserID,
			Stars:       item.Stars,
			Comment:     comment,
		}
	}

	rows := make([]models.RideRating, 0, len(targetOrder))
	for _, targetID := range targetOrder {
		rows = append(rows, rowsByTarget[targetID])
	}

	if err := database.Database.Db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "ride_id"},
				{Name: "rater_user_id"},
				{Name: "rated_user_id"},
			},
			DoUpdates: clause.Assignments(map[string]any{
				"stars":      gorm.Expr("excluded.stars"),
				"comment":    gorm.Expr("excluded.comment"),
				"updated_at": gorm.Expr("NOW()"),
			}),
		}).
		Create(&rows).Error; err != nil {
		log.Printf("SubmitRideRating: batch upsert failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "save failed"})
	}

	return c.JSON(fiber.Map{"saved": len(rows)})
}
