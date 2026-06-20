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
	"unipool-backend/database"
	"unipool-backend/helpers"
	"unipool-backend/initializer"
	"unipool-backend/middleware"
	"unipool-backend/models"
	"unipool-backend/services"
)

// ----------------------------------------------------------------------
// Shared helpers.
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
		   AND b.deleted_at IS NULL
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

// transformMessage serializes a models.Message into the frontend wire shape.
func transformMessage(msg *models.Message) fiber.Map {
	// Clients use kind to choose text bubbles vs system cards.
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

type messageRow struct {
	ID                      uuid.UUID
	RideID                  *uuid.UUID
	DMRoomID                *string
	SenderID                uuid.UUID
	Content                 string
	Kind                    string
	Metadata                models.MessageMetadata
	CreatedAt               time.Time
	SenderName              string
	SenderProfilePictureURL string
	ReadBy                  []string
}

func splitReadByCSV(csv string) []string {
	if csv == "" {
		return []string{}
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func transformMessageRow(row *messageRow) fiber.Map {
	kind := row.Kind
	if kind == "" {
		kind = models.MessageKindUser
	}
	metadata := row.Metadata
	if metadata == nil {
		metadata = models.MessageMetadata{}
	}
	out := fiber.Map{
		"id":        row.ID.String(),
		"content":   row.Content,
		"sender_id": row.SenderID.String(),
		"timestamp": row.CreatedAt.Format(time.RFC3339),
		"kind":      kind,
		"metadata":  metadata,
		"read_by":   row.ReadBy,
		"sender": fiber.Map{
			"name":                row.SenderName,
			"profile_picture_url": row.SenderProfilePictureURL,
		},
	}
	if row.RideID != nil {
		out["ride_id"] = row.RideID.String()
	}
	if row.DMRoomID != nil {
		out["dm_room_id"] = *row.DMRoomID
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

func shouldMarkReadFromQuery(c *fiber.Ctx) bool {
	raw := strings.ToLower(strings.TrimSpace(c.Query("mark_read")))
	return raw == "1" || raw == "true" || raw == "yes"
}

func upsertRideReadCursor(userID, rideID uuid.UUID, now time.Time) error {
	return database.Database.Db.Exec(`
		INSERT INTO chat_reads (id, user_id, ride_id, last_read_at, created_at, updated_at)
		VALUES (gen_random_uuid(), ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, ride_id) DO UPDATE SET last_read_at = EXCLUDED.last_read_at, updated_at = EXCLUDED.updated_at
	`, userID.String(), rideID.String(), now, now, now).Error
}

func upsertDMReadCursor(userID uuid.UUID, dmRoomID string, now time.Time) error {
	return database.Database.Db.Exec(`
		INSERT INTO chat_reads (id, user_id, dm_room_id, last_read_at, created_at, updated_at)
		VALUES (gen_random_uuid(), ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, dm_room_id)
		DO UPDATE SET last_read_at = EXCLUDED.last_read_at, updated_at = EXCLUDED.updated_at
	`, userID.String(), dmRoomID, now, now, now).Error
}

// ----------------------------------------------------------------------
// Message history (paginated).
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

	limit, before, hasBefore := parsePagination(c)

	type rideMessagePageRow struct {
		ViewerRole              string                 `gorm:"column:viewer_role"`
		ID                      *uuid.UUID             `gorm:"column:id"`
		RideID                  *uuid.UUID             `gorm:"column:ride_id"`
		DMRoomID                *string                `gorm:"column:dm_room_id"`
		SenderID                *uuid.UUID             `gorm:"column:sender_id"`
		Content                 *string                `gorm:"column:content"`
		Kind                    *string                `gorm:"column:kind"`
		Metadata                models.MessageMetadata `gorm:"column:metadata"`
		CreatedAt               *time.Time             `gorm:"column:created_at"`
		SenderName              *string                `gorm:"column:sender_name"`
		SenderProfilePictureURL *string                `gorm:"column:sender_profile_picture_url"`
		ReadByCSV               *string                `gorm:"column:read_by_csv"`
	}

	beforeClause := ""
	args := []any{
		user.ID,           // r.host_user_id = ?
		user.ID,           // b.passenger_id = ?
		rideUUID,          // r.id = ?
		rideUUID.String(), // cr.ride_id = ? (chat_reads stores ride ids as strings)
		rideUUID.String(), // m.ride_id = ? (messages stores ride ids as strings)
	}
	if hasBefore {
		beforeClause = "AND m.created_at < ?"
		args = append(args, before)
	}
	args = append(args, limit)

	// Resolve membership and page messages in one statement; the LEFT JOIN keeps
	// authorized empty chats distinguishable from unauthorized viewers.
	var rows []rideMessagePageRow
	if err := database.Database.Db.Raw(`
		WITH membership AS (
			SELECT
				CASE
					WHEN r.host_user_id = ? THEN '`+roleHost+`'
					WHEN b.request_status IS NOT NULL THEN b.request_status
					ELSE '`+roleNone+`'
				END AS viewer_role
			  FROM rides r
			  LEFT JOIN bookings b
			    ON b.ride_id = r.id
			   AND b.passenger_id = ?
			   AND b.deleted_at IS NULL
			 WHERE r.id = ?
			 LIMIT 1
		),
		page AS (
			SELECT
				m.id,
				m.ride_id::UUID AS ride_id,
				m.dm_room_id,
				m.sender_id,
				m.content,
				m.kind,
				m.metadata,
				m.created_at,
				u.name AS sender_name,
				u.profile_picture_url AS sender_profile_picture_url,
				COALESCE((
					SELECT string_agg(cr.user_id::STRING, ',')
					  FROM chat_reads cr
					 WHERE cr.deleted_at IS NULL
					   AND cr.ride_id = ?
					   AND cr.user_id <> m.sender_id::STRING
					   AND cr.last_read_at >= m.created_at
				), '') AS read_by_csv
			  FROM messages m
			  JOIN users u ON u.id = m.sender_id
			 WHERE m.ride_id = ?
			   AND m.deleted_at IS NULL
			   `+beforeClause+`
			   AND EXISTS (
					SELECT 1
					  FROM membership
					 WHERE viewer_role IN ('`+roleHost+`', '`+roleAccepted+`')
			   )
			 ORDER BY m.created_at DESC
			 LIMIT ?
		)
		SELECT
			membership.viewer_role,
			page.id,
			page.ride_id,
			page.dm_room_id,
			page.sender_id,
			page.content,
			page.kind,
			page.metadata,
			page.created_at,
			page.sender_name,
			page.sender_profile_picture_url,
			page.read_by_csv
		  FROM membership
		  LEFT JOIN page ON TRUE
	`, args...).Scan(&rows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch messages"})
	}
	if len(rows) == 0 || (rows[0].ViewerRole != roleHost && rows[0].ViewerRole != roleAccepted) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this ride"})
	}

	messages := make([]messageRow, 0, len(rows))
	for i := range rows {
		row := rows[i]
		if row.ID == nil || row.SenderID == nil || row.CreatedAt == nil {
			continue
		}
		content := ""
		if row.Content != nil {
			content = *row.Content
		}
		kind := ""
		if row.Kind != nil {
			kind = *row.Kind
		}
		senderName := ""
		if row.SenderName != nil {
			senderName = *row.SenderName
		}
		senderProfilePictureURL := ""
		if row.SenderProfilePictureURL != nil {
			senderProfilePictureURL = *row.SenderProfilePictureURL
		}
		readBy := []string{}
		if row.ReadByCSV != nil {
			readBy = splitReadByCSV(*row.ReadByCSV)
		}
		messages = append(messages, messageRow{
			ID:                      *row.ID,
			RideID:                  row.RideID,
			DMRoomID:                row.DMRoomID,
			SenderID:                *row.SenderID,
			Content:                 content,
			Kind:                    kind,
			Metadata:                row.Metadata,
			CreatedAt:               *row.CreatedAt,
			SenderName:              senderName,
			SenderProfilePictureURL: senderProfilePictureURL,
			ReadBy:                  readBy,
		})
	}

	// Reverse to chronological order on the wire — easier for the
	// client to render in a top-down list.
	transformed := make([]fiber.Map, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		transformed = append(transformed, transformMessageRow(&messages[i]))
	}

	if shouldMarkReadFromQuery(c) {
		if err := upsertRideReadCursor(user.ID, rideUUID, time.Now()); err != nil {
			log.Printf("GetRideMessages mark_read failed: %v", err)
		}
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
	a, b, err := parseDMRoomID(dmRoomID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid dm room id format"})
	}
	if user.ID != a && user.ID != b {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this dm"})
	}
	otherID := b
	if user.ID == b {
		otherID = a
	}

	limit, before, hasBefore := parsePagination(c)

	type dmMessagePageRow struct {
		Allowed                 bool                   `gorm:"column:allowed"`
		ID                      *uuid.UUID             `gorm:"column:id"`
		RideID                  *uuid.UUID             `gorm:"column:ride_id"`
		DMRoomID                *string                `gorm:"column:dm_room_id"`
		SenderID                *uuid.UUID             `gorm:"column:sender_id"`
		Content                 *string                `gorm:"column:content"`
		Kind                    *string                `gorm:"column:kind"`
		Metadata                models.MessageMetadata `gorm:"column:metadata"`
		CreatedAt               *time.Time             `gorm:"column:created_at"`
		SenderName              *string                `gorm:"column:sender_name"`
		SenderProfilePictureURL *string                `gorm:"column:sender_profile_picture_url"`
		ReadByCSV               *string                `gorm:"column:read_by_csv"`
	}

	beforeClause := ""
	args := []any{user.ID, otherID, otherID, user.ID, dmRoomID, dmRoomID}
	if hasBefore {
		beforeClause = "AND m.created_at < ?"
		args = append(args, before)
	}
	args = append(args, limit)

	// Access gate and message page in one statement. The access CTE
	// always returns exactly one row, so an allowed-but-empty DM
	// returns count 0 while a non-relationship still returns 403.
	var rows []dmMessagePageRow
	if err := database.Database.Db.Raw(`
		WITH access AS (
			SELECT EXISTS (
				SELECT 1
				  FROM bookings b
				  JOIN rides r ON r.id = b.ride_id
				 WHERE b.deleted_at IS NULL
				   AND r.deleted_at IS NULL
				   AND (
				        (r.host_user_id = ? AND b.passenger_id = ?)
				     OR (r.host_user_id = ? AND b.passenger_id = ?)
				   )
				 LIMIT 1
			) AS allowed
		),
		page AS (
			SELECT
				m.id,
				NULLIF(m.ride_id, '')::UUID AS ride_id,
				m.dm_room_id,
				m.sender_id,
				m.content,
				m.kind,
				m.metadata,
				m.created_at,
				u.name AS sender_name,
				u.profile_picture_url AS sender_profile_picture_url,
				COALESCE((
					SELECT string_agg(cr.user_id::STRING, ',')
					  FROM chat_reads cr
					 WHERE cr.deleted_at IS NULL
					   AND cr.dm_room_id = ?
					   AND cr.user_id <> m.sender_id::STRING
					   AND cr.last_read_at >= m.created_at
				), '') AS read_by_csv
			  FROM messages m
			  JOIN users u ON u.id = m.sender_id
			 WHERE m.dm_room_id = ?
			   AND m.deleted_at IS NULL
			   `+beforeClause+`
			   AND EXISTS (SELECT 1 FROM access WHERE allowed)
			 ORDER BY m.created_at DESC
			 LIMIT ?
		)
		SELECT
			access.allowed,
			page.id,
			page.ride_id,
			page.dm_room_id,
			page.sender_id,
			page.content,
			page.kind,
			page.metadata,
			page.created_at,
			page.sender_name,
			page.sender_profile_picture_url,
			page.read_by_csv
		  FROM access
		  LEFT JOIN page ON TRUE
	`, args...).Scan(&rows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch messages"})
	}
	if len(rows) == 0 || !rows[0].Allowed {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not a participant in this dm"})
	}

	messages := make([]messageRow, 0, len(rows))
	for i := range rows {
		row := rows[i]
		if row.ID == nil || row.SenderID == nil || row.CreatedAt == nil {
			continue
		}
		content := ""
		if row.Content != nil {
			content = *row.Content
		}
		kind := ""
		if row.Kind != nil {
			kind = *row.Kind
		}
		senderName := ""
		if row.SenderName != nil {
			senderName = *row.SenderName
		}
		senderProfilePictureURL := ""
		if row.SenderProfilePictureURL != nil {
			senderProfilePictureURL = *row.SenderProfilePictureURL
		}
		readBy := []string{}
		if row.ReadByCSV != nil {
			readBy = splitReadByCSV(*row.ReadByCSV)
		}
		messages = append(messages, messageRow{
			ID:                      *row.ID,
			RideID:                  row.RideID,
			DMRoomID:                row.DMRoomID,
			SenderID:                *row.SenderID,
			Content:                 content,
			Kind:                    kind,
			Metadata:                row.Metadata,
			CreatedAt:               *row.CreatedAt,
			SenderName:              senderName,
			SenderProfilePictureURL: senderProfilePictureURL,
			ReadBy:                  readBy,
		})
	}

	transformed := make([]fiber.Map, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		transformed = append(transformed, transformMessageRow(&messages[i]))
	}

	if shouldMarkReadFromQuery(c) {
		if err := upsertDMReadCursor(user.ID, dmRoomID, time.Now()); err != nil {
			log.Printf("GetDMMessages mark_read failed: %v", err)
		}
	}

	return c.JSON(fiber.Map{
		"messages": transformed,
		"count":    len(transformed),
		"has_more": len(messages) == limit,
	})
}

// ----------------------------------------------------------------------
// Send.
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

	// Broadcast before returning so open chat clients receive the message promptly.
	broadcastChatMessage(rideID, &msg, body.TempID, user)

	// Async push fan-out must not retain request context references.
	go fanOutRideNotifications(rideUUID, user, body.Content)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": transformMessage(&msg),
	})
}

// PostSystemMessage inserts a non-user message into a ride group chat,
// broadcasts it, and optionally fans out push notifications.
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

// fanOutRideNotificationsSystem sends ride-scoped system-message pushes to
// host and accepted passengers, excluding the actor.
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
	tokens, err := services.LoadAllowedFCMTokens(ids, helpers.NotifChatMessages, rideUUID)
	if err != nil {
		log.Printf("system fanout: token lookup failed: %v", err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	fcm.SendBatch(tokens, title, body, data)
}

// SendDMMessage mirrors SendMessage for direct-message rooms.
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

	// Ride row and accepted bookings are independent, so load them concurrently.
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

	// Resolve notification preferences and tokens as one set-oriented batch.
	recipientIDs := make([]uuid.UUID, 0, len(recipients))
	for uid := range recipients {
		recipientIDs = append(recipientIDs, uid)
	}

	// Suppress push banners for recipients currently connected to the room.
	if hub := initializer.GetChatHub(); hub != nil {
		recipientStrs := make([]string, 0, len(recipientIDs))
		for _, id := range recipientIDs {
			recipientStrs = append(recipientStrs, id.String())
		}
		offline := hub.FilterUsersNotInRoom(rideUUID.String(), recipientStrs)
		offlineSet := make(map[string]struct{}, len(offline))
		for _, s := range offline {
			offlineSet[s] = struct{}{}
		}
		filtered := recipientIDs[:0]
		for _, id := range recipientIDs {
			if _, ok := offlineSet[id.String()]; ok {
				filtered = append(filtered, id)
			}
		}
		recipientIDs = filtered
	}
	if len(recipientIDs) == 0 {
		return
	}

	tokens, err := services.LoadAllowedFCMTokens(recipientIDs, helpers.NotifChatMessages, rideUUID)
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
	// If the recipient is currently looking at this DM (their
	// ChatMessages WebSocket is open to this dmRoomID), skip the
	// push — the message already appeared in their open conversation
	// and an FCM banner on top would be duplicative noise.
	if hub := initializer.GetChatHub(); hub != nil && hub.IsUserActiveInRoom(dmRoomID, otherID.String()) {
		return
	}
	tokens, err := services.LoadAllowedFCMTokens([]uuid.UUID{otherID}, helpers.NotifDirectMessages, uuid.Nil)
	if err != nil {
		log.Printf("notifications: dm token lookup %s -> user %s failed: %v", dmRoomID, otherID, err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	title := fmt.Sprintf("New message from %s", sender.Name)
	body := fmt.Sprintf("💬 %s", content)
	if len(body) > 100 {
		body = body[:97] + "..."
	}
	fcm.SendBatch(tokens, title, body, map[string]string{
		"type":        "direct_message",
		"dm_room_id":  dmRoomID,
		"sender_id":   sender.ID.String(),
		"sender_name": sender.Name,
		"action":      "open_dm",
	})
}

// NotifyAfterPersistedChatMessage bridges WebSocket-persisted messages into
// the same FCM fan-out path used by HTTP fallback send endpoints.
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
// Chat list.
// ----------------------------------------------------------------------

// GetUserChats returns every ride chat the caller participates in, each
// annotated with latest message, unread state, and pending-request metadata.
func GetUserChats(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}

	// Every hosted or requested ride, joined to the host projection needed by
	// the chat list.
	type chatRideRow struct {
		ID                    uuid.UUID
		CreatedAt             time.Time
		HostUserID            uuid.UUID
		StartLocation         string
		EndLocation           string
		StartTime             time.Time
		BookedSeats           uint
		TotalSeats            uint
		TotalPrice            uint
		IsOngoing             uint
		IsSameGender          uint
		Settings              models.RideSettings
		HostUserName          string
		HostProfilePictureURL string
		HostIsEmailVerified   bool
	}
	var rideRows []chatRideRow
	// Hide chats for rides that are already over, matching the trips list
	// (involvedRides): a ride drops off once it's >24h past its start time
	// and isn't flagged ongoing. Filtering the source ride set here covers
	// BOTH the active chat rooms and the host's pending requests, since
	// both are derived from `rides` below.
	oneDayAgo := time.Now().UTC().Add(-24 * time.Hour)
	if err := database.Database.Db.Raw(`
		WITH viewer_ride_ids AS (
			SELECT r.id AS ride_id
			  FROM rides r
			 WHERE r.host_user_id = ?
			   AND r.deleted_at IS NULL

			UNION

			SELECT b.ride_id
			  FROM bookings b
			  JOIN rides r ON r.id = b.ride_id
			 WHERE b.deleted_at IS NULL
			   AND b.passenger_id = ?
			   AND b.request_status IN ('accepted', 'pending')
			   AND r.deleted_at IS NULL
		)
		SELECT r.id,
		       r.created_at,
		       r.host_user_id,
		       r.start_location,
		       r.end_location,
		       r.start_time,
		       r.booked_seats,
		       r.total_seats,
		       r.total_price,
		       r.is_ongoing,
		       r.is_same_gender,
		       r.settings,
		       u.name                AS host_user_name,
		       u.profile_picture_url AS host_profile_picture_url,
		       u.is_email_verified   AS host_is_email_verified
		  FROM viewer_ride_ids v
		  JOIN rides r ON r.id = v.ride_id
		  JOIN users u ON u.id = r.host_user_id
		 WHERE (r.is_ongoing = 1 OR r.start_time > ?)
		 ORDER BY r.start_time DESC
	`, user.ID, user.ID, oneDayAgo).Scan(&rideRows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch chats"})
	}

	if len(rideRows) == 0 {
		return c.JSON(fiber.Map{
			"chat_rooms":       []fiber.Map{},
			"count":            0,
			"pending_requests": []fiber.Map{},
			"viewer_user_id":   user.ID.String(),
		})
	}

	rides := make([]models.Ride, 0, len(rideRows))
	rideIDs := make([]uuid.UUID, 0, len(rideRows))
	for _, row := range rideRows {
		ride := models.Ride{
			BaseModel: models.BaseModel{
				ID:        row.ID,
				CreatedAt: row.CreatedAt,
			},
			HostUserID:    row.HostUserID,
			StartLocation: row.StartLocation,
			EndLocation:   row.EndLocation,
			StartTime:     row.StartTime,
			BookedSeats:   row.BookedSeats,
			TotalSeats:    row.TotalSeats,
			TotalPrice:    row.TotalPrice,
			IsOngoing:     row.IsOngoing,
			IsSameGender:  row.IsSameGender,
			Settings:      row.Settings,
			HostUser: models.User{
				BaseModel:         models.BaseModel{ID: row.HostUserID},
				Name:              row.HostUserName,
				ProfilePictureURL: row.HostProfilePictureURL,
				IsEmailVerified:   row.HostIsEmailVerified,
			},
		}
		rides = append(rides, ride)
		rideIDs = append(rideIDs, row.ID)
	}

	// Per-ride chat summary: latest message, viewer booking status, unread
	// count, and mute state in one set-oriented query.
	type chatSummaryRow struct {
		RideID          uuid.UUID
		MessageID       *uuid.UUID
		Content         *string
		SenderID        *uuid.UUID
		SenderName      *string
		CreatedAt       *time.Time
		Status          *string
		UnreadCount     int64
		RideMuted       bool
		GlobalChatMuted bool
	}
	var summaryRows []chatSummaryRow
	if err := database.Database.Db.Raw(`
		WITH ride_ids AS (
			SELECT id AS ride_id, id::STRING AS ride_id_text
			  FROM rides
			 WHERE id IN ?
		),
		latest AS (
			SELECT DISTINCT ON (ri.ride_id)
			       ri.ride_id,
			       m.id           AS message_id,
			       m.content,
			       m.sender_id,
			       u.name         AS sender_name,
			       m.created_at
			  FROM ride_ids ri
			  JOIN messages m ON m.ride_id = ri.ride_id_text
			  JOIN users u ON u.id = m.sender_id
			 WHERE m.deleted_at IS NULL
			 ORDER BY ri.ride_id, m.created_at DESC
		),
		viewer_status AS (
			SELECT b.ride_id, b.request_status AS status
			  FROM bookings b
			 WHERE b.deleted_at IS NULL
			   AND b.passenger_id = ?
			   AND b.ride_id IN (SELECT ride_id FROM ride_ids)
		),
		unread AS (
			SELECT ri.ride_id, COUNT(*) AS unread_count
			  FROM ride_ids ri
			  JOIN messages m ON m.ride_id = ri.ride_id_text
			  LEFT JOIN chat_reads cr
			    ON cr.ride_id = ri.ride_id_text AND cr.user_id = ?
			 WHERE m.deleted_at IS NULL
			   AND m.sender_id <> ?
			   AND m.created_at > COALESCE(cr.last_read_at, 'epoch'::timestamptz)
			 GROUP BY ri.ride_id
		),
		ride_mutes AS (
			SELECT np.ride_id, TRUE AS ride_muted
			  FROM notification_preferences np
			 WHERE np.deleted_at IS NULL
			   AND np.user_id = ?
			   AND np.category = ?
			   AND np.enabled = false
			   AND np.ride_id IN (SELECT ride_id FROM ride_ids)
		),
		global_mute AS (
			SELECT EXISTS (
				SELECT 1
				  FROM notification_preferences np
				 WHERE np.deleted_at IS NULL
				   AND np.user_id = ?
				   AND np.category = ?
				   AND np.enabled = false
				   AND np.ride_id IS NULL
			) AS global_chat_muted
		)
		SELECT ri.ride_id,
		       l.message_id,
		       l.content,
		       l.sender_id,
		       l.sender_name,
		       l.created_at,
		       vs.status,
		       COALESCE(u.unread_count, 0) AS unread_count,
		       COALESCE(rm.ride_muted, FALSE) AS ride_muted,
		       gm.global_chat_muted
		  FROM ride_ids ri
		  CROSS JOIN global_mute gm
		  LEFT JOIN latest l ON l.ride_id = ri.ride_id
		  LEFT JOIN viewer_status vs ON vs.ride_id = ri.ride_id
		  LEFT JOIN unread u ON u.ride_id = ri.ride_id
		  LEFT JOIN ride_mutes rm ON rm.ride_id = ri.ride_id
	`, rideIDs, user.ID, user.ID.String(), user.ID, user.ID, helpers.NotifChatMessages, user.ID, helpers.NotifChatMessages).Scan(&summaryRows).Error; err != nil {
		log.Printf("GetUserChats: summary query failed: %v", err)
	}
	summaryByRide := make(map[uuid.UUID]chatSummaryRow, len(summaryRows))
	for _, r := range summaryRows {
		summaryByRide[r.RideID] = r
	}

	// Assemble the response sorted by latest activity.
	type roomEntry struct {
		Room    fiber.Map
		SortKey time.Time
	}
	rooms := make([]roomEntry, 0, len(rides))
	for _, ride := range rides {
		summary := summaryByRide[ride.ID]
		// The client renders directly from viewer_role.
		viewerRole := "passenger"
		if ride.HostUserID == user.ID {
			viewerRole = "host"
		} else if summary.Status != nil {
			switch *summary.Status {
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
			"chat_name":           ride.Settings.ChatName,
			"notifications_muted": ride.Settings.NotificationsMuted || summary.GlobalChatMuted || summary.RideMuted,
			"unread_count":        summary.UnreadCount,
		}
		sortKey := ride.CreatedAt
		if summary.MessageID != nil && summary.SenderID != nil && summary.CreatedAt != nil {
			content := ""
			if summary.Content != nil {
				content = *summary.Content
			}
			senderName := ""
			if summary.SenderName != nil {
				senderName = *summary.SenderName
			}
			room["last_message"] = fiber.Map{
				"id":        summary.MessageID.String(),
				"content":   content,
				"sender":    senderName,
				"sender_id": summary.SenderID.String(),
				"timestamp": summary.CreatedAt.Format(time.RFC3339),
			}
			sortKey = *summary.CreatedAt
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
		   AND b.deleted_at IS NULL
		   AND b.request_status = 'pending'
		 ORDER BY b.created_at DESC
	`, hostedRideIDs).Scan(&pending).Error; err != nil {
		log.Printf("pendingRequestRowsForHost: %v", err)
		return []fiber.Map{}
	}
	if len(pending) == 0 {
		return []fiber.Map{}
	}

	// Batch-fetch latest DM message and unread count per requester.
	dmRoomIDs := make([]string, 0, len(pending))
	for _, p := range pending {
		rid := dmRoomID(hostID, p.PassengerID)
		dmRoomIDs = append(dmRoomIDs, rid)
	}

	type dmSummaryRow struct {
		DMRoomID    string
		Content     *string
		SenderID    *uuid.UUID
		CreatedAt   *time.Time
		UnreadCount int64
	}
	var dmSummaries []dmSummaryRow
	_ = database.Database.Db.Raw(`
		WITH room_ids AS (
			SELECT DISTINCT dm_room_id
			  FROM messages
			 WHERE dm_room_id IN ?
		),
		latest AS (
			SELECT DISTINCT ON (m.dm_room_id)
			       m.dm_room_id,
			       m.content,
			       m.sender_id,
			       m.created_at
			  FROM messages m
			 WHERE m.deleted_at IS NULL
			   AND m.dm_room_id IN ?
			 ORDER BY m.dm_room_id, m.created_at DESC
		),
		unread AS (
			SELECT m.dm_room_id, COUNT(*) AS unread_count
			  FROM messages m
			  LEFT JOIN chat_reads cr
			    ON cr.dm_room_id = m.dm_room_id AND cr.user_id = ?
			 WHERE m.deleted_at IS NULL
			   AND m.dm_room_id IN ?
			   AND m.sender_id <> ?
			   AND m.created_at > COALESCE(cr.last_read_at, 'epoch'::timestamptz)
			 GROUP BY m.dm_room_id
		)
		SELECT ri.dm_room_id,
		       l.content,
		       l.sender_id,
		       l.created_at,
		       COALESCE(u.unread_count, 0) AS unread_count
		  FROM room_ids ri
		  LEFT JOIN latest l ON l.dm_room_id = ri.dm_room_id
		  LEFT JOIN unread u ON u.dm_room_id = ri.dm_room_id
	`, dmRoomIDs, dmRoomIDs, hostID.String(), dmRoomIDs, hostID).Scan(&dmSummaries).Error
	dmSummaryByRoom := make(map[string]dmSummaryRow, len(dmSummaries))
	for _, r := range dmSummaries {
		dmSummaryByRoom[r.DMRoomID] = r
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
			"unread_count":                  dmSummaryByRoom[rid].UnreadCount,
		}
		if last := dmSummaryByRoom[rid]; last.SenderID != nil && last.CreatedAt != nil {
			content := ""
			if last.Content != nil {
				content = *last.Content
			}
			row["last_message"] = fiber.Map{
				"content":   content,
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

	var allowed bool
	err = database.Database.Db.Raw(`
		SELECT EXISTS (
			SELECT 1
		  FROM bookings b
		  JOIN rides r ON r.id = b.ride_id
		 WHERE b.deleted_at IS NULL
		   AND r.deleted_at IS NULL
		   AND (
		        (r.host_user_id = ? AND b.passenger_id = ?)
		     OR (r.host_user_id = ? AND b.passenger_id = ?)
		   )
		 LIMIT 1
		)
	`, userID, otherID, otherID, userID).Scan(&allowed).Error
	if err != nil {
		return otherID, false, err
	}
	return otherID, allowed, nil
}

// ----------------------------------------------------------------------
// WebSocket handler and debug endpoints.
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

	authCtx, cancelAuth := context.WithTimeout(context.Background(), 3*time.Second)
	socketUser, err := middleware.UserFromBearerToken(authCtx, token)
	cancelAuth()
	if err != nil {
		log.Printf("WebSocket connection rejected: auth lookup failed: %v", err)
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
// Mark-as-read.
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

	// Only participants can create read cursors for a ride.
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
	if err := upsertRideReadCursor(read.UserID, rideUUID, now); err != nil {
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
	if err := upsertDMReadCursor(user.ID, dmRoomID, now); err != nil {
		log.Printf("MarkDMRead failed: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to mark read"})
	}
	return c.JSON(fiber.Map{"ok": true})
}
