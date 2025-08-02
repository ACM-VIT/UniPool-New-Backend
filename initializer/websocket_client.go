package initializer

import (
	"encoding/json"
	"log"
	"time"

	websocket "github.com/gofiber/websocket/v2"
	"github.com/google/uuid"

	"unipool-backend/database"
	"unipool-backend/models"
)

const (
	writeWait = 10 * time.Second

	pongWait = 60 * time.Second

	pingPeriod = (pongWait * 9) / 10

	maxMessageSize = 512
)

func (c *Client) readPump() {
	defer func() {
		c.sendUserPresenceUpdate("user_left")
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	c.sendUserPresenceUpdate("user_joined")

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			log.Printf("readPump error for user %s room %s: %v", c.UserID, c.RoomID, err)
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("unexpected close error: %v", err)
			}
			break
		}

		// Parse the incoming message to determine its type
		var baseMsg map[string]interface{}
		if err := json.Unmarshal(message, &baseMsg); err != nil {
			log.Printf("Error parsing base message: %v", err)
			continue
		}

		msgType, _ := baseMsg["type"].(string)
		
		switch msgType {
		case "message":
			c.handleChatMessage(message)
		case "message_status":
			c.handleMessageStatus(message)
		case "typing":
			c.handleTypingIndicator(message)
		default:
			log.Printf("Unknown message type: %s", msgType)
		}
	}
}

func (c *Client) handleChatMessage(message []byte) {
	var chatMsg ChatMessage
	if err := json.Unmarshal(message, &chatMsg); err != nil {
		log.Printf("Error parsing chat message: %v", err)
		return
	}

	var sender models.User
	senderUUID, err := uuid.Parse(c.UserID)
	if err != nil {
		log.Printf("Invalid sender UUID: %v", err)
		return
	}

	if err := database.Database.Db.First(&sender, senderUUID).Error; err != nil {
		log.Printf("Error fetching sender details: %v", err)
		return
	}

	chatMsg.SenderID = c.UserID
	chatMsg.RoomID = c.RoomID
	chatMsg.Timestamp = time.Now().Format(time.RFC3339)
	chatMsg.Type = "message"
	
	chatMsg.Sender = &UserInfo{
		Name:              sender.Name,
		ProfilePictureURL: sender.ProfilePictureURL,
	}

	if chatMsg.MessageID == "" {
		chatMsg.MessageID = uuid.New().String()
	}

	tempID := chatMsg.TempID

	go func(m ChatMessage, tempID string) {
		rideUUID, err1 := uuid.Parse(m.RoomID)
		senderUUID, err2 := uuid.Parse(m.SenderID)
		if err1 != nil || err2 != nil {
			log.Printf("UUID parse error: ride=%v, sender=%v", err1, err2)
			return
		}

		msg := models.Message{
			RideID:   rideUUID,
			SenderID: senderUUID,
			Content:  m.Content,
		}
		if err := database.Database.Db.Create(&msg).Error; err != nil {
			log.Printf("DB save error: %v", err)
			return
		}

		if tempID != "" {
			confirmation := ChatMessage{
				Type:      "message",
				MessageID: msg.ID.String(),
				TempID:    tempID,
				RoomID:    m.RoomID,
				SenderID:  m.SenderID,
				Content:   m.Content,
				Timestamp: msg.CreatedAt.Format(time.RFC3339),
				Sender:    m.Sender,
			}
			
			if confirmMsg, err := json.Marshal(confirmation); err == nil {
				select {
				case c.Send <- confirmMsg:
				default:
					log.Printf("Failed to send confirmation to sender")
				}
			}
		}
	}(chatMsg, tempID)

	updatedMessage, err := json.Marshal(chatMsg)
	if err != nil {
		log.Printf("Error marshaling message: %v", err)
		return
	}

	c.Hub.BroadcastToRoom(c.RoomID, updatedMessage)
}

func (c *Client) handleMessageStatus(message []byte) {
	var statusMsg MessageStatusUpdate
	if err := json.Unmarshal(message, &statusMsg); err != nil {
		log.Printf("Error parsing message status: %v", err)
		return
	}

	statusMsg.UserID = c.UserID
	statusMsg.Type = "message_status"

	// Broadcast status update to all clients in the room
	if statusBytes, err := json.Marshal(statusMsg); err == nil {
		c.Hub.BroadcastToRoom(c.RoomID, statusBytes)
	}
}

func (c *Client) handleTypingIndicator(message []byte) {
	var typingMsg TypingIndicator
	if err := json.Unmarshal(message, &typingMsg); err != nil {
		log.Printf("Error parsing typing indicator: %v", err)
		return
	}

	var user models.User
	userUUID, err := uuid.Parse(c.UserID)
	if err != nil {
		log.Printf("Invalid user UUID: %v", err)
		return
	}

	if err := database.Database.Db.First(&user, userUUID).Error; err != nil {
		log.Printf("Error fetching user details: %v", err)
		return
	}

	typingMsg.UserID = c.UserID
	typingMsg.UserName = user.Name
	typingMsg.RoomID = c.RoomID
	typingMsg.Type = "typing"

	if typingBytes, err := json.Marshal(typingMsg); err == nil {
		c.Hub.BroadcastToRoomExceptSender(c.RoomID, c, typingBytes)
	}
}

func (c *Client) sendUserPresenceUpdate(presenceType string) {
	// Get user name
	var user models.User
	userUUID, err := uuid.Parse(c.UserID)
	if err != nil {
		log.Printf("Invalid user UUID: %v", err)
		return
	}

	if err := database.Database.Db.First(&user, userUUID).Error; err != nil {
		log.Printf("Error fetching user details: %v", err)
		return
	}

	presenceMsg := UserPresenceMessage{
		Type:     presenceType,
		UserID:   c.UserID,
		UserName: user.Name,
		RoomID:   c.RoomID,
	}

	if presenceBytes, err := json.Marshal(presenceMsg); err == nil {
		c.Hub.BroadcastToRoomExceptSender(c.RoomID, c, presenceBytes)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
