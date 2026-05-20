package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Tunables for the verify flow. Kept here so they're easy to find;
// the values mirror what 6-digit-code flows at other consumer apps
// (Stripe, Linear, Cash App) settle on — short window, small attempt
// budget, but generous enough that a user fat-fingering once doesn't
// have to ask for a new email.
const (
	verifyCodeTTL     = 10 * time.Minute
	verifyMaxAttempts = 5
	// Once a row is sent, don't accept another /verify/start for the
	// same target email until the cooldown elapses. Prevents bursting
	// SES quota on a flaky network and gives spam filters fewer
	// reasons to flag us.
	verifyResendCooldown = 45 * time.Second
	// Custom URL scheme registered in app.json. The magic-link
	// confirms the same row a code would.
	verifyDeeplinkScheme = "unipool://verify"
)

// generateCode returns a uniformly-distributed 6-digit numeric code
// as a zero-padded string. Uses crypto/rand so a leaked code can't
// be predicted from a previous one.
func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func randomSalt() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomToken returns a 256-bit URL-safe token. Used as the
// magic-link payload; long enough that a brute force would burn
// quota long before landing on a live row.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashCode(code, salt string) string {
	h := sha256.Sum256([]byte(salt + ":" + code))
	return hex.EncodeToString(h[:])
}

// StartEmailVerification creates a pending challenge and sends a
// magic-link + code to the target email. Auth-gated; the user being
// verified is the signed-in user.
//
// Body: { "email": "yash@vitstudent.ac.in" }
//
// Rejects emails whose domain isn't a known institute — the verified
// badge would have nothing to attach to, so it's pointless to send a
// code.
func StartEmailVerification(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	target := strings.ToLower(strings.TrimSpace(body.Email))
	if target == "" || !strings.Contains(target, "@") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "please enter a valid email"})
	}

	// Only let people verify against a known institute domain — the
	// whole point of the badge is institute affiliation. Unknown
	// domain → no row created, no email sent.
	if _, recognized := resolveInstituteFromEmail(target); !recognized {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "we don't recognise this institute yet — pick a university from the list",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Cooldown on rapid re-sends to the same target.
	var recent models.EmailVerification
	err := database.Database.Db.WithContext(ctx).
		Where("user_id = ? AND email = ? AND consumed_at IS NULL", user.ID, target).
		Order("created_at DESC").
		First(&recent).Error
	if err == nil {
		since := time.Since(recent.CreatedAt)
		if since < verifyResendCooldown {
			retryAfter := int((verifyResendCooldown - since).Seconds()) + 1
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "please wait a moment before requesting another code",
				"retry_after": retryAfter,
			})
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("StartEmailVerification: cooldown check failed: %v", err)
	}

	code, err := generateCode()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "code gen failed"})
	}
	salt, err := randomSalt()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "code gen failed"})
	}
	token, err := randomToken()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "code gen failed"})
	}

	row := models.EmailVerification{
		UserID:    user.ID,
		Email:     target,
		CodeHash:  hashCode(code, salt),
		Salt:      salt,
		Token:     token,
		ExpiresAt: time.Now().Add(verifyCodeTTL),
	}
	if err := database.Database.Db.WithContext(ctx).Create(&row).Error; err != nil {
		log.Printf("StartEmailVerification: insert failed for user %s: %v", user.ID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not start verification"})
	}

	magicLink := fmt.Sprintf("%s?t=%s", verifyDeeplinkScheme, token)
	if err := helpers.SendVerificationCode(target, code, magicLink); err != nil {
		log.Printf("StartEmailVerification: SES send to %s failed: %v", target, err)
		// Roll back the row so the user can retry without hitting the
		// cooldown. Better to surface the failure than to silently
		// time them out.
		database.Database.Db.WithContext(ctx).Delete(&row)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "couldn't send the email — try again in a moment",
		})
	}

	return c.JSON(fiber.Map{
		"status":     "code_sent",
		"expires_in": int(verifyCodeTTL.Seconds()),
	})
}

// ConfirmEmailVerification accepts EITHER `{email, code}` (manual
// 6-digit entry) OR `{token}` (magic-link tap from the email body).
// Both paths land on the same `email_verifications` row and produce
// the same side effects on the user: institute_id, institute_email,
// is_email_verified.
func ConfirmEmailVerification(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	target := strings.ToLower(strings.TrimSpace(body.Email))
	code := strings.TrimSpace(body.Code)
	token := strings.TrimSpace(body.Token)

	if token == "" && (target == "" || code == "") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing token or email+code"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Look up the pending challenge — by token if provided, otherwise
	// by (user, email).
	var row models.EmailVerification
	var lookupErr error
	if token != "" {
		lookupErr = database.Database.Db.WithContext(ctx).
			Where("token = ? AND consumed_at IS NULL", token).
			First(&row).Error
		// Guard against cross-user token use. The /verify/start path
		// is auth-gated and tokens are 256-bit; this is belt+braces.
		if lookupErr == nil && row.UserID != user.ID {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "this verification link is for a different account",
			})
		}
	} else {
		lookupErr = database.Database.Db.WithContext(ctx).
			Where("user_id = ? AND email = ? AND consumed_at IS NULL", user.ID, target).
			Order("created_at DESC").
			First(&row).Error
	}

	if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "this code or link has already been used — start over",
		})
	}
	if lookupErr != nil {
		log.Printf("ConfirmEmailVerification: lookup failed: %v", lookupErr)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}

	if time.Now().After(row.ExpiresAt) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "this verification has expired — request a new one"})
	}

	// Code path also checks attempt count and hash. Token path skips
	// both — possession of a 256-bit secret IS the proof.
	if token == "" {
		if row.Attempts >= verifyMaxAttempts {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "too many attempts — request a new code"})
		}
		if hashCode(code, row.Salt) != row.CodeHash {
			database.Database.Db.WithContext(ctx).
				Model(&row).
				Update("attempts", row.Attempts+1)
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":              "that code doesn't match",
				"attempts_remaining": verifyMaxAttempts - (row.Attempts + 1),
			})
		}
	}

	// Resolve institute fresh — the institute table may have grown
	// between start and confirm.
	instituteID, recognized := resolveInstituteFromEmail(row.Email)
	if !recognized {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "this email's institute is no longer recognised",
		})
	}

	now := time.Now()
	tx := database.Database.Db.WithContext(ctx).Begin()
	if err := tx.Model(&row).Updates(map[string]any{
		"consumed_at": now,
		"attempts":    row.Attempts + 1,
	}).Error; err != nil {
		tx.Rollback()
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to consume code"})
	}
	if err := tx.Model(&models.User{}).
		Where("id = ?", user.ID).
		Updates(map[string]any{
			"institute_id":      *instituteID,
			"is_email_verified": true,
			"institute_email":   row.Email,
		}).Error; err != nil {
		tx.Rollback()
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to mark verified"})
	}
	if err := tx.Commit().Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "commit failed"})
	}

	// Return the post-update user row so the client can refresh in
	// place without a second round-trip.
	var fresh models.User
	if err := database.Database.Db.WithContext(ctx).
		Preload("Institute").
		Where("id = ?", user.ID).
		First(&fresh).Error; err == nil {
		return c.JSON(fiber.Map{"status": "verified", "user": fresh})
	}
	return c.JSON(fiber.Map{"status": "verified"})
}

// Compile-time assertion that the uuid package is used (avoids the
// "imported and not used" linter shout if the route ever stops
// referencing it directly).
var _ = uuid.Nil
