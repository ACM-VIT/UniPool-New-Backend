package middleware

import (
	"context"
	"errors"
	"log"
	"strings"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

func Authenticate(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return &fiber.Error{Code: 401, Message: "Authorization header not found"}
	}

	token := strings.TrimSpace(strings.Replace(authHeader, "Bearer", "", 1))
	if token == "" {
		return &fiber.Error{Code: 401, Message: "Token not found"}
	}

	// Verify the token using Firebase
	client, err := initializer.FirebaseApp.Auth(context.Background())
	if err != nil {
		log.Println("Firebase Auth error:", err)
		return &fiber.Error{Code: 500, Message: "Firebase Auth error"}
	}
	decodedToken, err := client.VerifyIDToken(context.Background(), token)
	if err != nil {
		log.Println("Invalid token:", err)
		return &fiber.Error{Code: 401, Message: "Invalid token"}
	}

	// Extract necessary claims from the token
	if decodedToken == nil || decodedToken.Claims == nil || decodedToken.Claims["email"] == nil {
		return &fiber.Error{Code: 401, Message: "Invalid token"}
	}

	email := decodedToken.Claims["email"].(string)

	name, nameOk := decodedToken.Claims["name"].(string)
	if !nameOk {
		return &fiber.Error{Code: 401, Message: "Please check your privacy settings and allow us to access your name"}
	}

	profilePicture, picOk := decodedToken.Claims["picture"].(string)
	if !picOk {
		profilePicture = "" // INSERT PLACEHOLDER IMAGE URL HERE (@JUXTARYCT - pleaj give image)
	}

	// Check if the user exists in the database (optional step)
	var user models.User
	if err := database.Database.Db.Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// User does not exist; set new user data
			c.Locals("newuser", map[string]interface{}{
				"email":               email,
				"name":                name,
				"profile_picture_url": profilePicture,
			})
			// log.Println("New user to be created:", c.Locals("newuser"))
		} else {
			// Database error
			log.Println("Database error:", err)
			return &fiber.Error{Code: 500, Message: "Database error"}
		}
	} else {
		// User exists; set existing user in locals
		c.Locals("user", user)
		// log.Println("Authenticated existing user:", c.Locals("user"))
	}

	return c.Next()
}
