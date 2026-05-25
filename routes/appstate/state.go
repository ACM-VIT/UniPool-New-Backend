package appstate

import (
	"context"
	"strconv"
	"sync"
	"time"

	"unipool-backend/database"
	"unipool-backend/models"
	"unipool-backend/routes/bookings"
	"unipool-backend/routes/rides"
	"unipool-backend/routes/users"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type HomeState struct {
	UserRides      []users.UserRidesResponse `json:"user_rides"`
	ActiveTripCard *bookings.TripCard        `json:"active_trip_card"`
	PendingRatings []rides.PendingRatingRide `json:"pending_ratings"`
	Nearby         *rides.NearbyRidesPayload `json:"nearby,omitempty"`
}

type StateResponse struct {
	ServerTime    time.Time         `json:"server_time"`
	Authenticated bool              `json:"authenticated"`
	User          fiber.Map         `json:"user,omitempty"`
	Home          HomeState         `json:"home"`
	Errors        map[string]string `json:"errors,omitempty"`
}

func GetState(c *fiber.Ctx) error {
	resp := StateResponse{
		ServerTime: time.Now().UTC(),
		Home: HomeState{
			UserRides:      []users.UserRidesResponse{},
			PendingRatings: []rides.PendingRatingRide{},
		},
	}
	errors := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	addError := func(key string, value string) {
		mu.Lock()
		errors[key] = value
		mu.Unlock()
	}

	if lat, lng, ok, err := parseLocation(c); err != nil {
		addError("nearby", "lat and lng must both be valid numbers")
	} else if ok {
		radius, _ := strconv.ParseFloat(c.Query("radius"), 64)
		limit, _ := strconv.Atoi(c.Query("limit"))
		// Hide the viewer's own rides from the home-map snapshot when
		// we know who they are. /app/state runs the auth middleware
		// in optional mode, so an authenticated caller has a non-nil
		// user in c.Locals; an anonymous caller falls through with
		// an empty filter and sees every ride.
		excludeHostUserID := ""
		if viewer, ok := c.Locals("user").(models.User); ok {
			excludeHostUserID = viewer.ID.String()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			nearby, err := rides.LoadNearbyRides(ctx, lat, lng, radius, limit, excludeHostUserID)
			if err != nil {
				addError("nearby", "nearby rides unavailable")
				return
			}

			mu.Lock()
			resp.Home.Nearby = &nearby
			mu.Unlock()
		}()
	}

	user, ok := c.Locals("user").(models.User)
	if ok {
		resp.Authenticated = true

		wg.Add(4)

		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			userSummary, err := buildUserSummary(ctx, user)
			if err != nil {
				addError("user", "user details unavailable")
				return
			}

			mu.Lock()
			resp.User = userSummary
			mu.Unlock()
		}()

		go func() {
			defer wg.Done()
			if userRides, err := users.BuildUserRides(user.ID, "upcoming"); err != nil {
				addError("user_rides", "user rides unavailable")
			} else {
				mu.Lock()
				resp.Home.UserRides = userRides
				mu.Unlock()
			}
		}()

		go func() {
			defer wg.Done()
			activeTripCard := bookings.BuildActiveTripCard(user.ID)
			mu.Lock()
			resp.Home.ActiveTripCard = activeTripCard
			mu.Unlock()
		}()

		go func() {
			defer wg.Done()
			if pendingRatings, err := rides.BuildPendingRatings(user.ID); err != nil {
				addError("pending_ratings", "pending ratings unavailable")
			} else {
				mu.Lock()
				resp.Home.PendingRatings = pendingRatings
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(errors) > 0 {
		resp.Errors = errors
	}
	return c.JSON(resp)
}

func parseLocation(c *fiber.Ctx) (float64, float64, bool, error) {
	latRaw := c.Query("lat")
	lngRaw := c.Query("lng")
	if latRaw == "" && lngRaw == "" {
		return 0, 0, false, nil
	}
	lat, latErr := strconv.ParseFloat(latRaw, 64)
	lng, lngErr := strconv.ParseFloat(lngRaw, 64)
	if latErr != nil || lngErr != nil {
		if latErr != nil {
			return 0, 0, false, latErr
		}
		return 0, 0, false, lngErr
	}
	return lat, lng, true, nil
}

func buildUserSummary(ctx context.Context, user models.User) (fiber.Map, error) {
	type row struct {
		ID                uuid.UUID
		Name              string
		Email             string
		ProfilePictureURL string
		ContactNumber     string
		Gender            string
		YOB               uint
		DefaultAddress    string
		CreatedAt         time.Time
		UpdatedAt         time.Time
		UPIVPA            string
		IsEmailVerified   bool
		InstituteEmail    string
		InstituteID       *uuid.UUID
		InstituteName     *string
		InstituteCountry  *string
		TotalHostedRides  int64
	}
	var full row
	if err := database.Database.Db.WithContext(ctx).Raw(`
		SELECT
			u.id,
			u.name,
			u.email,
			u.profile_picture_url,
			u.contact_number,
			u.gender,
			u.yob,
			u.default_address,
			u.created_at,
			u.updated_at,
			u.upi_vpa,
			u.is_email_verified,
			u.institute_email,
			u.institute_id,
			i.name AS institute_name,
			i.country AS institute_country,
			(
				SELECT COUNT(*)
				  FROM rides r
				 WHERE r.host_user_id = u.id
				   AND r.deleted_at IS NULL
			) AS total_hosted_rides
		  FROM users u
		  LEFT JOIN institutes i ON i.id = u.institute_id
		 WHERE u.id = ?
		 LIMIT 1
	`, user.ID).Scan(&full).Error; err != nil {
		return nil, err
	}
	if full.ID == (uuid.UUID{}) {
		return nil, gorm.ErrRecordNotFound
	}

	var institute any
	if full.InstituteID != nil {
		institute = fiber.Map{
			"id":      full.InstituteID,
			"name":    full.InstituteName,
			"country": full.InstituteCountry,
		}
	}

	return fiber.Map{
		"id":                  full.ID,
		"name":                full.Name,
		"email":               full.Email,
		"profile_picture_url": full.ProfilePictureURL,
		"contact_number":      full.ContactNumber,
		"gender":              full.Gender,
		"yob":                 full.YOB,
		"default_address":     full.DefaultAddress,
		"created_at":          full.CreatedAt,
		"updated_at":          full.UpdatedAt,
		"total_hosted_rides":  full.TotalHostedRides,
		"upi_vpa":             full.UPIVPA,
		"is_email_verified":   full.IsEmailVerified,
		"institute_email":     full.InstituteEmail,
		"institute":           institute,
		"institute_id":        full.InstituteID,
	}, nil
}
