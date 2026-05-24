// Package main is the prod entrypoint. Subcommands (`migrate`,
// `migrate-status`, `serve`) all wire to helpers in the `server`
// package so the integration test harness can drive the same route
// table without re-implementing it. Anything beyond arg dispatch +
// process-level init (Firebase, FCM, websocket hub, /health) lives
// here; everything route-table-shaped lives in `server/`.
package main

import (
	"log"
	"os"
	"time"

	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/migsource"
	"unipool-backend/routes/chat"
	"unipool-backend/routes/users"
	"unipool-backend/server"
	"unipool-backend/services"

	"github.com/gofiber/fiber/v2"
)

func main() {
	// Subcommand dispatch. Anything other than `serve` (the default)
	// runs and exits without spinning up Fiber, the websocket hub, or
	// the FCM scheduler.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			if err := database.RunMigrations(migsource.FS); err != nil {
				log.Fatalf("migrate failed: %v", err)
			}
			return
		case "migrate-status":
			if err := database.MigrationStatus(migsource.FS); err != nil {
				log.Fatalf("migrate-status failed: %v", err)
			}
			return
		case "serve":
			// fall through to normal startup
		default:
			log.Fatalf("unknown subcommand %q (expected: serve, migrate, migrate-status)", os.Args[1])
		}
	}

	initializer.InitFirebase()

	if err := services.InitFCMService(); err != nil {
		log.Fatalf("Failed to initialize FCM service: %v", err)
	}
	services.InitNotificationScheduler()
	initializer.InitializeWebsocket()

	// Bridge the WebSocket chat persist path into the FCM fan-out
	// helpers in routes/chat. Without this wire-up, every chat/DM
	// message persists silently with zero push notifications (the
	// existing HTTP /chat/:id/message + /dm/:id/message endpoints
	// have fan-out, but the frontend sends every message over WS,
	// so the HTTP path is unreachable for real user traffic).
	initializer.OnChatMessagePersisted = chat.NotifyAfterPersistedChatMessage

	app := server.NewApp()
	server.SetupMiddleware(app)
	database.ConnectToDB()

	// Idempotent — guarantees the launch institutes (e.g. VIT) exist
	// in the institutes / institute_domains tables, so a user signing
	// in with a known student-email domain auto-resolves to a
	// verified profile on first login.
	users.SeedDefaultInstitutes()

	// Health probe sits in main rather than server.WireRoutes so the
	// binary owns its own liveness surface independent of route
	// wiring concerns. Tests don't need it.
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

	server.WireRoutes(app, server.ProdAuth, server.ProdOptionalAuth)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Fatal(app.Listen(":" + port))
}
