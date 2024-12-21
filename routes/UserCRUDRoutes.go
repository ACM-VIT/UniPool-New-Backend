package routes

import (
	"errors"
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// User response model
type UserResponse struct {
	UserID uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Email  string    `json:"email"`
	Phone  string    `json:"phone"`
}

//Function to create a user in the DB
func CreateUser(c *fiber.Ctx) error {
	user := c.Locals("user").(models.User)

	var existingUser models.User

	if err := database.Database.Db.Where("email = ?", user.Email).First(&existingUser).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Println(err)
			if err := database.Database.Db.Create(&user).Error; err != nil {
				log.Println(err)
				return &fiber.Error{Code: 500, Message: "Database error"}
			}
			return c.Status(201).SendString("User created")
		} else {
			log.Println(err)
			return &fiber.Error{Code: 502, Message: "Error Finding User"}
		}
	} else {
		// User already exists
		log.Println(existingUser)
		return &fiber.Error{Code: 409, Message: "User already exists"}
	}
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
