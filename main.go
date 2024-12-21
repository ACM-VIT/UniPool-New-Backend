package main

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/middleware"
	"unipool-backend/routes"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func SetupRoutes(app *fiber.App) {
	app.Use(cors.New())

	// Ride CRUD routes
	app.Post("/ride/create", routes.CreateRide)           // Creates a new ride
	app.Get("/ride/fetch/:id", routes.GetRideByID)        // Gets details of a ride by it's ID
	app.Get("/ride/all", routes.GetRides)                 // Gets all rides
	app.Put("/ride/update/:id", routes.UpdateRideByID)    // Updates a ride by it's ID
	app.Delete("/ride/delete/:id", routes.DeleteRideByID) // Deletes a ride by it's ID

	// User CRUD routes
	app.Post("/user", middleware.Authenticate, routes.CreateOrUpdateUser) // Create or update a user
	app.Get("/user/all", routes.GetUsers)          // Gets all users
	app.Get("/user/fetch/:id", routes.GetUserByID) // Gets details of a user by it's ID
	app.Put("/user/update/:id", routes.UpdateUserByID)	// Updates a user by it's ID
	app.Delete("/user/delete/:id", routes.DeleteUserByID) // Deletes a user by it's ID
  app.Get(("/user/:id/rides"), routes.FetchUserRides) //Gets all the rides of a particular user

	//Booking CRUD routes
	app.Post("/booking/create", routes.CreateBooking)      // Creates a new booking
	app.Get("/booking/list", routes.GetBookings)           // Retrieves all bookings
	app.Get("/booking/:id", routes.GetBookingByID)         // Retrieves a specific booking by its ID
	app.Patch("/booking/update/:id", routes.UpdateBooking) // Updates an existing booking
	app.Delete("/booking/delete/:id", routes.DeleteBooking) // Deletes a booking
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
