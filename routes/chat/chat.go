package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/initializer"
	"unipool-backend/models"
	"unipool-backend/services"
)

// ----------------------------------------------------------------------
// Shared helpers
// ----------------------------------------------------------------------

const (
	// Cap the size of a single page so the client never has to download
	// a multi-megabyte JSON blob for a chatty ride. The chat UI loads
	// page-by-page as the user scrolls up.
	defaultMessageLimit = 50
	maxMessageLimit     = 200
)

// Roles a user can have on a ride chat. The chat endpoints branch on
// this for both auth (none/rejected => 403) and visibility filtering
// (pending only sees host ↔ self thread).
const (
	roleHost     = "host"
	roleAccepted = "accepted"
	rolePending  = "pending"
	roleRejected = "rejected"
	roleNone     = "none"
)

// getRideViewerRole resolves the caller's relationship to a ride in
// one query. Returns the host_user_id alongside the role so callers
// that need it (message filtering for `pending` viewers) avoid a
// second lookup.
func getRideViewerRole(userID, rideID uuid.UUID) (role string, hostID uuid.UUID, err error) {
	var row struct {
		HostUserID    uuid.UUID
		RequestStatus *string
	}
	q := database.Database.Db.Raw(`
		SELECT r.host_user_id,
		       b.request_status
		  FROM rides r
		  LEFT JOIN bookings b
		    ON b.ride_id = r.id
		   AND b.passenger_id = ?
		 WHERE r.id = ?
		 LIMIT 1
	`, userID, rideID).Scan(&row)
	if q.Error != nil {
		return roleNone, uuid.Nil, q.Error
	}
	if q.RowsAffected == 0 {
		return roleNone, uuid.Nil, nil
	}
	hostID = row.HostUserID
	if hostID == userID {
		return roleHost, hostID, nil
	}
	if row.RequestStatus == nil {
		return roleNone, hostID, nil
	}
	switch *row.RequestStatus {
	case "accepted":
		return roleAccepted, hostID, nil
	case "pending":
		return rolePending, hostID, nil
	case "rejected":
		return roleRejected, hostID, nil
	}
	return roleNone, hostID, nil
}

// transformMessage serialises a `models.Message` into the wire shape the
// frontend expects. Kept in one place so every endpoint emits identical
// keys — the old code had four different shapes which forced the JS
// client to normalise six field name variants.
func transformMessage(msg *models.Message) fiber.Map {
	// `kind` defaults to "user" — clients dispatch their render on
	// this value (text bubble vs system card). `metadata` is the
	// non-'user' card payload; always emitted so clients can rely on
	// the field existing.
	kind := msg.Kind
	if kind == "" {
		kind = models.MessageKindUser
	}
	metadata := msg.Metadata
	if metadata == nil {
		metadata = models.MessageMetadata{}
	}
	out := fiber.Map{
		"id":        msg.ID.String(),
		"content":   msg.Content,
		"sender_id": msg.SenderID.String(),
		"timestamp": msg.CreatedAt.Format(time.RFC3339),
		"kind":      kind,
		"metadata":  metadata,
		"sender": fiber.Map{
			"name":                msg.Sender.Name,
			"profile_picture_url": msg.Sender.ProfilePictureURL,
		},
	}
	if msg.RideID != nil {
		out["ride_id"] = msg.RideID.String()
	}
	if msg.DMRoomID != nil {
		out["dm_room_id"] = *msg.DMRoomID
	}
	return out
}

// parsePagination reads `?limit=` and `?before=<rfc3339-ts>` query
// params. `before` lets the client ask for messages older than a given
// timestamp, which is the natural cursor for an infinite-scroll list.
func parsePagination(c *fiber.Ctx) (limit int, before time.Time, hasBefore bool) {
	limit = defaultMessageLimit
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			if v > maxMessageLimit {
				v = maxMessageLimit
			}
			limit = v
		}
	}
	if raw := c.Query("before"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			before = t
			hasBefore = true
		}
	}
	return
}

// ----------------------------------------------------------------------
// Message history (paginated)
// ----------------------------------------------------------------------

// GetRideMessages returns at most `limit` messages older than `before`
// for a ride chat, newest-first on the wire so the client can render
// the latest at the bottom without sorting. Default limit = 50.
//
// Paginated to keep the initial chat load snappy regardless of how
// chatty the ride has been.
func GetRideMessages(c *fiber.Ctx) error {
	rideID := c.Params("ride_id")
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}

	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}

	// Membership check: only host / accepted can read the group ride
	// chat. Pending requesters are routed to a DM with the host
	// (`/dm/...`) and never touch this endpoint; rejected requesters
	// and outsiders get 403.
	role, _, err := getRideViewerRole(user.ID, rideUUID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "membership lookup failed"})
	}
	if role != roleHost && role != roleAccepted {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this ride"})
	}

	limit, before, hasBefore := parsePagination(c)

	query := database.Database.Db.
		// `transformMessage` only reads sender.Name +
		// sender.ProfilePictureURL; pulling the full User row (incl.
		// the 500-char FCMToken, DeviceID, InstituteEmail, etc.) per
		// message ballooned the response on a 50-message page.
		Preload("Sender", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, profile_picture_url")
		}).
		Where("ride_id = ?", rideUUID).
		Order("created_at desc").
		Limit(limit)
	if hasBefore {
		query = query.Where("created_at < ?", before)
	}

	var messages []models.Message
	if err := query.Find(&messages).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch messages"})
	}

	// Reverse to chronological order on the wire — easier for the
	// client to render in a top-down list.
	transformed := make([]fiber.Map, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		transformed = append(transformed, transformMessage(&messages[i]))
	}

	return c.JSON(fiber.Map{
		"messages": transformed,
		"count":    len(transformed),
		"has_more": len(messages) == limit,
	})
}

