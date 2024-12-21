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
	client, err := initializer.FirebaseApp.Auth(context.Background())
	if err != nil {
		log.Println(err)
		return &fiber.Error{Code: 500, Message: "Firebase Auth error"}
	}
	decodedToken, err := client.VerifyIDToken(context.Background(), token)
	if err != nil {
		return &fiber.Error{Code: 401, Message: "Invalid token"}
	}
	if decodedToken == nil || decodedToken.Claims == nil || decodedToken.Claims["email"] == nil {
		return &fiber.Error{Code: 401, Message: "Invalid token"}
	}
	email := decodedToken.Claims["email"].(string)
	var user models.User
	if err := database.Database.Db.Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if decodedToken.Claims["name"] == nil {
				user = models.User{
					Email: email,
					Name:  "Test User",
				}
			} else {
				user = models.User{
					Email: email,
					Name:  decodedToken.Claims["name"].(string),
				}
			}
		} else {
			return &fiber.Error{Code: 500, Message: "Database error"}
		}
	}

	c.Locals("user", user)
	return c.Next()
}
