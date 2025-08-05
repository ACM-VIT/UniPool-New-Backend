package middleware

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
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

	// Check if the user exists in the database (without global activation scope)
	var user models.User
	log.Printf("Searching for user with email: %s", email)

	// Increase timeout to 5 seconds and add retry logic
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Add retry logic for database queries
	maxRetries := 3
	
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			log.Printf("Retrying database query for user: %s (attempt %d/%d)", email, i+1, maxRetries)
			time.Sleep(time.Duration(i) * 100 * time.Millisecond) // Progressive backoff
		}
		
		err = database.Database.Db.WithContext(ctx).
			Select("id", "email", "name", "profile_picture_url", "contact_number", "gender", "yob", "default_address", "created_at", "updated_at").
			Where("email = ?", email).
			First(&user).Error
			
		if err == nil {
			log.Printf("Found existing user: %s (ID: %s)", user.Email, user.ID)
			c.Locals("user", user)
			break
		}
		
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("User not found in database, creating new user entry for: %s", email)
			c.Locals("newuser", map[string]interface{}{
				"email":               email,
				"name":                name,
				"profile_picture_url": profilePicture,
			})
			break
		}
		
		if errors.Is(err, context.DeadlineExceeded) && i < maxRetries-1 {
			log.Printf("Database query timeout for user: %s (attempt %d/%d), retrying...", email, i+1, maxRetries)
			continue
		}
		
		if !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Database error for user %s: %v", email, err)
			return c.Status(500).JSON(fiber.Map{"error": "Database error"})
		}
	}
	
	// If all retries failed with timeout
	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("Database query failed after %d retries for user: %s", maxRetries, email)
		return c.Status(500).JSON(fiber.Map{"error": "Database timeout - please try again later"})
	}

	return c.Next()
}
