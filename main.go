package main

import (
	"log"
	"os"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/middleware"
	"unipool-backend/routes/CRUD"
	"unipool-backend/routes/bookings"
	"unipool-backend/routes/rides"
	"unipool-backend/routes/chat"
	"github.com/gofiber/websocket/v2"
	"unipool-backend/routes/users"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func SetupRoutes(app *fiber.App) {
	app.Use(cors.New())

	// Ride CRUD routes
	app.Post("/ride/create", rides.CreateRide)          // Creates a new ride
	app.Get("/ride/fetch/:id", CRUD.GetRideByID)        // Gets details of a ride by it's ID
	app.Get("/ride/all", CRUD.GetRides)                 // Gets all rides
	app.Put("/ride/update/:id", CRUD.UpdateRideByID)    // Updates a ride by it's ID
	app.Delete("/ride/delete/:id", CRUD.DeleteRideByID) // Deletes a ride by it's ID
	app.Get("ride/search", rides.SearchRides)           // Search for rides

	// User CRUD routes
	app.Post("/user", users.CreateOrUpdateUser)    // Create or update a user
	app.Get("/user/details", users.GetUser)        // Gets user details
	app.Get("/user/all", users.GetAllUsers)        // Gets all users
	app.Delete("/user/delete", users.DeleteUser)   // Deletes a user
	app.Get("/user/rides", users.FetchUserRides)   // Gets all the rides of a particular user
	app.Get("/user/passengers", users.GetPassengers) // Gets all passengers the user has travelled with
	app.Get("/user/default-address", users.GetDefaultAddress) // Gets user's default start address
	app.Post("/user/default-address", users.SetDefaultAddress) // Sets user's default start address
	app.Patch("/user/default-address", users.SetDefaultAddress) // PATCH also sets user's default start address

	//Booking CRUD routes
	app.Post("/booking/create", CRUD.CreateBooking)              // Creates a new booking
	app.Get("/booking/list", CRUD.GetBookings)                   // Retrieves all bookings
	app.Get("/booking/:id", CRUD.GetBookingByID)                 // Retrieves a specific booking by its ID
	app.Patch("/booking/update/:id", CRUD.UpdateBooking)         // Updates an existing booking
	app.Delete("/booking/delete/:id", CRUD.DeleteBooking)        // Deletes a booking
	app.Put("/bookings/accept/:bookingID", bookings.AcceptRoute) // Accepts a booking
	app.Post("/bookings/request", bookings.Request)              // Requests a booking aka Create a booking

	// Chat routes
	app.Get("/chats/:user_id", chat.GetUserChats)
	app.Get("/chat/:ride_id/messages", chat.GetRideMessages)
	app.Post("/chat/:ride_id/message", chat.SendMessage)

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

	app.Post("/booking/create", CRUD.CreateBooking)           // Creates a new booking
	app.Get("/booking/list", CRUD.GetBookings)                // Retrieves all bookings
	app.Get("/booking/:id", CRUD.GetBookingByID)              // Retrieves a specific booking by its ID
	app.Patch("/booking/update/:id", CRUD.UpdateBooking)      // Updates an existing booking
	app.Delete("/booking/delete/:id", bookings.DeleteBooking) // Deletes a booking

}

func main() {
	initializer.InitFirebase()
	initializer.InitializeWebsocket()
	app := fiber.New()

	database.ConnectToDB()

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
