package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/models"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

var authDebugLogs = os.Getenv("AUTH_DEBUG_LOGS") == "1"

var firebaseAuthCache struct {
	sync.RWMutex
	client *firebaseauth.Client
}

const authUserCacheTTL = 5 * time.Minute
const authTokenCacheMaxEntries = 2048
const authTokenCacheMaxTTL = 5 * time.Minute
const authTokenCacheExpirySkew = 10 * time.Second

type authUserCacheEntry struct {
	user      models.User
	expiresAt time.Time
}

type authTokenCacheEntry struct {
	email          string
	name           string
	profilePicture string
	expiresAt      time.Time
}

var authUserCache struct {
	sync.RWMutex
	byEmail map[string]authUserCacheEntry
}

var authUserLookupGroup singleflight.Group

var authTokenCache struct {
	sync.RWMutex
	byTokenHash map[string]authTokenCacheEntry
}

func normalizeAuthEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func fallbackAuthNameFromEmail(email string) string {
	email = normalizeAuthEmail(email)
	if strings.HasSuffix(email, "@privaterelay.appleid.com") {
		return "Apple User"
	}
	local, _, ok := strings.Cut(email, "@")
	if !ok {
		local = email
	}
	local = strings.TrimSpace(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(local))
	if local == "" {
		return "UniPool User"
	}
	return local
}

func firstStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []string:
		for _, item := range v {
			if s := strings.TrimSpace(item); s != "" {
				return s
			}
		}
	case []interface{}:
		for _, item := range v {
			if s := firstStringValue(item); s != "" {
				return s
			}
		}
	}
	return ""
}

func authTokenEmail(decoded *firebaseauth.Token) string {
	if decoded == nil {
		return ""
	}
	if email := firstStringValue(decoded.Claims["email"]); email != "" {
		return normalizeAuthEmail(email)
	}
	if email := firstStringValue(decoded.Firebase.Identities["email"]); email != "" {
		return normalizeAuthEmail(email)
	}
	return ""
}

func authTokenUID(decoded *firebaseauth.Token) string {
	if decoded == nil {
		return ""
	}
	if uid := strings.TrimSpace(decoded.UID); uid != "" {
		return uid
	}
	return strings.TrimSpace(decoded.Subject)
}

func authTokenCacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func cachedVerifiedToken(token string) (authTokenCacheEntry, bool) {
	if token == "" {
		return authTokenCacheEntry{}, false
	}
	key := authTokenCacheKey(token)
	authTokenCache.RLock()
	entry, ok := authTokenCache.byTokenHash[key]
	authTokenCache.RUnlock()
	if !ok {
		return authTokenCacheEntry{}, false
	}
	if time.Now().After(entry.expiresAt) {
		authTokenCache.Lock()
		if current, exists := authTokenCache.byTokenHash[key]; exists && current.expiresAt.Equal(entry.expiresAt) {
			delete(authTokenCache.byTokenHash, key)
		}
		authTokenCache.Unlock()
		return authTokenCacheEntry{}, false
	}
	return entry, true
}

func pruneAuthTokenCacheLocked(now time.Time) {
	for key, entry := range authTokenCache.byTokenHash {
		if now.After(entry.expiresAt) {
			delete(authTokenCache.byTokenHash, key)
		}
	}
	if len(authTokenCache.byTokenHash) <= authTokenCacheMaxEntries {
		return
	}
	for key := range authTokenCache.byTokenHash {
		delete(authTokenCache.byTokenHash, key)
		if len(authTokenCache.byTokenHash) <= authTokenCacheMaxEntries {
			break
		}
	}
}

func cacheVerifiedToken(token string, entry authTokenCacheEntry) authTokenCacheEntry {
	now := time.Now()
	if entry.email == "" || !entry.expiresAt.After(now) {
		return entry
	}

	key := authTokenCacheKey(token)
	authTokenCache.Lock()
	if authTokenCache.byTokenHash == nil {
		authTokenCache.byTokenHash = make(map[string]authTokenCacheEntry)
	}
	pruneAuthTokenCacheLocked(now)
	authTokenCache.byTokenHash[key] = entry
	authTokenCache.Unlock()
	return entry
}