// GetDMMessages mirrors GetRideMessages for direct-message rooms.
func GetDMMessages(c *fiber.Ctx) error {
	dmRoomID := c.Params("dm_room_id")
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	if _, allowed, err := canAccessDMRoom(user.ID, dmRoomID); err != nil {
		if errors.Is(err, errInvalidDMRoomID) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid dm room id format"})
		}
		log.Printf("GetDMMessages: dm access lookup failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "dm access lookup failed"})
	} else if !allowed {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this dm"})
	}

	limit, before, hasBefore := parsePagination(c)

	query := database.Database.Db.
		// Same narrowing as GetRideMessages — transformMessage only
		// needs Name + ProfilePictureURL from the sender.
		Preload("Sender", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, profile_picture_url")
		}).
		Where("dm_room_id = ?", dmRoomID).
		Order("created_at desc").
		Limit(limit)
	if hasBefore {
		query = query.Where("created_at < ?", before)
	}

	var messages []models.Message
	if err := query.Find(&messages).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch messages"})
	}

	transformed := make([]fiber.Map, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		transformed = append(transformed, transformMessage(&messages[i]))
	}

	return c.JSON(fiber.Map{
		"messages": transformed,
		"count":    len(transformed),
		"has_more": len(messages) == limit,
	})
}

// ----------------------------------------------------------------------
// Send
// ----------------------------------------------------------------------

// SendMessage persists a ride chat message, broadcasts it to connected
// WebSocket clients, and fans out push notifications to every other
// participant (host + accepted passengers).
//
// Notifications respect the per-ride `NotificationsMuted` flag and run
// in parallel rather than sequentially — so notifying ten passengers
// no longer takes ten times as long.
func SendMessage(c *fiber.Ctx) error {
	rideID := c.Params("ride_id")
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}

	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}

	// Only host + accepted can post to the group ride chat. Pending
	// requesters live in a DM with the host, not in here.
	role, _, err := getRideViewerRole(user.ID, rideUUID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "membership lookup failed"})
	}
	if role != roleHost && role != roleAccepted {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this ride"})
	}

	var body struct {
		Content string `json:"content"`
		TempID  string `json:"temp_id,omitempty"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "message content cannot be empty"})
	}

	msg := models.Message{RideID: &rideUUID, SenderID: user.ID, Content: body.Content}
	if err := database.Database.Db.Create(&msg).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to save message"})
	}
	msg.Sender = user // we already have the sender — skip the round-trip Preload

	// Broadcast over the websocket so anyone with the chat open sees
	// the message before the HTTP response even returns to the sender.
	broadcastChatMessage(rideID, &msg, body.TempID, user)

	// Async fan-out of push notifications. We snapshot just the IDs we
	// need so the goroutine doesn't hold references to the request ctx.
	go fanOutRideNotifications(rideUUID, user, body.Content)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": transformMessage(&msg),
	})
}

// PostSystemMessage inserts a non-'user' message into a ride group
// chat, broadcasts it over the websocket, and fires push fan-out to
// every accepted participant. Used by non-chat handlers (currently
// the payment lifecycle in routes/bookings) to make state changes
// visible in the chat without forcing every consumer to re-implement
// the broadcast + FCM plumbing.
//
// `senderID` is the user the system message is attributed to. For
// payment_marker that's the passenger; for payment_ack that's the
// host. The chat client checks `kind != "user"` to render it as a
// system card instead of a text bubble.
//
// `fcmTitle` and `fcmBody` are the push payload. Pass empty strings
// to skip the push (e.g. for low-signal system messages). When the
// fan-out includes the sender we still skip pushing to them — same
// posture as fanOutRideNotifications.
//
// Errors are returned, not swallowed; callers decide whether the
// system message is critical (return 500) or best-effort (log and
// move on).
func PostSystemMessage(
	rideUUID uuid.UUID,
	senderID uuid.UUID,
	kind, content string,
	metadata models.MessageMetadata,
	fcmTitle, fcmBody string,
) (*models.Message, error) {
	msg := models.Message{
		RideID:   &rideUUID,
		SenderID: senderID,
		Content:  content,
		Kind:     kind,
		Metadata: metadata,
	}
	if msg.Metadata == nil {
		msg.Metadata = models.MessageMetadata{}
	}
	if err := database.Database.Db.Create(&msg).Error; err != nil {
		return nil, err
	}
	// Hydrate the sender so the broadcast carries name + avatar
	// without an extra round-trip. Best-effort: a failure here just
	// means the broadcast omits the sender block, which the client
	// handles.
	var sender models.User
	if err := database.Database.Db.
		Select("id, name, profile_picture_url").
		First(&sender, senderID).Error; err == nil {
		msg.Sender = sender
	}

	broadcastChatMessage(rideUUID.String(), &msg, "", sender)

	if fcmTitle != "" || fcmBody != "" {
		go fanOutRideNotificationsSystem(rideUUID, senderID, fcmTitle, fcmBody, map[string]string{
			"type":    "system_" + kind,
			"ride_id": rideUUID.String(),
			"action":  "open_chat",
		})
	}
	return &msg, nil
}

// fanOutRideNotificationsSystem is the system-message twin of
// fanOutRideNotifications. Same recipient resolution (host +
// accepted passengers, minus the actor), same per-user notification
// preference gate, same batched FCM send — just with a configurable
// payload so payment markers / acks can use their own title/body
// instead of the chat-message default.
func fanOutRideNotificationsSystem(rideUUID uuid.UUID, actorID uuid.UUID, title, body string, data map[string]string) {
	fcm := services.GetFCMService()
	if fcm == nil {
		return
	}
	var ride models.Ride
	var bookings []models.Booking
	var rideErr, bookingsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rideErr = database.Database.Db.
			Select("id, host_user_id, settings").
			First(&ride, rideUUID).Error
	}()
	go func() {
		defer wg.Done()
		bookingsErr = database.Database.Db.
			Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").
			Find(&bookings).Error
	}()
	wg.Wait()
	if rideErr != nil {
		log.Printf("system fanout: ride %s lookup failed: %v", rideUUID, rideErr)
		return
	}
	if ride.Settings.NotificationsMuted {
		return
	}
	if bookingsErr != nil {
		log.Printf("system fanout: bookings lookup for ride %s failed: %v", rideUUID, bookingsErr)
		return
	}

	recipients := map[uuid.UUID]struct{}{}
	if ride.HostUserID != actorID {
		recipients[ride.HostUserID] = struct{}{}
	}
	for _, b := range bookings {
		if b.PassengerID != actorID {
			recipients[b.PassengerID] = struct{}{}
		}
	}
	if len(recipients) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(recipients))
	for id := range recipients {
		ids = append(ids, id)
	}
	allowed := helpers.FilterAllowedRecipients(ids, helpers.NotifChatMessages, rideUUID)
	tokens, err := services.LoadFCMTokens(allowed)
	if err != nil {
		log.Printf("system fanout: token lookup failed: %v", err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	fcm.SendBatch(tokens, title, body, data)
}

// SendDMMessage mirrors SendMessage for direct messages between two
// users. The room ID encodes both UUIDs (`dm_<a>_<b>`); we extract the
// recipient and FCM them.
func SendDMMessage(c *fiber.Ctx) error {
	dmRoomID := c.Params("dm_room_id")
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	if _, allowed, err := canAccessDMRoom(user.ID, dmRoomID); err != nil {
		if errors.Is(err, errInvalidDMRoomID) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid dm room id format"})
		}
		log.Printf("SendDMMessage: dm access lookup failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "dm access lookup failed"})
	} else if !allowed {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this dm"})
	}

	var body struct {
		Content string `json:"content"`
		TempID  string `json:"temp_id,omitempty"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "message content cannot be empty"})
	}

	msg := models.Message{
		DMRoomID: &dmRoomID,
		SenderID: user.ID,
		Content:  body.Content,
	}
	if err := database.Database.Db.Create(&msg).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to save message"})
	}
	msg.Sender = user

	broadcastChatMessage(dmRoomID, &msg, body.TempID, user)

	go fanOutDMNotification(dmRoomID, user, body.Content)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": transformMessage(&msg),
	})
}

