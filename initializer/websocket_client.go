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

		var chatMsg ChatMessage
		if err := json.Unmarshal(message, &chatMsg); err != nil {
			log.Printf("Error parsing message: %v", err)
			continue
		}

		chatMsg.SenderID = c.UserID
		chatMsg.RoomID = c.RoomID
		chatMsg.Timestamp = time.Now().Format(time.RFC3339)
		if chatMsg.MessageID == "" {
			chatMsg.MessageID = uuid.New().String()
		}

		go func(m ChatMessage) {
			rideUUID, err1 := uuid.Parse(m.RoomID)
			var senderUUID uuid.UUID
			senderUUID, err2 := uuid.Parse(m.SenderID)
			if err2 != nil {
				log.Printf("invalid sender UUID: %v", err2)
				return
			}
			if err1 != nil {
				log.Printf("ride uuid parse error: %v", err1)
				return
			}

			msg := models.Message{
				RideID:   rideUUID,
				SenderID: senderUUID,
				Content:  m.Content,
			}
			if err := database.Database.Db.Create(&msg).Error; err != nil {
				log.Printf("DB save error: %v", err)
			}
		}(chatMsg)

		updatedMessage, err := json.Marshal(chatMsg)
		if err != nil {
			log.Printf("Error marshaling message: %v", err)
			continue
		}

		c.Hub.BroadcastToRoom(c.RoomID, updatedMessage)
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
