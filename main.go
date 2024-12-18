package main

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/middleware"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func SetupRoutes(app *fiber.App) {
	app.Use(cors.New())
	// idhar likho
}
func main() {
	initializer.InitFirebase()
	app := fiber.New()

	database.ConnectToDB()

	app.Use(middleware.Authenticate)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Scared of Women✌️!")
	})

	SetupRoutes(app)
	log.Fatal(app.Listen(":3000"))
}