// broadcastChatMessage serialises and pushes a message over the
// websocket hub. Idempotent — caller decides whether to send.
func broadcastChatMessage(roomID string, msg *models.Message, tempID string, sender models.User) {
	hub := initializer.GetChatHub()
	if hub == nil {
		return
	}
	chatMsg := initializer.ChatMessage{
		Type:      "message",
		RoomID:    roomID,
		SenderID:  sender.ID.String(),
		Content:   msg.Content,
		Timestamp: msg.CreatedAt.Format(time.RFC3339),
		MessageID: msg.ID.String(),
		TempID:    tempID,
		Sender: &initializer.UserInfo{
			Name:              sender.Name,
			ProfilePictureURL: sender.ProfilePictureURL,
		},
	}
	if b, err := json.Marshal(chatMsg); err == nil {
		hub.BroadcastToRoom(roomID, b)
	}
}

// fanOutRideNotifications fetches the ride + accepted bookings, then
// pushes FCM to every participant except the sender — in parallel.
//
// Respects the ride's `NotificationsMuted` setting so a host who's
// silenced the group doesn't get pinged on every reply.
func fanOutRideNotifications(rideUUID uuid.UUID, sender models.User, content string) {
	fcm := services.GetFCMService()
	if fcm == nil {
		return
	}

	// Two independent reads — ride row + accepted bookings — kicked
	// off concurrently. Pre-fix this was sequential (~50ms wall
	// time) AND the ride preload pulled a full HostUser including
	// the 500-char FCMToken which is never used here. Dropping that
	// preload + parallelising both queries saves ~25ms and ~1KB per
	// chat-message send before the FCM goroutines even spin up.
	var (
		ride        models.Ride
		bookings    []models.Booking
		rideErr     error
		bookingsErr error
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		rideErr = database.Database.Db.
			Select("id, host_user_id, start_location, end_location, settings").
			First(&ride, rideUUID).Error
	}()
	go func() {
		defer wg.Done()
		bookingsErr = database.Database.Db.
			Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").
			Find(&bookings).Error
	}()
	wg.Wait()
	if rideErr != nil {
		log.Printf("notifications: ride %s lookup failed: %v", rideUUID, rideErr)
		return
	}
	if ride.Settings.NotificationsMuted {
		return
	}
	if bookingsErr != nil {
		log.Printf("notifications: bookings lookup for ride %s failed: %v", rideUUID, bookingsErr)
		return
	}

	// Build the recipient set: host + every accepted passenger, minus
	// the sender. Pending requesters live in DMs with the host and
	// are notified through fanOutDMNotification instead.
	recipients := map[uuid.UUID]struct{}{}
	if ride.HostUserID != sender.ID {
		recipients[ride.HostUserID] = struct{}{}
	}
	for _, b := range bookings {
		if b.PassengerID != sender.ID {
			recipients[b.PassengerID] = struct{}{}
		}
	}

	// Batched recipient pipeline. Pre-fix this loop spawned one
	// goroutine per recipient and each one did:
	//   1. IsNotificationAllowed → 2 DB lookups
	//   2. fcm.SendChatMessageNotification → 1 DB lookup + 1 FCM HTTP
	// At 10 accepted passengers that was ~30 DB round-trips and 10
	// separate FCM HTTP calls. Now: 2 DB queries (allowed-filter +
	// token-batch) and 1 FCM HTTP call (SendEach fan-out) regardless
	// of recipient count.
	recipientIDs := make([]uuid.UUID, 0, len(recipients))
	for uid := range recipients {
		recipientIDs = append(recipientIDs, uid)
	}
	allowedIDs := helpers.FilterAllowedRecipients(recipientIDs, helpers.NotifChatMessages, rideUUID)
	tokens, err := services.LoadFCMTokens(allowedIDs)
	if err != nil {
		log.Printf("notifications: token batch lookup for ride %s failed: %v", rideUUID, err)
		return
	}
	if len(tokens) == 0 {
		return
	}

	// Mirror SendChatMessageNotification's title/body/data shape so
	// the client routing logic is unchanged.
	title := fmt.Sprintf("New message from %s", sender.Name)
	body := fmt.Sprintf("💬 %s", content)
	if len(body) > 100 {
		body = body[:97] + "..."
	}
	fcm.SendBatch(tokens, title, body, map[string]string{
		"type":    "chat_message",
		"ride_id": rideUUID.String(),
		"action":  "open_chat",
	})
}

