package routes

import (
	"errors"
	"log"
	"unipool-backend/database"
	"unipool-backend/helpers"
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

// Function to create or update a user in the DB after authentication
func CreateOrUpdateUser(c *fiber.Ctx) error {
	var user models.User

	if err := c.BodyParser(&user); err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid JSON body"}
	}

	if err := helpers.ValidateUser(user); err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid user data"}
	}

	var existingUser models.User

	if err := database.Database.Db.Where("email = ?", user.Email).First(&existingUser).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return createUser(user, c)
		}
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding user"}
	}

	return updateUser(&existingUser, user, c)
}


// Function to create a new user in the DB
func createUser(user models.User, c *fiber.Ctx) error {

	if err := database.Database.Db.Create(&user).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}

	userResponse := UserResponse{
		UserID: user.ID,
		Name:   user.Name,
		Email:  user.Email,
		Phone:  user.ContactNumber,
	}

	log.Printf("User with id %v created\n", user.ID)
	return c.Status(201).JSON(userResponse)
}

// Function to update the details of an existing user
func updateUser(existingUser *models.User, user models.User, c *fiber.Ctx) error {
	updated := false

	if user.Name != existingUser.Name {
		existingUser.Name = user.Name
		updated = true
	}

	if user.ContactNumber != existingUser.ContactNumber {
		existingUser.ContactNumber = user.ContactNumber
		updated = true
	}

	if user.ProfilePictureURL != existingUser.ProfilePictureURL {
		existingUser.ProfilePictureURL = user.ProfilePictureURL
		updated = true
	}

	if user.Gender != existingUser.Gender {
		existingUser.Gender = user.Gender
		updated = true
	}

	if user.YOB != existingUser.YOB {
		existingUser.YOB = user.YOB
		updated = true
	}

	if updated {
		if err := database.Database.Db.Save(existingUser).Error; err != nil {
			log.Println(err)
			return &fiber.Error{Code: 500, Message: "Database error"}
		}
		userResponse := UserResponse{
			UserID: existingUser.ID,
			Name:   existingUser.Name,
			Email:  existingUser.Email,
			Phone:  existingUser.ContactNumber,
		}

		log.Printf("User with id %v updated\n", existingUser.ID)
		return c.Status(200).JSON(userResponse)
	}

	return c.Status(409).SendString("User already exists with complete details")
}

// Function to get all users in the DB
func GetUsers(c *fiber.Ctx) error {
	var users []models.User

	if err := database.Database.Db.Find(&users).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding users"}
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
	idStr := c.Params("id")

	_, err := uuid.Parse(idStr)
	if err != nil {
		log.Println(err)
		return &fiber.Error{Code: 400, Message: "Invalid user ID format"}
	}

	var user models.User

	if err := database.Database.Db.First(&user, "id = ?", idStr).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding user"}
	}

	userResponse := UserResponse{
		UserID: user.ID,
		Name:   user.Name,
		Email:  user.Email,
		Phone:  user.ContactNumber,
	}

	return c.Status(200).JSON(userResponse)
}


// Function to delete a user by their ID
func DeleteUserByID(c *fiber.Ctx) error {
	idStr := c.Params("id")

	_, err := uuid.Parse(idStr)
	if err != nil {
		log.Println(err)
		return &fiber.Error{Code: 400, Message: "Invalid user ID format"}
	}

	var user models.User

	if err := database.Database.Db.First(&user, "id = ?", idStr).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding user"}
	}


	if err := database.Database.Db.Delete(&user). Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Database error"}
	}
	

	log.Printf("User with id %v deleted\n", user.ID)
	return c.Status(200).SendString("User deleted")
}

// Function to update user by their ID
func UpdateUserByID(c *fiber.Ctx) error {
	idStr := c.Params("id")

	_, err := uuid.Parse(idStr)
	if err != nil {
		log.Println(err)
		return &fiber.Error{Code: 400, Message: "Invalid user ID format"}
	}

	var user models.User
	if err := c.BodyParser(&user); err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid JSON body"}
	}

	if err := helpers.ValidateUser(user); err != nil {
		return &fiber.Error{Code: 400, Message: "Invalid user data"}
	}

	var existingUser models.User
	if err := database.Database.Db.First(&existingUser, "id = ?", idStr).Error; err != nil {
		log.Println(err)
		return &fiber.Error{Code: 502, Message: "Error finding user"}
	}

	updated := false

	if user.Name != existingUser.Name {
		existingUser.Name = user.Name
		updated = true
	}

	if user.ContactNumber != existingUser.ContactNumber {
		existingUser.ContactNumber = user.ContactNumber
		updated = true
	}

	if user.ProfilePictureURL != existingUser.ProfilePictureURL {
		existingUser.ProfilePictureURL = user.ProfilePictureURL
		updated = true
	}

	if user.Gender != existingUser.Gender {
		existingUser.Gender = user.Gender
		updated = true
	}

	if user.YOB != existingUser.YOB {
		existingUser.YOB = user.YOB
		updated = true
	}

	if updated {
		if err := database.Database.Db.Save(&existingUser).Error; err != nil {
			log.Println(err)
			return &fiber.Error{Code: 500, Message: "Database error"}
		}

		userResponse := UserResponse{
			UserID: existingUser.ID,
			Name:   existingUser.Name,
			Email:  existingUser.Email,
			Phone:  existingUser.ContactNumber,
		}

		log.Printf("User with id %v updated\n", existingUser.ID)
		return c.Status(200).JSON(userResponse)
	}

	return c.Status(409).SendString("User already has the same details")
}
