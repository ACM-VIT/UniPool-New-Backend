// Package server owns Fiber route registration and the middleware
// chain. main.go is a thin wrapper that calls Run; integration tests
// import this package to wire the same route table with header-based
// test middleware in place of Firebase auth.
package server

import (
	"log"
	"time"

	"unipool-backend/middleware"
	"unipool-backend/routes/CRUD"
	"unipool-backend/routes/appstate"
	"unipool-backend/routes/bookings"
	"unipool-backend/routes/chat"
	"unipool-backend/routes/locations"
	"unipool-backend/routes/notifications"
	"unipool-backend/routes/ota"
	"unipool-backend/routes/reports"
	"unipool-backend/routes/rides"
	"unipool-backend/routes/users"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
)

// SetupMiddleware installs the production middleware chain.
func SetupMiddleware(app *fiber.App) {
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))

	app.Use(cors.New(cors.Config{
		AllowOrigins:     "*",
		AllowMethods:     "GET,POST,HEAD,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Requested-With",
		AllowCredentials: false,
	}))
}

// WireRoutes registers public routes first, then installs the auth gate before
// registering authenticated routes.
func WireRoutes(app *fiber.App, auth fiber.Handler, optionalAuth fiber.Handler) {
	// Public nearby-rides endpoints power the signed-out home map.
	app.Get("/rides/nearby-count", rides.NearbyRidesCount)
	app.Get("/rides/nearby", rides.NearbyRides)

	// Optional auth lets guests browse while signed-in users get viewer_state.
	app.Get("/ride/search", optionalAuth, rides.SearchRides)

	// Public share preview exposes a sanitized subset of ride details.
	app.Get("/ride/preview/:id", rides.GetRidePreview)
	app.Get("/locations/search", locations.SearchLocations)

	// Public institute catalogue.
	app.Get("/institutes", users.ListInstitutes)
	app.Get("/institutes/search", users.SearchInstitutes)

	// Public aggregate only: average/count ratings for trust surfaces.
	app.Get("/user/:id/rating-summary", rides.GetUserRatingSummary)

	app.Get("/app/state", optionalAuth, appstate.GetState)

	// CreateRide uses this optional-auth probe to suggest matching rides first.
	app.Get("/ride/matching-create", optionalAuth, rides.MatchingCreate)

	// Expo Updates endpoints run before auth because clients fetch manifests at startup.
	app.Get("/api/manifest", ota.ManifestHandler)
	app.Get("/api/assets", ota.AssetHandler)
	app.Post("/api/ota/upload", ota.UploadHandler)

	// Every route registered after this point requires c.Locals("user").
	app.Use(auth)

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Scared of Women✌️!")
	})

	SetupRoutes(app)
}