// fanOutDMNotification handles the simpler "send FCM to the other half
// of this DM" case.
func fanOutDMNotification(dmRoomID string, sender models.User, content string) {
	fcm := services.GetFCMService()
	if fcm == nil {
		return
	}

	parts := strings.Split(strings.TrimPrefix(dmRoomID, "dm_"), "_")
	if len(parts) != 2 {
		return
	}

	var otherIDStr string
	if parts[0] == sender.ID.String() {
		otherIDStr = parts[1]
	} else {
		otherIDStr = parts[0]
	}
	otherID, err := uuid.Parse(otherIDStr)
	if err != nil {
		return
	}
	// DMs go through their own preference category so a user can
	// silence the noisy ride group chats without losing 1:1 threads
	// (or vice versa). Default-allowed when no row exists — most
	// users never touch the settings.
	if allowed, _ := helpers.IsNotificationAllowed(otherID, helpers.NotifDirectMessages, uuid.Nil); !allowed {
		return
	}
	if err := fcm.SendDirectMessageNotification(otherID, sender.ID, sender.Name, content, dmRoomID); err != nil {
		log.Printf("notifications: dm %s -> user %s failed: %v", dmRoomID, otherID, err)
	}
}

// NotifyAfterPersistedChatMessage is the post-WS-persist hook
// registered with initializer.OnChatMessagePersisted at boot.
// Chat messages are sent over WebSocket, not HTTP, so the existing
// HTTP-side fan-out functions never actually fire for real user
// messages. This bridges the WS persist path to the same fan-out
// logic, so DMs and ride chat both push the same way the HTTP
// endpoints (kept around as fallbacks) already would.
//
// Idempotent + cheap: a single User lookup, then dispatch to the
// existing fanOutDMNotification / fanOutRideNotifications which
// own preference gating and FCM delivery.
func NotifyAfterPersistedChatMessage(messageID, roomID, senderIDStr, content string) {
	senderID, err := uuid.Parse(senderIDStr)
	if err != nil {
		log.Printf("notifications: bad sender uuid %q: %v", senderIDStr, err)
		return
	}
	var sender models.User
	if err := database.Database.Db.
		Select("id, name").
		First(&sender, senderID).Error; err != nil {
		log.Printf("notifications: sender lookup %s: %v", senderID, err)
		return
	}
	if strings.HasPrefix(roomID, "dm_") {
		fanOutDMNotification(roomID, sender, content)
		return
	}
	rideID, err := uuid.Parse(roomID)
	if err != nil {
		log.Printf("notifications: bad ride uuid %q: %v", roomID, err)
		return
	}
	fanOutRideNotifications(rideID, sender, content)
}

// ----------------------------------------------------------------------
// Chat list
// ----------------------------------------------------------------------

