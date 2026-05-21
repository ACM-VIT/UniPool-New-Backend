package main

import (
	"log"
	"os"
	"time"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/middleware"
	"unipool-backend/routes/CRUD"
	"unipool-backend/routes/appstate"
	"unipool-backend/routes/bookings"
	"unipool-backend/routes/chat"
	"unipool-backend/routes/locations"
	"unipool-backend/routes/notifications"
	"unipool-backend/routes/reports"
	"unipool-backend/routes/rides"
	"unipool-backend/routes/users"
	"unipool-backend/services"

	"github.com/gofiber/websocket/v2"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func SetupMiddleware(app *fiber.App) {
	// gzip every JSON response. Chat-list / user-rides / ride-search
	// payloads are textually verbose; compression cuts wire bytes
	// 60-75% on average. CPU cost is negligible at our load.
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))

	app.Use(cors.New(cors.Config{
		AllowOrigins:     "*",
		AllowMethods:     "GET,POST,HEAD,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Requested-With",
		AllowCredentials: false,
	}))
}

func SetupRoutes(app *fiber.App) {
	// Ride CRUD routes
	app.Post("/ride/create", rides.CreateRide)                 // Creates a new ride
	app.Get("/ride/fetch/:id", CRUD.GetRideByID)               // Gets details of a ride by it's ID
	app.Get("/ride/details/:id", rides.GetRideDetailsComplete) // Gets complete ride details with bookings and passengers
	app.Get("/ride/all", CRUD.GetRides)                        // Gets all rides
	app.Put("/ride/update/:id", CRUD.UpdateRideByID)           // Updates a ride by it's ID
	app.Delete("/ride/delete/:id", CRUD.DeleteRideByID)        // Deletes a ride by it's ID
	// NOTE: `/ride/search` is now registered above as a public route
	// (with OptionalAuthenticate) so guests can browse the catalogue
	// without an account. Don't re-register it here under the hard
	// auth gate.

	// Ride settings routes
	app.Get("/ride/:ride_id/settings", rides.GetRideSettings)    // Gets ride settings
	app.Put("/ride/:ride_id/settings", rides.UpdateRideSettings) // Updates ride settings

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

	// User CRUD routes
	app.Post("/user", users.CreateOrUpdateUser)                  // Create or update a user
	app.Get("/user/details", users.GetUser)                      // Gets user details
	app.Get("/user/all", users.GetAllUsers)                      // Gets all users
	app.Get("/user/rides", users.FetchUserRides)                 // Gets all the rides of a particular user
	app.Get("/user/passengers", users.GetPassengers)             // Gets all passengers the user has travelled with
	app.Get("/user/default-address", users.GetDefaultAddress)    // Gets user's default start address
	app.Post("/user/default-address", users.SetDefaultAddress)   // Sets user's default start address
	app.Put("/user/default-address", users.SetDefaultAddress)    // PUT also sets / clears user's default start address
	app.Patch("/user/default-address", users.SetDefaultAddress)  // PATCH also sets user's default start address
	app.Delete("/user/default-address", users.SetDefaultAddress) // DELETE clears user's default start address (uses empty payload path)
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
	app.Get("/user/:id", users.GetUserByID)      // Gets user details by ID (must be after specific routes)
	app.Delete("/user/delete", users.DeleteUser) // Deletes a user

	// User token management routes
	app.Post("/users/me/token", users.UpdateUserToken)   // Update user's FCM token
	app.Delete("/users/me/token", users.RemoveUserToken) // Remove user's FCM token

	//Booking CRUD routes
	app.Post("/booking/create", CRUD.CreateBooking)              // Creates a new booking
	app.Get("/booking/list", CRUD.GetBookings)                   // Retrieves all bookings
	app.Get("/booking/:id", CRUD.GetBookingByID)                 // Retrieves a specific booking by its ID
	app.Get("/booking/ride/:ride_id", CRUD.GetBookingsByRideID)  // Retrieves all bookings for a specific ride
	app.Patch("/booking/update/:id", CRUD.UpdateBooking)         // Updates an existing booking
	app.Delete("/booking/delete/:id", bookings.DeleteBooking)    // Deletes a booking
	app.Put("/bookings/accept/:bookingID", bookings.AcceptRoute) // Accepts a booking
	app.Put("/bookings/reject/:bookingID", bookings.RejectRoute) // Rejects a booking
	app.Post("/bookings/request", bookings.Request)              // Requests a booking aka Create a booking

	// Post-trip card surface for the home screen — one query for the
	// currently relevant trip, and a dismiss endpoint to record the
	// passenger's "I paid" / "didn't happen" signal.
	app.Get("/trip-card/active", bookings.GetActiveTripCard)
	app.Post("/trip-card/dismiss", bookings.DismissTripCard)

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
	app.Get("/chat/connections", chat.GetActiveConnections) // Debug

	// Notification routes
	app.Post("/notifications/send", notifications.SendNotification)               // Send FCM notification
	app.Post("/notifications/send-to-user", notifications.SendNotificationToUser) // Send notification to specific user

	// User-submitted moderation reports (chat settings → "Report").
	// Auth-gated by the middleware below; the reporter_id is read from
	// `c.Locals("user")` so a client can't spoof someone else.
	app.Post("/reports", reports.CreateReport)

	// WebSocket endpoint (upgrade)
	app.Use("/ws", func(c *fiber.Ctx) error {
		//log.Println("/ws middleware reached; checking upgrade...")
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

func main() {
	initializer.InitFirebase()

	// Initialize FCM service
	if err := services.InitFCMService(); err != nil {
		log.Fatalf("Failed to initialize FCM service: %v", err)
	}

	// Initialize notification scheduler
	services.InitNotificationScheduler()

	initializer.InitializeWebsocket()

	app := fiber.New(fiber.Config{
		ReadTimeout:  60 * time.Second,  // Increased for WebSocket connections
		WriteTimeout: 60 * time.Second,  // Increased for WebSocket connections
		IdleTimeout:  120 * time.Second, // Increased for long-lived connections
		BodyLimit:    10 * 1024 * 1024,  // 10MB body limit
		Immutable:    true,              // Better performance
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

	SetupMiddleware(app)

	database.ConnectToDB()

	// Run database migrations
	// if err := database.InitializeMigrations(); err != nil {
	// 	log.Fatalf("Failed to run migrations: %v", err)
	// }

	// Idempotent — guarantees the launch institutes (e.g. VIT) exist
	// in the institutes / institute_domains tables, so a user signing
	// in with a known student-email domain auto-resolves to a
	// verified profile on first login.
	users.SeedDefaultInstitutes()

	app.Get("/health", func(c *fiber.Ctx) error {
		sqlDB, err := database.Database.Db.DB()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"status":   "unhealthy",
				"database": "error getting db instance",
				"error":    err.Error(),
			})
		}

		if err := sqlDB.Ping(); err != nil {
			return c.Status(500).JSON(fiber.Map{
				"status":   "unhealthy",
				"database": "ping failed",
				"error":    err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"status":    "healthy",
			"database":  "connected",
			"timestamp": time.Now().UTC(),
		})
	})

	// Public — no auth required. Lets the unauthenticated HomeScreen
	// decide whether the "carpools nearby" pill is worth showing
	// and, with /rides/nearby, plot the actual ride pins on the map.
	app.Get("/rides/nearby-count", rides.NearbyRidesCount)
	app.Get("/rides/nearby", rides.NearbyRides)

	// `/ride/search` is public too: guests need to be able to browse
	// the catalogue before they're nudged to sign in. OptionalAuthenticate
	// populates c.Locals("user") when the caller IS signed in, so
	// signed-in searchers still get their own rides filtered out and
	// per-result viewer_state computed; guests get the same results
	// with viewer_state="available" everywhere.
	app.Get("/ride/search", middleware.OptionalAuthenticate, rides.SearchRides)
	app.Get("/locations/search", locations.SearchLocations)

	// Public institute catalogue — frontend uses this to display the
	// host's school on profile / ride cards without an authed call.
	app.Get("/institutes", users.ListInstitutes)
	// Typeahead picker on the verify-academic-status sheet. Public,
	// case-insensitive LIKE match against name, returns domains
	// inline so the client can validate the email locally.
	app.Get("/institutes/search", users.SearchInstitutes)

	// Public aggregate only: average/count ratings for trust surfaces.
	// Individual comments and rater identities remain private.
	app.Get("/user/:id/rating-summary", rides.GetUserRatingSummary)

	// Bootstrap read model for app startup/home. Optional auth lets
	// guests receive public nearby activity while signed-in users get
	// their user summary, upcoming trips, active trip card, and pending
	// ratings in one cached request.
	app.Get("/app/state", middleware.OptionalAuthenticate, appstate.GetState)

	app.Use(middleware.Authenticate)

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Scared of Women✌️!")
	})

	SetupRoutes(app)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	log.Fatal(app.Listen(":" + port))
}