// SetupRoutes registers authenticated routes. Production callers should use
// WireRoutes so the auth boundary is installed first.
func SetupRoutes(app *fiber.App) {
	// Ride CRUD routes
	app.Post("/ride/create", rides.CreateRide)
	app.Get("/ride/fetch/:id", CRUD.GetRideByID)
	app.Get("/ride/details/:id", rides.GetRideDetailsComplete)
	app.Get("/ride/all", CRUD.GetRides)
	app.Put("/ride/update/:id", CRUD.UpdateRideByID)
	app.Delete("/ride/delete/:id", CRUD.DeleteRideByID)
	// /ride/search is registered in WireRoutes with optional auth.

	// Ride settings routes
	app.Get("/ride/:ride_id/settings", rides.GetRideSettings)
	app.Put("/ride/:ride_id/settings", rides.UpdateRideSettings)

	// Post-trip ratings. /eligibility tells the client whether the
	// caller can rate someone on this ride yet (12h after start);
	// /rate accepts the batched form submission.
	app.Get("/ride/:id/rating-eligibility", rides.GetRideRatingEligibility)
	app.Post("/ride/:id/rate", rides.SubmitRideRating)
	// Aggregate "what trips still need ratings from me?" — drives
	// both the home-screen prompt on app focus and the trip-history
	// badge counts.
	app.Get("/user/pending-ratings", rides.GetPendingRatings)

	// Notification preferences. /prefs is the global category list
	// (chat / ride-updates / reminders / rating-prompts); per-ride
	// chat mute lives at /ride/:ride_id/chat-mute and is the
	// canonical wiring for the chat-settings sheet's "Mute
	// notifications" toggle.
	app.Get("/user/notification-prefs", users.GetNotificationPreferences)
	app.Put("/user/notification-prefs", users.SetNotificationPreference)
	app.Get("/ride/:ride_id/chat-mute", users.GetRideChatMute)
	app.Put("/ride/:ride_id/chat-mute", users.SetRideChatMute)

	// User routes
	app.Post("/user", users.CreateOrUpdateUser)
	app.Get("/user/details", users.GetUser)
	app.Get("/user/all", users.GetAllUsers)
	app.Get("/user/rides", users.FetchUserRides)
	app.Get("/user/passengers", users.GetPassengers)
	app.Get("/user/default-address", users.GetDefaultAddress)
	app.Post("/user/default-address", users.SetDefaultAddress)
	app.Put("/user/default-address", users.SetDefaultAddress)
	app.Patch("/user/default-address", users.SetDefaultAddress)
	app.Delete("/user/default-address", users.SetDefaultAddress)
	// Self-edit profile fields (UPI VPA, contact number). Name +
	// email + verification status are NOT editable here — those flow
	// from Firebase identity / institute matching.
	app.Patch("/user/profile", users.UpdateProfile)
	// Email verification — proves the signed-in user owns an
	// institute email, even if they signed in with a personal
	// Google account. /start sends a code via SES; /confirm
	// matches it and stamps is_email_verified + institute_id.
	app.Post("/user/verify/start", users.StartEmailVerification)
	app.Post("/user/verify/confirm", users.ConfirmEmailVerification)
	app.Get("/user/:id", users.GetUserByID) // Keep after the specific /user routes.
	app.Delete("/user/delete", users.DeleteUser)

	// User token management routes
	app.Post("/users/me/token", users.UpdateUserToken)
	app.Delete("/users/me/token", users.RemoveUserToken)

	// Booking routes
	app.Post("/booking/create", CRUD.CreateBooking)
	app.Get("/booking/list", CRUD.GetBookings)
	app.Get("/booking/:id", CRUD.GetBookingByID)
	app.Get("/booking/ride/:ride_id", CRUD.GetBookingsByRideID)
	app.Patch("/booking/update/:id", CRUD.UpdateBooking)
	app.Delete("/booking/delete/:id", bookings.DeleteBooking)
	app.Put("/bookings/accept/:bookingID", bookings.AcceptRoute)
	app.Put("/bookings/reject/:bookingID", bookings.RejectRoute)
	app.Post("/bookings/request", bookings.Request)

	// Post-trip card surface for the home screen — one query for the
	// currently relevant trip, and a dismiss endpoint to record the
	// passenger's "I paid" / "didn't happen" signal.
	app.Get("/trip-card/active", bookings.GetActiveTripCard)
	app.Post("/trip-card/dismiss", bookings.DismissTripCard)
	// Host's response to a passenger's payment_marker. See
	// routes/bookings/TripCard.go::PaymentAck for the state machine.
	app.Post("/booking/:id/payment-ack", bookings.PaymentAck)

	// Chat routes
	app.Get("/chats/:user_id", chat.GetUserChats)
	// "me" alias for the chat list — the handler reads the
	// authenticated user from locals, so the URL param is purely
	// informational. Keeps the client URL clean.
	app.Get("/chats/me", chat.GetUserChats)
	app.Get("/chat/:ride_id/messages", chat.GetRideMessages)
	app.Get("/dm/:dm_room_id/messages", chat.GetDMMessages)
	app.Post("/chat/:ride_id/message", chat.SendMessage)
	app.Post("/dm/:dm_room_id/message", chat.SendDMMessage)
	// Mark a ride chat read for the current user. Frontend calls this
	// whenever the chat is opened or returns to the foreground so the
	// unread badge on the chat-list stays accurate.
	app.Post("/chat/:ride_id/read", chat.MarkRideRead)
	// DM-room read marker — same shape as the ride one, used by the
	// host's pending-request thread so the unread badge clears when
	// they open the DM.
	app.Post("/dm/:dm_room_id/read", chat.MarkDMRead)
	app.Get("/chat/connections", chat.GetActiveConnections)

	// Notification routes
	app.Post("/notifications/send", notifications.SendNotification)
	app.Post("/notifications/send-to-user", notifications.SendNotificationToUser)

	// User-submitted moderation reports (chat settings → "Report").
	// Auth-gated by the middleware below; the reporter_id is read from
	// `c.Locals("user")` so a client can't spoof someone else.
	app.Post("/reports", reports.CreateReport)

	// WebSocket upgrade endpoint.
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	app.Get("/ws", websocket.New(chat.WebSocketHandler, websocket.Config{}))

	// Ride and Passenger Info routes
	app.Get("/rides/involved", rides.GetInvolvedRides)
	app.Get("/passengers/all", rides.GetAllPassengers)
	app.Delete("/rides/:ride_id/participants/:user_id", rides.RemoveRideParticipant)
}

// NewApp returns a Fiber app pre-configured with the production
// timeouts, body limit, and error handler. Tests usually want their
// own slimmer config; this helper is the single source of truth for
// the prod knobs.
func NewApp() *fiber.App {
	return fiber.New(fiber.Config{
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
		BodyLimit:    10 * 1024 * 1024,
		Immutable:    true,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			log.Printf("Request error: %v", err)
			return c.Status(code).JSON(fiber.Map{
				"error": err.Error(),
			})
		},
	})
}

// Prod auth bindings are swapped out by tests that need header-based auth.
var (
	ProdAuth         = middleware.Authenticate
	ProdOptionalAuth = middleware.OptionalAuthenticate
)