// GetUserChats returns every ride chat the caller participates in
// (hosting OR booked as an accepted passenger), each annotated with the
// most recent message — all in two queries, not 1 + N like before.
//
// Fixes a long-standing bug where the previous version looked up
// `bookings.user_id` (which doesn't exist; the column is
// `passenger_id`), so passengers never saw their own chats.
func GetUserChats(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}

	// 1) Every ride the user touches — hosting OR ANY booking
	//    (pending, accepted, or rejected). Pending passengers stay
	//    in the chat list so they can message the host before being
	//    accepted; their viewer_role tells the client how to gate
	//    visibility of the rest of the thread.
	var rides []models.Ride
	if err := database.Database.Db.
		// Narrow both the ride row and the preloaded HostUser to the
		// columns the chat-list response actually emits — pre-fix
		// we were pulling the full Ride (incl. Settings JSONB,
		// VehicleInfo, four lat/lng decimals) AND the full User
		// (incl. 500-char FCMToken, DeviceID, DefaultAddress,
		// InstituteEmail, contact_number) for every row in the
		// Chats list. On a user with 20 chats that's >50KB of
		// wasted wire bytes per request.
		Select("id, host_user_id, start_location, end_location, start_time, booked_seats, total_seats, total_price, created_at, is_ongoing, is_same_gender, settings").
		Preload("HostUser", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, profile_picture_url, is_email_verified")
		}).
		Where("host_user_id = ?", user.ID).
		Or("id IN (SELECT ride_id FROM bookings WHERE passenger_id = ? AND request_status IN ?)",
			user.ID, []string{"accepted", "pending"}).
		Order("start_time desc").
		Find(&rides).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch chats"})
	}

	if len(rides) == 0 {
		return c.JSON(fiber.Map{
			"chat_rooms":       []fiber.Map{},
			"count":            0,
			"pending_requests": []fiber.Map{},
			"viewer_user_id":   user.ID.String(),
		})
	}

	rideIDs := make([]uuid.UUID, 0, len(rides))
	for _, r := range rides {
		rideIDs = append(rideIDs, r.ID)
	}

	// 2) Last message per ride — one query using a window function
	//    (DISTINCT ON in PostgreSQL is even faster but less portable).
	//    Replaces N round-trips with 1.
	type latestRow struct {
		RideID     uuid.UUID
		MessageID  uuid.UUID
		Content    string
		SenderID   uuid.UUID
		SenderName string
		CreatedAt  time.Time
	}
	var rows []latestRow
	err := database.Database.Db.Raw(`
		SELECT DISTINCT ON (m.ride_id)
		       m.ride_id,
		       m.id           AS message_id,
		       m.content,
		       m.sender_id,
		       u.name         AS sender_name,
		       m.created_at
		  FROM messages m
		  JOIN users u ON u.id = m.sender_id
		 WHERE m.ride_id IN ?
		 ORDER BY m.ride_id, m.created_at DESC
	`, rideIDs).Scan(&rows).Error
	if err != nil {
		log.Printf("GetUserChats: latest-message query failed: %v", err)
	}
	latestByRide := make(map[uuid.UUID]latestRow, len(rows))
	for _, r := range rows {
		latestByRide[r.RideID] = r
	}

	// 2a) Viewer's booking status per ride — drives the per-row
	//     viewer_role chip ("Hosting" / "Confirmed" / "Pending")
	//     on the chat list. Single batched query.
	type bookingStatusRow struct {
		RideID uuid.UUID
		Status string
	}
	viewerStatus := make(map[uuid.UUID]string, len(rides))
	{
		var statusRows []bookingStatusRow
		_ = database.Database.Db.Raw(`
			SELECT ride_id, request_status AS status
			  FROM bookings
			 WHERE passenger_id = ?
			   AND ride_id IN ?
		`, user.ID, rideIDs).Scan(&statusRows).Error
		for _, r := range statusRows {
			viewerStatus[r.RideID] = r.Status
		}
	}

	// 3) Unread count per ride — messages newer than the user's
	//    `last_read_at` for that ride (single grouped query).
	var unreadRows []struct {
		RideID uuid.UUID
		Cnt    int64
	}
	_ = database.Database.Db.Raw(`
		SELECT m.ride_id,
		       COUNT(*) AS cnt
		  FROM messages m
		  LEFT JOIN chat_reads r
		    ON r.ride_id = m.ride_id AND r.user_id = ?
		 WHERE m.ride_id IN ?
		   AND m.sender_id <> ?
		   AND m.created_at > COALESCE(r.last_read_at, 'epoch'::timestamptz)
		 GROUP BY m.ride_id
	`, user.ID, rideIDs, user.ID).Scan(&unreadRows).Error
	unreadByRide := make(map[uuid.UUID]int64, len(unreadRows))
	for _, u := range unreadRows {
		unreadByRide[u.RideID] = u.Cnt
	}

	// 4) Assemble the response. Sorted by latest activity (newest
	//    chats first) which is the obvious sort for a chat list.
	type roomEntry struct {
		Room    fiber.Map
		SortKey time.Time
	}
	rooms := make([]roomEntry, 0, len(rides))
	for _, ride := range rides {
		// Compute viewer_role from host + (cached) booking status —
		// the chat-list client renders directly off this string so
		// it doesn't have to do its own "am I host? am I confirmed?"
		// math (same pattern as viewer_state for ride endpoints).
		viewerRole := "passenger"
		if ride.HostUserID == user.ID {
			viewerRole = "host"
		} else if status, ok := viewerStatus[ride.ID]; ok {
			switch status {
			case "accepted":
				viewerRole = "confirmed_passenger"
			case "pending":
				viewerRole = "pending_passenger"
			case "rejected":
				viewerRole = "rejected_passenger"
			}
		}

		room := fiber.Map{
			"id":                       ride.ID.String(),
			"title":                    ride.StartLocation + " to " + ride.EndLocation,
			"start_location":           ride.StartLocation,
			"end_location":             ride.EndLocation,
			"start_time":               ride.StartTime.Format(time.RFC3339),
			"subtitle":                 "Trip on " + ride.StartTime.Format("Mon 2 Jan 2006"),
			"participants":             strconv.Itoa(int(ride.BookedSeats)) + " passengers",
			"host_user_id":             ride.HostUserID.String(),
			"host_user_name":           ride.HostUser.Name,
			"host_profile_picture_url": ride.HostUser.ProfilePictureURL,
			// Lets the chat list surface a lime checkmark next to
			// the host's name without a second round-trip.
			"host_is_verified":    ride.HostUser.IsEmailVerified,
			"viewer_role":         viewerRole,
			"notifications_muted": ride.Settings.NotificationsMuted,
			"unread_count":        unreadByRide[ride.ID],
		}
		sortKey := ride.CreatedAt
		if last, ok := latestByRide[ride.ID]; ok {
			room["last_message"] = fiber.Map{
				"id":        last.MessageID.String(),
				"content":   last.Content,
				"sender":    last.SenderName,
				"sender_id": last.SenderID.String(),
				"timestamp": last.CreatedAt.Format(time.RFC3339),
			}
			sortKey = last.CreatedAt
		}
		rooms = append(rooms, roomEntry{Room: room, SortKey: sortKey})
	}
	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].SortKey.After(rooms[j].SortKey)
	})

	result := make([]fiber.Map, len(rooms))
	for i, r := range rooms {
		result[i] = r.Room
	}

	// Pending requests — for each hosted ride, surface every pending
	// passenger as its own row so the host can open a 1:1 DM with
	// them before deciding to accept. Pending passengers themselves
	// already see this thread via their own viewer_role=pending_passenger
	// ride row above, so this section is host-only.
	pendingRequests := pendingRequestRowsForHost(user.ID, rides)

	return c.JSON(fiber.Map{
		"chat_rooms":       result,
		"count":            len(result),
		"pending_requests": pendingRequests,
		"viewer_user_id":   user.ID.String(),
	})
}

