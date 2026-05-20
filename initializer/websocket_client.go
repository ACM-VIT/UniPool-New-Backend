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
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

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

	if _, err := uuid.Parse(c.UserID); err != nil {
		log.Printf("Invalid sender UUID: %v", err)
		return
	}

	chatMsg.SenderID = c.UserID
	chatMsg.RoomID = c.RoomID
	chatMsg.Timestamp = time.Now().Format(time.RFC3339)
	chatMsg.Type = "message"

	chatMsg.Sender = &UserInfo{
		Name:              c.Name,
		ProfilePictureURL: c.Avatar,
	}

	if chatMsg.MessageID == "" {
		chatMsg.MessageID = uuid.New().String()
	}

	tempID := chatMsg.TempID

	go func(m ChatMessage, tempID string) {
		senderUUID, err := uuid.Parse(m.SenderID)
		if err != nil {
			log.Printf("Invalid sender UUID: %v", err)
			return
		}

		var msg models.Message
		msg.SenderID = senderUUID
		msg.Content = m.Content

		if len(m.RoomID) > 3 && m.RoomID[:3] == "dm_" {
			msg.DMRoomID = &m.RoomID
		} else {
			rideUUID, err := uuid.Parse(m.RoomID)
			if err != nil {
				log.Printf("Invalid room UUID: %v", err)
				return
			}
			msg.RideID = &rideUUID
		}

		if err := database.Database.Db.Create(&msg).Error; err != nil {
			log.Printf("DB save error: %v", err)
			return
		}

		m.MessageID = msg.ID.String()
		m.Timestamp = msg.CreatedAt.Format(time.RFC3339)

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
				if ok := c.TrySend(confirmMsg); !ok {
					log.Printf("Failed to send confirmation to sender")
				}
			}
		}

		if updatedMessage, err := json.Marshal(m); err == nil {
			c.Hub.BroadcastToRoom(c.RoomID, updatedMessage)
		}
	}(chatMsg, tempID)
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

	typingMsg.UserID = c.UserID
	typingMsg.UserName = c.Name
	typingMsg.RoomID = c.RoomID
	typingMsg.Type = "typing"

	if typingBytes, err := json.Marshal(typingMsg); err == nil {
		c.Hub.BroadcastToRoomExceptSender(c.RoomID, c, typingBytes)
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
