package users

import (
	"log"
	"unipool-backend/database"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
)

// Function to create or update a user in the DB after authentication
func CreateOrUpdateUser(c *fiber.Ctx) error {
	// Extract user info from locals
	localUser, ok := c.Locals("newuser").(map[string]interface{})
	var existingUser models.User

	if ok {
		// Parse the request body for additional fields
		var requestBody struct {
			ContactNumber string `json:"contact_number"`
			Gender        string `json:"gender"`
			YOB           uint   `json:"yob"`
		}

		if err := c.BodyParser(&requestBody); err != nil {
			return &fiber.Error{Code: 400, Message: "Invalid JSON body"}
		}

		// Combine data from locals and request body to create a new user object
		newUser := models.User{
			Name:              localUser["name"].(string),
			Email:             localUser["email"].(string),
			ProfilePictureURL: localUser["profile_picture_url"].(string),
			ContactNumber:     requestBody.ContactNumber,
			Gender:            requestBody.Gender,
			YOB:               requestBody.YOB,
		}

		log.Println("User to be created:", newUser)
		UserFCM := c.Get("FCMToken")

		var userMetadata models.UserMetadata

		tx := database.Database.Db.Begin()
		if err := database.Database.Db.Create(&newUser).Error; err != nil {
			log.Println("Error creating user:", err)
			tx.Rollback()
			return &fiber.Error{Code: 500, Message: "Error creating user"}
		}

		// Create user metadata with FCM token
		userMetadata = models.UserMetadata{
			UserID:   newUser.ID,
			FCMToken: UserFCM,
		}

		if err := database.Database.Db.Create(&userMetadata).Error; err != nil {
			log.Println("Error creating user metadata:", err)
			tx.Rollback()
			return &fiber.Error{Code: 500, Message: "Error creating user metadata"}
		}
		tx.Commit()

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"message": "User created successfully", "user": newUser})
	}

	if user, ok := c.Locals("user").(models.User); ok {
		existingUser = user
		UserFCM := c.Get("FCMToken")

		// Update user metadata with the new FCM token
		existingUserMetadata := models.UserMetadata{
			UserID:   existingUser.ID,
			FCMToken: UserFCM,
		}

		// Save the FCM token update for existing user
		if err := database.Database.Db.Where("user_id = ?", existingUser.ID).Save(&existingUserMetadata).Error; err != nil {
			log.Println("Error updating user metadata:", err)
			return &fiber.Error{Code: 500, Message: "Error updating user metadata"}
		}

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"message": "User FCM token updated successfully",
			"user":    existingUser,
		})
	}

	// If neither newuser nor user exists in locals, return an error
	return &fiber.Error{Code: 400, Message: "User data not found in locals"}
}