// pendingRequestRowsForHost builds the host-side "Pending requests"
// section of the chat list. Returns one row per pending booking on
// rides the user hosts, joined with the requester's identity + any
// DM messages already exchanged.
func pendingRequestRowsForHost(hostID uuid.UUID, rides []models.Ride) []fiber.Map {
	hostedRideIDs := make([]uuid.UUID, 0, len(rides))
	rideByID := make(map[uuid.UUID]models.Ride, len(rides))
	for _, r := range rides {
		if r.HostUserID == hostID {
			hostedRideIDs = append(hostedRideIDs, r.ID)
			rideByID[r.ID] = r
		}
	}
	if len(hostedRideIDs) == 0 {
		return []fiber.Map{}
	}

	type pendingRow struct {
		BookingID         uuid.UUID
		RideID            uuid.UUID
		PassengerID       uuid.UUID
		PassengerName     string
		PassengerPic      string
		PassengerVerified bool
		CreatedAt         time.Time
	}
	var pending []pendingRow
	if err := database.Database.Db.Raw(`
		SELECT b.id              AS booking_id,
		       b.ride_id,
		       b.passenger_id,
		       u.name             AS passenger_name,
		       u.profile_picture_url AS passenger_pic,
		       u.is_email_verified  AS passenger_verified,
		       b.created_at
		  FROM bookings b
		  JOIN users u ON u.id = b.passenger_id
		 WHERE b.ride_id IN ?
		   AND b.request_status = 'pending'
		 ORDER BY b.created_at DESC
	`, hostedRideIDs).Scan(&pending).Error; err != nil {
		log.Printf("pendingRequestRowsForHost: %v", err)
		return []fiber.Map{}
	}
	if len(pending) == 0 {
		return []fiber.Map{}
	}

	// Batch-fetch latest DM message + unread count per requester.
	dmRoomIDs := make([]string, 0, len(pending))
	for _, p := range pending {
		rid := dmRoomID(hostID, p.PassengerID)
		dmRoomIDs = append(dmRoomIDs, rid)
	}

	type dmLatestRow struct {
		DMRoomID  string
		Content   string
		SenderID  uuid.UUID
		CreatedAt time.Time
	}
	var latestDM []dmLatestRow
	_ = database.Database.Db.Raw(`
		SELECT DISTINCT ON (m.dm_room_id)
		       m.dm_room_id, m.content, m.sender_id, m.created_at
		  FROM messages m
		 WHERE m.dm_room_id IN ?
		 ORDER BY m.dm_room_id, m.created_at DESC
	`, dmRoomIDs).Scan(&latestDM).Error
	latestByRoom := make(map[string]dmLatestRow, len(latestDM))
	for _, r := range latestDM {
		latestByRoom[r.DMRoomID] = r
	}

	// Unread = DM messages newer than chat_reads.last_read_at,
	// excluding ones the host themselves sent.
	type unreadRow struct {
		DMRoomID string
		Cnt      int64
	}
	var unreadRows []unreadRow
	_ = database.Database.Db.Raw(`
		SELECT m.dm_room_id, COUNT(*) AS cnt
		  FROM messages m
		  LEFT JOIN chat_reads r
		    ON r.dm_room_id = m.dm_room_id AND r.user_id = ?
		 WHERE m.dm_room_id IN ?
		   AND m.sender_id <> ?
		   AND m.created_at > COALESCE(r.last_read_at, 'epoch'::timestamptz)
		 GROUP BY m.dm_room_id
	`, hostID, dmRoomIDs, hostID).Scan(&unreadRows).Error
	unreadByRoom := make(map[string]int64, len(unreadRows))
	for _, u := range unreadRows {
		unreadByRoom[u.DMRoomID] = u.Cnt
	}

	out := make([]fiber.Map, 0, len(pending))
	for _, p := range pending {
		ride := rideByID[p.RideID]
		rid := dmRoomID(hostID, p.PassengerID)
		row := fiber.Map{
			"booking_id":                    p.BookingID.String(),
			"ride_id":                       p.RideID.String(),
			"dm_room_id":                    rid,
			"requester_id":                  p.PassengerID.String(),
			"requester_name":                p.PassengerName,
			"requester_profile_picture_url": p.PassengerPic,
			"requester_is_verified":         p.PassengerVerified,
			"ride_start_location":           ride.StartLocation,
			"ride_end_location":             ride.EndLocation,
			"ride_start_time":               ride.StartTime.Format(time.RFC3339),
			"requested_at":                  p.CreatedAt.Format(time.RFC3339),
			"unread_count":                  unreadByRoom[rid],
		}
		if last, ok := latestByRoom[rid]; ok {
			row["last_message"] = fiber.Map{
				"content":   last.Content,
				"sender_id": last.SenderID.String(),
				"timestamp": last.CreatedAt.Format(time.RFC3339),
			}
		}
		out = append(out, row)
	}
	return out
}

