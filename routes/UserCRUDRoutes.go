package routes

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// User response model
type UserResponse struct {
	UserID uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Email  string    `json:"email"`
	Phone  string    `json:"phone"`
}

// Function to get all users in the DB
func GetUsers(c *fiber.Ctx) error {
	var users []models.User
	result := database.Database.Db.Find(&users)

	if result.Error != nil {
		log.Printf("Error finding users: %v\n", result.Error)
		return c.Status(502).SendString("Error finding users")
	}

	usersResponse := make([]UserResponse, len(users))

	for i, user := range users {
		usersResponse[i] = UserResponse{
			UserID: user.ID,
			Name:   user.Name,
			Email:  user.Email,
			Phone:  user.ContactNumber,
		}
	}

	return c.Status(200).JSON(usersResponse)
}

// Function to get a user by their ID
func GetUserByID(c *fiber.Ctx) error {
	id := c.Params("id")
	var user models.User
	result := database.Database.Db.First(&user, id)

	if result.Error != nil {
		log.Printf("Error finding user: %v\n", result.Error)
		return c.Status(502).SendString("Error finding user")
	}

	userResponse := UserResponse{
		UserID: user.ID,
		Name:   user.Name,
		Email:  user.Email,
		Phone:  user.ContactNumber,
	}

	return c.Status(200).JSON(userResponse)
}