func storeVerifiedToken(token string, decoded *firebaseauth.Token) authTokenCacheEntry {
	now := time.Now()
	expiresAt := time.Unix(decoded.Expires, 0)
	if maxExpiresAt := now.Add(authTokenCacheMaxTTL); expiresAt.After(maxExpiresAt) {
		expiresAt = maxExpiresAt
	}

	entry := authTokenCacheEntry{
		expiresAt: expiresAt.Add(-authTokenCacheExpirySkew),
	}
	entry.email = authTokenEmail(decoded)
	entry.name = firstStringValue(decoded.Claims["name"])
	entry.profilePicture = firstStringValue(decoded.Claims["picture"])
	return cacheVerifiedToken(token, entry)
}

func verifyAuthTokenClaims(ctx context.Context, token string) (authTokenCacheEntry, error) {
	if cached, ok := cachedVerifiedToken(token); ok {
		return cached, nil
	}
	client, err := firebaseAuthClient(ctx)
	if err != nil {
		return authTokenCacheEntry{}, err
	}
	decodedToken, err := client.VerifyIDToken(ctx, token)
	if err != nil {
		return authTokenCacheEntry{}, err
	}
	if decodedToken == nil {
		return authTokenCacheEntry{}, errors.New("invalid token claims")
	}

	entry := storeVerifiedToken(token, decodedToken)
	if entry.email == "" {
		if uid := authTokenUID(decodedToken); uid != "" {
			record, err := client.GetUser(ctx, uid)
			if err == nil && record != nil {
				entry.email = normalizeAuthEmail(record.Email)
				if entry.name == "" {
					entry.name = strings.TrimSpace(record.DisplayName)
				}
				if entry.profilePicture == "" {
					entry.profilePicture = strings.TrimSpace(record.PhotoURL)
				}
				entry = cacheVerifiedToken(token, entry)
			}
		}
	}
	if entry.email == "" {
		return authTokenCacheEntry{}, errors.New("invalid token claims")
	}
	return entry, nil
}

func cachedAuthUser(email string) (models.User, bool) {
	key := normalizeAuthEmail(email)
	if key == "" {
		return models.User{}, false
	}
	authUserCache.RLock()
	entry, ok := authUserCache.byEmail[key]
	authUserCache.RUnlock()
	if !ok {
		return models.User{}, false
	}
	if time.Now().After(entry.expiresAt) {
		authUserCache.Lock()
		if current, exists := authUserCache.byEmail[key]; exists && current.expiresAt.Equal(entry.expiresAt) {
			delete(authUserCache.byEmail, key)
		}
		authUserCache.Unlock()
		return models.User{}, false
	}
	return entry.user, true
}

func storeAuthUser(email string, user models.User) {
	key := normalizeAuthEmail(email)
	if key == "" {
		return
	}
	authUserCache.Lock()
	if authUserCache.byEmail == nil {
		authUserCache.byEmail = make(map[string]authUserCacheEntry)
	}
	authUserCache.byEmail[key] = authUserCacheEntry{
		user:      user,
		expiresAt: time.Now().Add(authUserCacheTTL),
	}
	authUserCache.Unlock()
}

func loadAuthUserByEmail(ctx context.Context, email string) (models.User, error) {
	if user, ok := cachedAuthUser(email); ok {
		return user, nil
	}

	key := normalizeAuthEmail(email)
	if key == "" {
		return models.User{}, gorm.ErrRecordNotFound
	}

	value, err, _ := authUserLookupGroup.Do(key, func() (any, error) {
		if user, ok := cachedAuthUser(key); ok {
			return user, nil
		}

		var user models.User
		if err := database.Database.Db.WithContext(ctx).
			Select("id", "email", "name", "profile_picture_url", "contact_number", "gender", "yob", "default_address", "institute_id", "is_email_verified", "institute_email", "upi_vpa", "created_at", "updated_at").
			Where("LOWER(email) = ?", key).
			Take(&user).Error; err != nil {
			return models.User{}, err
		}
		storeAuthUser(key, user)
		return user, nil
	})
	if err != nil {
		return models.User{}, err
	}

	user, ok := value.(models.User)
	if !ok {
		return models.User{}, gorm.ErrInvalidData
	}
	return user, nil
}