// dmRoomID returns the canonical DM room id between two users. The
// UUIDs are string-sorted so both sides resolve to the same room.
func dmRoomID(a, b uuid.UUID) string {
	as, bs := a.String(), b.String()
	if as < bs {
		return "dm_" + as + "_" + bs
	}
	return "dm_" + bs + "_" + as
}

var errInvalidDMRoomID = errors.New("invalid dm room id format")

func parseDMRoomID(roomID string) (uuid.UUID, uuid.UUID, error) {
	if !strings.HasPrefix(roomID, "dm_") {
		return uuid.Nil, uuid.Nil, errInvalidDMRoomID
	}
	parts := strings.SplitN(strings.TrimPrefix(roomID, "dm_"), "_", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return uuid.Nil, uuid.Nil, errInvalidDMRoomID
	}
	a, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, uuid.Nil, errInvalidDMRoomID
	}
	b, err := uuid.Parse(parts[1])
	if err != nil || a == b {
		return uuid.Nil, uuid.Nil, errInvalidDMRoomID
	}
	return a, b, nil
}

// canAccessDMRoom gates direct messages to real UniPool relationships:
// the caller must be one of the two encoded users, and one of them
// must host a ride that the other has requested. That keeps the
// pending-request DM feature from becoming an arbitrary user-to-user
// messaging backchannel.
func canAccessDMRoom(userID uuid.UUID, roomID string) (uuid.UUID, bool, error) {
	a, b, err := parseDMRoomID(roomID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if userID != a && userID != b {
		return uuid.Nil, false, nil
	}

	otherID := b
	if userID == b {
		otherID = a
	}

	var count int64
	err = database.Database.Db.Raw(`
		SELECT COUNT(*)
		  FROM bookings b
		  JOIN rides r ON r.id = b.ride_id
		 WHERE b.deleted_at IS NULL
		   AND r.deleted_at IS NULL
		   AND (
		        (r.host_user_id = ? AND b.passenger_id = ?)
		     OR (r.host_user_id = ? AND b.passenger_id = ?)
		   )
	`, userID, otherID, otherID, userID).Scan(&count).Error
	if err != nil {
		return otherID, false, err
	}
	return otherID, count > 0, nil
}

// ----------------------------------------------------------------------
// WebSocket handler & debug
// ----------------------------------------------------------------------

func WebSocketHandler(c *websocket.Conn) {
	userID := c.Query("user_id")
	roomID := c.Query("room_id")
	token := c.Query("token")

	if userID == "" || roomID == "" {
		log.Printf("WebSocket connection rejected: missing user_id or room_id")
		c.WriteMessage(websocket.CloseMessage, []byte("missing user_id or room_id"))
		c.Close()
		return
	}
	if token == "" {
		log.Printf("WebSocket connection rejected: missing token")
		c.WriteMessage(websocket.CloseMessage, []byte("missing token"))
		c.Close()
		return
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		log.Printf("WebSocket connection rejected: invalid user_id format")
		c.WriteMessage(websocket.CloseMessage, []byte("invalid user_id format"))
		c.Close()
		return
	}

	if initializer.FirebaseApp == nil {
		log.Printf("WebSocket connection rejected: Firebase not initialized")
		c.WriteMessage(websocket.CloseMessage, []byte("auth unavailable"))
		c.Close()
		return
	}
	authClient, err := initializer.FirebaseApp.Auth(context.Background())
	if err != nil {
		log.Printf("WebSocket connection rejected: Firebase Auth error: %v", err)
		c.WriteMessage(websocket.CloseMessage, []byte("auth unavailable"))
		c.Close()
		return
	}
	decodedToken, err := authClient.VerifyIDToken(context.Background(), token)
	if err != nil || decodedToken == nil || decodedToken.Claims == nil {
		log.Printf("WebSocket connection rejected: invalid token: %v", err)
		c.WriteMessage(websocket.CloseMessage, []byte("invalid token"))
		c.Close()
		return
	}
	email, ok := decodedToken.Claims["email"].(string)
	if !ok || strings.TrimSpace(email) == "" {
		log.Printf("WebSocket connection rejected: token missing email")
		c.WriteMessage(websocket.CloseMessage, []byte("invalid token"))
		c.Close()
		return
	}

	var socketUser models.User
	if err := database.Database.Db.
		Select("id", "name", "profile_picture_url").
		Where("email = ?", email).
		First(&socketUser).Error; err != nil {
		log.Printf("WebSocket connection rejected: user lookup failed: %v", err)
		c.WriteMessage(websocket.CloseMessage, []byte("user lookup failed"))
		c.Close()
		return
	}
	if socketUser.ID != userUUID {
		log.Printf("WebSocket connection rejected: token user %s tried to attach as %s", socketUser.ID, userID)
		c.WriteMessage(websocket.CloseMessage, []byte("user mismatch"))
		c.Close()
		return
	}

	if strings.HasPrefix(roomID, "dm_") {
		if _, allowed, err := canAccessDMRoom(socketUser.ID, roomID); err != nil {
			if errors.Is(err, errInvalidDMRoomID) {
				log.Printf("WebSocket connection rejected: invalid DM room_id format: %s", roomID)
				c.WriteMessage(websocket.CloseMessage, []byte("invalid DM room_id format"))
			} else {
				log.Printf("WebSocket connection rejected: DM membership lookup failed: %v", err)
				c.WriteMessage(websocket.CloseMessage, []byte("membership lookup failed"))
			}
			c.Close()
			return
		} else if !allowed {
			log.Printf("WebSocket connection rejected: user %s not allowed in DM room %s", userID, roomID)
			c.WriteMessage(websocket.CloseMessage, []byte("not a participant"))
			c.Close()
			return
		}
	} else {
		rideUUID, err := uuid.Parse(roomID)
		if err != nil {
			log.Printf("WebSocket connection rejected: invalid room_id format")
			c.WriteMessage(websocket.CloseMessage, []byte("invalid room_id format"))
			c.Close()
			return
		}
		// Only host + accepted can join the group ride room. Pending
		// requesters go through DM rooms instead.
		role, _, lookupErr := getRideViewerRole(socketUser.ID, rideUUID)
		if lookupErr != nil {
			log.Printf("WebSocket connection rejected: membership lookup failed: %v", lookupErr)
			c.WriteMessage(websocket.CloseMessage, []byte("membership lookup failed"))
			c.Close()
			return
		}
		if role != roleHost && role != roleAccepted {
			log.Printf("WebSocket connection rejected: user %s role=%s not allowed in ride %s", userID, role, roomID)
			c.WriteMessage(websocket.CloseMessage, []byte("not a participant"))
			c.Close()
			return
		}
	}

	log.Printf("WebSocket connection established for user %s in room %s", userID, roomID)
	initializer.NewClient(c, userID, roomID, socketUser.Name, socketUser.ProfilePictureURL)
	select {}
}

func GetActiveConnections(c *fiber.Ctx) error {
	hub := initializer.GetChatHub()
	if hub == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "WebSocket hub not initialized"})
	}

	roomStats, totalConnections := hub.RoomStats()

	return c.JSON(fiber.Map{
		"total_connections": totalConnections,
		"room_stats":        roomStats,
		"timestamp":         time.Now().Format(time.RFC3339),
	})
}

