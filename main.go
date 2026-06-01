// Package main is the production entrypoint. CLI subcommands delegate to the
// server and database packages so tests can reuse the same route wiring.
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
	// Migration subcommands run and exit without starting Fiber.
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
			// Continue to normal startup.
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

	// WebSocket chat sends persist outside the HTTP handlers, so wire their
	// persisted-message hook into the same notification fanout.
	initializer.OnChatMessagePersisted = chat.NotifyAfterPersistedChatMessage

	app := server.NewApp()
	server.SetupMiddleware(app)
	database.ConnectToDB()

	// Idempotently seed launch institutes and their verified email domains.
	users.SeedDefaultInstitutes()

	// Keep liveness separate from the application route table.
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