// InvalidateAuthUserCacheByEmail is called by profile/signup/delete
// writes so request auth never serves stale identity fields after a
// user-facing mutation.
func InvalidateAuthUserCacheByEmail(email string) {
	key := normalizeAuthEmail(email)
	if key == "" {
		return
	}
	authUserCache.Lock()
	delete(authUserCache.byEmail, key)
	authUserCache.Unlock()
}

func firebaseAuthClient(ctx context.Context) (*firebaseauth.Client, error) {
	firebaseAuthCache.RLock()
	client := firebaseAuthCache.client
	firebaseAuthCache.RUnlock()
	if client != nil {
		return client, nil
	}

	firebaseAuthCache.Lock()
	defer firebaseAuthCache.Unlock()
	if firebaseAuthCache.client != nil {
		return firebaseAuthCache.client, nil
	}

	next, err := initializer.FirebaseApp.Auth(ctx)
	if err != nil {
		return nil, err
	}
	firebaseAuthCache.client = next
	return next, nil
}

// UserFromBearerToken verifies a Firebase ID token and resolves the matching
// database user through the same caches used by HTTP middleware.
func UserFromBearerToken(ctx context.Context, token string) (models.User, error) {
	claims, err := verifyAuthTokenClaims(ctx, token)
	if err != nil {
		return models.User{}, err
	}
	email := claims.email
	if strings.TrimSpace(email) == "" {
		return models.User{}, errors.New("invalid token claims")
	}

	return loadAuthUserByEmail(ctx, email)
}

// OptionalAuthenticate populates c.Locals("user") when a valid token and user
// row are present, but never blocks public endpoints.
//
// Used by public reads that can personalize responses for signed-in users.
func OptionalAuthenticate(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return c.Next()
	}
	token := strings.TrimSpace(strings.Replace(authHeader, "Bearer", "", 1))
	if token == "" {
		return c.Next()
	}

	claims, err := verifyAuthTokenClaims(context.Background(), token)
	if err != nil || claims.email == "" {
		return c.Next()
	}
	email := claims.email

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	user, err := loadAuthUserByEmail(ctx, email)
	if err == nil {
		c.Locals("user", user)
	}
	return c.Next()
}

func Authenticate(c *fiber.Ctx) error {
	// WebSocket handshakes validate the token after upgrade.
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

	// Verified token claims are cached briefly to reduce repeated Firebase calls.
	claims, err := verifyAuthTokenClaims(context.Background(), token)
	if err != nil {
		log.Println("Invalid token:", err)
		return c.Status(401).JSON(fiber.Map{"error": "Invalid token"})
	}

	email := claims.email
	if strings.TrimSpace(email) == "" {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid token"})
	}

	name := strings.TrimSpace(claims.name)
	if strings.TrimSpace(name) == "" {
		name = fallbackAuthNameFromEmail(email)
	}

	profilePicture := claims.profilePicture

	var user models.User
	if authDebugLogs {
		log.Printf("Searching for user with email: %s", email)
	}

	if cached, ok := cachedAuthUser(email); ok {
		if authDebugLogs {
			log.Printf("Found cached auth user: %s (ID: %s)", cached.Email, cached.ID)
		}
		c.Locals("user", cached)
		return c.Next()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			log.Printf("Retrying database query for user: %s (attempt %d/%d)", email, i+1, maxRetries)
			time.Sleep(time.Duration(i) * 100 * time.Millisecond)
		}

		user, err = loadAuthUserByEmail(ctx, email)

		if err == nil {
			if authDebugLogs {
				log.Printf("Found existing user: %s (ID: %s)", user.Email, user.ID)
			}
			storeAuthUser(email, user)
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

	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("Database query failed after %d retries for user: %s", maxRetries, email)
		return c.Status(500).JSON(fiber.Map{"error": "Database timeout - please try again later"})
	}

	return c.Next()
}