// ----------------------------------------------------------------------
// Mark-as-read
// ----------------------------------------------------------------------

// MarkRideRead bumps the caller's `last_read_at` for a ride chat to
// "now". Used by the client when the user opens a chat (so unread
// badges stay accurate without the frontend having to track them).
func MarkRideRead(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}

	rideID := c.Params("ride_id")
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
	}

	// Membership check: only ride participants (host + accepted +
	// pending requesters) get to bump the read cursor. Without this
	// gate an attacker who knows a ride_id can spam read-marks
	// against arbitrary rides — harmless in isolation but it (a)
	// leaks "the caller is acknowledged in this ride" through the
	// upsert side effect, and (b) lets a stranger silently dirty
	// the read-state table for arbitrary users. Mirrors the same
	// guard SendMessage and the WebSocket attach already use.
	role, _, roleErr := getRideViewerRole(user.ID, rideUUID)
	if roleErr != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "membership lookup failed"})
	}
	if role != roleHost && role != roleAccepted && role != rolePending {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this ride"})
	}

	now := time.Now()
	read := models.ChatRead{
		UserID:     user.ID,
		RideID:     &rideUUID,
		LastReadAt: now,
	}
	// Upsert (UserID, RideID) — bumps the timestamp if a row already
	// exists, inserts otherwise.
	if err := database.Database.Db.Exec(`
		INSERT INTO chat_reads (id, user_id, ride_id, last_read_at, created_at, updated_at)
		VALUES (gen_random_uuid(), ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, ride_id) DO UPDATE SET last_read_at = EXCLUDED.last_read_at, updated_at = EXCLUDED.updated_at
	`, read.UserID, rideUUID, now, now, now).Error; err != nil {
		log.Printf("MarkRideRead failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to mark read"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// MarkDMRead is the DM analogue of MarkRideRead. Bumps the caller's
// last_read_at for a specific dm_room_id so the chat-list unread
// badges (host pending-requests, pending requester's own DM) update
// the moment the user opens the thread.
func MarkDMRead(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	dmRoomID := c.Params("dm_room_id")
	if _, allowed, err := canAccessDMRoom(user.ID, dmRoomID); err != nil {
		if errors.Is(err, errInvalidDMRoomID) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid dm room id format"})
		}
		log.Printf("MarkDMRead: dm access lookup failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "dm access lookup failed"})
	} else if !allowed {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this dm"})
	}

	now := time.Now()
	// Upsert against the unique index `idx_chat_reads_user_dm_v2`
	// (user_id, dm_room_id) installed by migration 00002. NULL
	// dm_room_id rows (the ride-chat case) hit the separate
	// idx_chat_reads_user_ride constraint and never reach this
	// handler.
	if err := database.Database.Db.Exec(`
		INSERT INTO chat_reads (id, user_id, dm_room_id, last_read_at, created_at, updated_at)
		VALUES (gen_random_uuid(), ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, dm_room_id)
		DO UPDATE SET last_read_at = EXCLUDED.last_read_at, updated_at = EXCLUDED.updated_at
	`, user.ID, dmRoomID, now, now, now).Error; err != nil {
		log.Printf("MarkDMRead failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to mark read"})
	}
	return c.JSON(fiber.Map{"ok": true})
}
