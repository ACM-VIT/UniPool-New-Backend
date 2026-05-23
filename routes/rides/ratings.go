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
)

// Ratings open up 12h after a ride's scheduled start time. Earlier
// is too soon to know how the trip went; later means the memory's
// stale. Direct rating stays open until 30 days post-trip so people
// who open a specific old trip can still leave feedback, but the
// persistent "rate this trip" prompt is capped at 7 days so one
// missing counterpart does not pin a stale pill for a month.
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
// Individual comments stay private for now; surfaces only need the
// average + count to show useful trust context without leaking detail.
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
		database.Database.Db.WithContext(ctx).
			Table("bookings").
			Select("users.id, users.name, users.profile_picture_url").
			Joins("JOIN users ON users.id = bookings.passenger_id").
			Where("bookings.ride_id = ? AND bookings.request_status = ?", rideID, "accepted").
			Scan(&rows)
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

	// All rides the user was involved in (host OR accepted
	// passenger) that fall inside the rating window. Only the five
	// fields the response actually emits are pulled — skipping the
	// fat Settings JSONB and the four decimal lat/lng columns saves
	// ~30-40% of the wire payload on this hot helper.
	type rideRow struct {
		ID            uuid.UUID `gorm:"column:id"`
		StartLocation string    `gorm:"column:start_location"`
		EndLocation   string    `gorm:"column:end_location"`
		StartTime     time.Time `gorm:"column:start_time"`
		HostUserID    uuid.UUID `gorm:"column:host_user_id"`
	}
	var rides []rideRow
	if err := database.Database.Db.WithContext(ctx).
		Table("rides").
		Select("id, start_location, end_location, start_time, host_user_id").
		Where(`
			start_time BETWEEN ? AND ?
			AND (
				host_user_id = ?
				OR id IN (
				SELECT ride_id FROM bookings
					WHERE passenger_id = ? AND request_status = 'accepted'
				)
			)
		`, windowFloor, windowCeil, userID, userID).
		Order("start_time DESC").
		Scan(&rides).Error; err != nil {
		return nil, err
	}
	if len(rides) == 0 {
		return []PendingRatingRide{}, nil
	}

	// Partition rides into "I was the host" vs "I was a passenger" so
	// we can resolve each group's counterparts with ONE batched query
	// instead of a per-ride round-trip. Pre-fix this was a 2N+1
	// pattern: per-ride lookup of accepted passengers (host case) +
	// per-ride count of already-rated rows. With ~5 rides in the
	// window that was ~10 sequential round-trips at ~25ms each
	// (~250ms on /app/state's hot path); the batched form is two
	// round-trips total regardless of ride count.
	hostedRideIDs := make([]uuid.UUID, 0, len(rides))
	rideIDs := make([]uuid.UUID, 0, len(rides))
	for _, r := range rides {
		rideIDs = append(rideIDs, r.ID)
		if r.HostUserID == userID {
			hostedRideIDs = append(hostedRideIDs, r.ID)
		}
	}

	// 1) For rides I hosted: who's my accepted-passenger counterpart
	//    set, grouped by ride.
	passengersByRide := make(map[uuid.UUID][]uuid.UUID, len(hostedRideIDs))
	if len(hostedRideIDs) > 0 {
		type bookingRow struct {
			RideID      uuid.UUID `gorm:"column:ride_id"`
			PassengerID uuid.UUID `gorm:"column:passenger_id"`
		}
		var bookings []bookingRow
		if err := database.Database.Db.WithContext(ctx).
			Table("bookings").
			Select("ride_id, passenger_id").
			Where("ride_id IN ? AND request_status = ?", hostedRideIDs, "accepted").
			Scan(&bookings).Error; err != nil {
			return nil, err
		}
		for _, b := range bookings {
			passengersByRide[b.RideID] = append(passengersByRide[b.RideID], b.PassengerID)
		}
	}

	// 2) Already-rated counts per ride for THIS user, in one shot.
	ratedCountByRide := make(map[uuid.UUID]int, len(rideIDs))
	{
		type cntRow struct {
			RideID uuid.UUID `gorm:"column:ride_id"`
			Cnt    int       `gorm:"column:cnt"`
		}
		var rows []cntRow
		if err := database.Database.Db.WithContext(ctx).
			Table("ride_ratings").
			Select("ride_id, COUNT(*) AS cnt").
			Where("ride_id IN ? AND rater_user_id = ?", rideIDs, userID).
			Group("ride_id").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			ratedCountByRide[r.RideID] = r.Cnt
		}
	}

	out := make([]PendingRatingRide, 0, len(rides))
	for _, r := range rides {
		var targets []uuid.UUID
		if r.HostUserID == userID {
			targets = passengersByRide[r.ID]
		} else {
			targets = []uuid.UUID{r.HostUserID}
		}
		if len(targets) == 0 {
			continue
		}
		pending := len(targets) - ratedCountByRide[r.ID]
		if pending <= 0 {
			continue
		}
		out = append(out, PendingRatingRide{
			RideID:        r.ID,
			StartLocation: r.StartLocation,
			EndLocation:   r.EndLocation,
			StartTime:     r.StartTime,
			PendingCount:  pending,
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

	saved := 0
	for _, item := range body.Ratings {
		comment := strings.TrimSpace(item.Comment)
		if len(comment) > 240 {
			comment = comment[:240]
		}

		// Upsert on (ride_id, rater_user_id, rated_user_id).
		existing := models.RideRating{}
		err := database.Database.Db.WithContext(ctx).
			Where("ride_id = ? AND rater_user_id = ? AND rated_user_id = ?", rideID, user.ID, item.RatedUserID).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row := models.RideRating{
				RideID:      rideID,
				RaterUserID: user.ID,
				RatedUserID: item.RatedUserID,
				Stars:       item.Stars,
				Comment:     comment,
			}
			if err := database.Database.Db.WithContext(ctx).Create(&row).Error; err != nil {
				log.Printf("SubmitRideRating: insert failed: %v", err)
				continue
			}
			saved++
		} else if err == nil {
			if err := database.Database.Db.WithContext(ctx).
				Model(&existing).
				Updates(map[string]any{"stars": item.Stars, "comment": comment}).Error; err != nil {
				log.Printf("SubmitRideRating: update failed: %v", err)
				continue
			}
			saved++
		}
	}

	return c.JSON(fiber.Map{"saved": saved})
}
