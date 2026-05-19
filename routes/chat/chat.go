package chat

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
	"github.com/google/uuid"
	"unipool-backend/database"
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

// transformMessage serialises a `models.Message` into the wire shape the
// frontend expects. Kept in one place so every endpoint emits identical
// keys — the old code had four different shapes which forced the JS
// client to normalise six field name variants.
func transformMessage(msg *models.Message) fiber.Map {
	out := fiber.Map{
		"id":        msg.ID.String(),
		"content":   msg.Content,
		"sender_id": msg.SenderID.String(),
		"timestamp": msg.CreatedAt.Format(time.RFC3339),
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

	limit, before, hasBefore := parsePagination(c)

	query := database.Database.Db.
		Preload("Sender").
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
	if !strings.HasPrefix(dmRoomID, "dm_") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid dm room id format"})
	}

	limit, before, hasBefore := parsePagination(c)

	query := database.Database.Db.
		Preload("Sender").
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

// SendDMMessage mirrors SendMessage for direct messages between two
// users. The room ID encodes both UUIDs (`dm_<a>_<b>`); we extract the
// recipient and FCM them.
func SendDMMessage(c *fiber.Ctx) error {
	dmRoomID := c.Params("dm_room_id")
	if !strings.HasPrefix(dmRoomID, "dm_") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid dm room id format"})
	}

	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
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

	var ride models.Ride
	if err := database.Database.Db.Preload("HostUser").First(&ride, rideUUID).Error; err != nil {
		log.Printf("notifications: ride %s lookup failed: %v", rideUUID, err)
		return
	}
	if ride.Settings.NotificationsMuted {
		log.Printf("notifications: ride %s muted — skipping fan-out", rideUUID)
		return
	}

	var bookings []models.Booking
	if err := database.Database.Db.
		Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").
		Find(&bookings).Error; err != nil {
		log.Printf("notifications: bookings lookup for ride %s failed: %v", rideUUID, err)
		return
	}

	// Build the recipient set: host + every accepted passenger, minus
	// the sender. Use a map so duplicates can't double-notify.
	recipients := map[uuid.UUID]struct{}{}
	if ride.HostUserID != sender.ID {
		recipients[ride.HostUserID] = struct{}{}
	}
	for _, b := range bookings {
		if b.PassengerID != sender.ID {
			recipients[b.PassengerID] = struct{}{}
		}
	}

	rideRoute := ride.StartLocation + " to " + ride.EndLocation

	var wg sync.WaitGroup
	for userID := range recipients {
		wg.Add(1)
		uid := userID
		go func() {
			defer wg.Done()
			if err := fcm.SendChatMessageNotification(uid, sender.Name, content, rideRoute, rideUUID); err != nil {
				log.Printf("notifications: ride %s -> user %s failed: %v", rideUUID, uid, err)
			}
		}()
	}
	wg.Wait()
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
	if err := fcm.SendDirectMessageNotification(otherID, sender.Name, content); err != nil {
		log.Printf("notifications: dm %s -> user %s failed: %v", dmRoomID, otherID, err)
	}
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
		Preload("HostUser").
		Where("host_user_id = ?", user.ID).
		Or("id IN (SELECT ride_id FROM bookings WHERE passenger_id = ? AND request_status IN ?)",
			user.ID, []string{"accepted", "pending"}).
		Order("start_time desc").
		Find(&rides).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch chats"})
	}

	if len(rides) == 0 {
		return c.JSON(fiber.Map{"chat_rooms": []fiber.Map{}, "count": 0})
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
			"id":                  ride.ID.String(),
			"title":               ride.StartLocation + " to " + ride.EndLocation,
			"start_location":      ride.StartLocation,
			"end_location":        ride.EndLocation,
			"start_time":          ride.StartTime.Format(time.RFC3339),
			"subtitle":            "Trip on " + ride.StartTime.Format("Mon 2 Jan 2006"),
			"participants":        strconv.Itoa(int(ride.BookedSeats)) + " passengers",
			"host_user_id":        ride.HostUserID.String(),
			"host_user_name":      ride.HostUser.Name,
			"host_profile_picture_url": ride.HostUser.ProfilePictureURL,
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
	// In-memory sort by latest activity desc — small N, cheaper than
	// reshuffling on the database side.
	for i := 1; i < len(rooms); i++ {
		for j := i; j > 0 && rooms[j].SortKey.After(rooms[j-1].SortKey); j-- {
			rooms[j], rooms[j-1] = rooms[j-1], rooms[j]
		}
	}

	result := make([]fiber.Map, len(rooms))
	for i, r := range rooms {
		result[i] = r.Room
	}
	return c.JSON(fiber.Map{"chat_rooms": result, "count": len(result)})
}

// ----------------------------------------------------------------------
// WebSocket handler & debug
// ----------------------------------------------------------------------

func WebSocketHandler(c *websocket.Conn) {
	log.Printf("WebSocketHandler invoked. Query: %s", c.Query(""))
	userID := c.Query("user_id")
	roomID := c.Query("room_id")

	if userID == "" || roomID == "" {
		log.Printf("WebSocket connection rejected: missing user_id or room_id")
		c.WriteMessage(websocket.CloseMessage, []byte("missing user_id or room_id"))
		c.Close()
		return
	}

	if _, err := uuid.Parse(userID); err != nil {
		log.Printf("WebSocket connection rejected: invalid user_id format")
		c.WriteMessage(websocket.CloseMessage, []byte("invalid user_id format"))
		c.Close()
		return
	}

	if strings.HasPrefix(roomID, "dm_") {
		userIds := strings.Split(strings.TrimPrefix(roomID, "dm_"), "_")
		if len(userIds) != 2 {
			log.Printf("WebSocket connection rejected: invalid DM room_id format: %s", roomID)
			c.WriteMessage(websocket.CloseMessage, []byte("invalid DM room_id format"))
			c.Close()
			return
		}
		for _, uid := range userIds {
			if _, err := uuid.Parse(uid); err != nil {
				log.Printf("WebSocket connection rejected: invalid user ID in DM room: %s", uid)
				c.WriteMessage(websocket.CloseMessage, []byte("invalid user ID in DM room"))
				c.Close()
				return
			}
		}
		log.Printf("Valid DM room ID: %s with users: %s, %s", roomID, userIds[0], userIds[1])
	} else {
		if _, err := uuid.Parse(roomID); err != nil {
			log.Printf("WebSocket connection rejected: invalid room_id format")
			c.WriteMessage(websocket.CloseMessage, []byte("invalid room_id format"))
			c.Close()
			return
		}
	}

	log.Printf("WebSocket connection established for user %s in room %s", userID, roomID)
	initializer.NewClient(c, userID, roomID)
	select {}
}

func GetActiveConnections(c *fiber.Ctx) error {
	hub := initializer.GetChatHub()
	if hub == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "WebSocket hub not initialized"})
	}

	roomStats := make(map[string]int)
	totalConnections := 0
	for roomID, clients := range hub.Rooms {
		roomStats[roomID] = len(clients)
		totalConnections += len(clients)
	}

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
