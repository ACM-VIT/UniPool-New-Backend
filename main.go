package main

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/middleware"
	"unipool-backend/routes"
	"unipool-backend/routes/bookings"
	"unipool-backend/routes/users"

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
	app.Post("/user", users.CreateOrUpdateUser)    // Create or update a user
	app.Get("/user/details", users.GetUser)        // Gets user details
	app.Get("/user/all", users.GetAllUsers)        // Gets all users
	app.Delete("/user/delete", users.DeleteUser)   // Deletes a user
	app.Get(("/user/rides"), users.FetchUserRides) //Gets all the rides of a particular user

	//Booking CRUD routes
	app.Post("/booking/create", routes.CreateBooking)            // Creates a new booking
	app.Get("/booking/list", routes.GetBookings)                 // Retrieves all bookings
	app.Get("/booking/:id", routes.GetBookingByID)               // Retrieves a specific booking by its ID
	app.Patch("/booking/update/:id", routes.UpdateBooking)       // Updates an existing booking
	app.Delete("/booking/delete/:id", routes.DeleteBooking)      // Deletes a booking
	app.Put("/bookings/accept/:bookingID", bookings.AcceptRoute) // Accepts a booking
	app.Post("/bookings/request", bookings.Request)              // Requests a booking aka Create a booking

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
