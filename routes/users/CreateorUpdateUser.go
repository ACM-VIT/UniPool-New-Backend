package users

import (
	"log"
	"strings"
	"unipool-backend/database"
	"unipool-backend/middleware"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// resolveInstituteFromEmail looks up the user's email domain in the
// institute_domains table. If the domain is known, returns the
// matching InstituteID + verified=true; otherwise returns nil + false.
//
// Called on new-user creation so verification + institute linkage
// happen once at signup. Cheap (single indexed query) and silent —
// unknown domains just create an unverified user, no error.
func resolveInstituteFromEmail(email string) (*uuid.UUID, bool) {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return nil, false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	if domain == "" {
		return nil, false
	}
	var row models.InstituteDomain
	if err := database.Database.Db.
		Where("LOWER(domain) = ?", domain).
		First(&row).Error; err != nil {
		return nil, false
	}
	id := row.InstituteID
	return &id, true
}

// CreateOrUpdateUser creates a profile for new Firebase users or refreshes the
// stored FCM token for returning users.
func CreateOrUpdateUser(c *fiber.Ctx) error {
	localUser, ok := c.Locals("newuser").(map[string]interface{})
	var existingUser models.User

	if ok {
		var requestBody struct {
			ContactNumber string `json:"contact_number"`
			Gender        string `json:"gender"`
			YOB           uint   `json:"yob"`
		}

		if err := c.BodyParser(&requestBody); err != nil {
			return &fiber.Error{Code: 400, Message: "Invalid JSON body"}
		}

		newUser := models.User{
			Name:              localUser["name"].(string),
			Email:             localUser["email"].(string),
			ProfilePictureURL: localUser["profile_picture_url"].(string),
			ContactNumber:     requestBody.ContactNumber,
			Gender:            requestBody.Gender,
			YOB:               requestBody.YOB,
		}

		// Known institute domains are auto-linked and marked verified.
		if instituteID, verified := resolveInstituteFromEmail(newUser.Email); verified {
			newUser.InstituteID = instituteID
			newUser.IsEmailVerified = true
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
		middleware.InvalidateAuthUserCacheByEmail(newUser.Email)

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"message": "User created successfully", "user": newUser})
	}

	if user, ok := c.Locals("user").(models.User); ok {
		existingUser = user
		UserFCM := c.Get("FCMToken")

		existingUserMetadata := models.UserMetadata{
			UserID:   existingUser.ID,
			FCMToken: UserFCM,
		}

		if err := database.Database.Db.Where("user_id = ?", existingUser.ID).Save(&existingUserMetadata).Error; err != nil {
			log.Println("Error updating user metadata:", err)
			return &fiber.Error{Code: 500, Message: "Error updating user metadata"}
		}
		middleware.InvalidateAuthUserCacheByEmail(existingUser.Email)

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"message": "User FCM token updated successfully",
			"user":    existingUser,
		})
	}

	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
		"error":   true,
		"message": "User data not found in locals",
	})
}
