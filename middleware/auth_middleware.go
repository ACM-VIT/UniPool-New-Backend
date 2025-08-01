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
	// Skip authentication for WebSocket handshake – token will be validated inside chat logic.
	if c.Path() == "/ws" {
		return c.Next()
	}

	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return c.Status(401).JSON(fiber.Map{"error": "Authorization header not found"})
	}

	token := strings.TrimSpace(strings.Replace(authHeader, "Bearer", "", 1))
	if token == "" {
		return c.Status(401).JSON(fiber.Map{"error": "Token not found"})
	}

	// Verify the token using Firebase
	client, err := initializer.FirebaseApp.Auth(context.Background())
	if err != nil {
		log.Println("Firebase Auth error:", err)
		return c.Status(500).JSON(fiber.Map{"error": "Firebase Auth error"})
	}
	decodedToken, err := client.VerifyIDToken(context.Background(), token)
	if err != nil {
		log.Println("Invalid token:", err)
		return c.Status(401).JSON(fiber.Map{"error": "Invalid token"})
	}

	// Extract necessary claims from the token
	if decodedToken == nil || decodedToken.Claims == nil || decodedToken.Claims["email"] == nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid token"})
	}

	email := decodedToken.Claims["email"].(string)

	name, nameOk := decodedToken.Claims["name"].(string)
	if !nameOk {
		return c.Status(401).JSON(fiber.Map{"error": "Please check your privacy settings and allow us to access your name"})
	}

	profilePicture, picOk := decodedToken.Claims["picture"].(string)
	if !picOk {
		profilePicture = "" // INSERT PLACEHOLDER IMAGE URL HERE (@JUXTARYCT - pleaj give image)
	}

	// Check if the user exists in the database (optimized with index hint)
	var user models.User
	//log.Printf("Searching for user with email: %s", email)

	if err := database.Database.Db.Unscoped().Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("User not found in database, creating new user entry for: %s", email)
			c.Locals("newuser", map[string]interface{}{
				"email":               email,
				"name":                name,
				"profile_picture_url": profilePicture,
			})
			// log.Println("New user to be created:", c.Locals("newuser"))
		} else {
			// Database error
			log.Println("Database error:", err)
			return c.Status(500).JSON(fiber.Map{"error": "Database error"})
		}
	} else {
		// User exists; set existing user in locals
		// log.Printf("Found existing user: %s (ID: %s)", user.Email, user.ID)
		c.Locals("user", user)
		// log.Println("Authenticated existing user:", c.Locals("user"))
	}

	return c.Next()
}
