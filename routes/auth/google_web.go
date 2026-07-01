package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
	"unipool-backend/initializer"

	"github.com/gofiber/fiber/v2"
	"google.golang.org/api/idtoken"
)

const defaultGoogleWebClientID = "290309531485-vnb7pgofegur0g8456f3k9lbutgo89fq.apps.googleusercontent.com"

type googleWebAuthRequest struct {
	IDToken string `json:"id_token"`
}

func googleWebClientID() string {
	if value := strings.TrimSpace(os.Getenv("GOOGLE_WEB_CLIENT_ID")); value != "" {
		return value
	}
	return defaultGoogleWebClientID
}

func verifiedEmail(payload *idtoken.Payload) (string, error) {
	email, _ := payload.Claims["email"].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", errors.New("missing email claim")
	}

	switch value := payload.Claims["email_verified"].(type) {
	case bool:
		if !value {
			return "", errors.New("email is not verified")
		}
	case string:
		if value != "true" {
			return "", errors.New("email is not verified")
		}
	}

	return email, nil
}

// GoogleWeb exchanges a Google Identity Services ID token for a Firebase
// custom token. The web app then signs into Firebase with that custom token,
// avoiding Firebase's OAuth popup/redirect flow and its authorized-domain
// check while preserving normal Firebase ID tokens for backend auth.
func GoogleWeb(c *fiber.Ctx) error {
	var body googleWebAuthRequest
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid JSON body"})
	}

	googleIDToken := strings.TrimSpace(body.IDToken)
	if googleIDToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id_token is required"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, err := idtoken.Validate(ctx, googleIDToken, googleWebClientID())
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid Google token"})
	}

	email, err := verifiedEmail(payload)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid Google token"})
	}

	name, _ := payload.Claims["name"].(string)
	picture, _ := payload.Claims["picture"].(string)
	uid := "google:" + strings.TrimSpace(payload.Subject)
	if uid == "google:" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid Google token"})
	}

	authClient, err := initializer.FirebaseApp.Auth(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Firebase auth unavailable"})
	}

	customToken, err := authClient.CustomTokenWithClaims(ctx, uid, map[string]interface{}{
		"email":   email,
		"name":    strings.TrimSpace(name),
		"picture": strings.TrimSpace(picture),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Could not create Firebase token"})
	}

	return c.JSON(fiber.Map{
		"firebase_custom_token": customToken,
		"user": fiber.Map{
			"email":               email,
			"name":                strings.TrimSpace(name),
			"profile_picture_url": strings.TrimSpace(picture),
		},
	})
}
