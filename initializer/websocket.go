package initializer

import (
	"encoding/json"
	"log"
	"sync"

	websocket "github.com/gofiber/websocket/v2"
)

type Hub struct {
	mu         sync.RWMutex
	Clients    map[*Client]bool
	Broadcast  chan []byte
	Register   chan *Client
	Unregister chan *Client
	Rooms      map[string]map[*Client]bool
}

type Client struct {
	Hub    *Hub
	Conn   *websocket.Conn
	Send   chan []byte
	UserID string
	RoomID string
	Name   string
	Avatar string
}

type ChatMessage struct {
	Type      string    `json:"type"`
	RoomID    string    `json:"room_id"`
	SenderID  string    `json:"sender_id"`
	Content   string    `json:"content"`
	Timestamp string    `json:"timestamp"`
	MessageID string    `json:"message_id"`
	TempID    string    `json:"temp_id,omitempty"`
	Sender    *UserInfo `json:"sender,omitempty"`
}

type MessageStatusUpdate struct {
	Type      string `json:"type"`
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	Status    string `json:"status"` // "delivered" or "seen"
}

type UserPresenceMessage struct {
	Type     string `json:"type"` // "user_joined" or "user_left"
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	RoomID   string `json:"room_id"`
}

type PresenceSnapshot struct {
	Type   string              `json:"type"` // "presence_snapshot"
	RoomID string              `json:"room_id"`
	Users  []UserPresenceState `json:"users"`
}

type UserPresenceState struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
}

type TypingIndicator struct {
	Type     string `json:"type"` // "typing"
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	RoomID   string `json:"room_id"`
	IsTyping bool   `json:"is_typing"`
}

type UserInfo struct {
	Name              string `json:"name"`
	ProfilePictureURL string `json:"profile_picture_url"`
}

var chatHub *Hub

func InitializeWebsocket() {
	chatHub = &Hub{
		Clients:    make(map[*Client]bool),
		Broadcast:  make(chan []byte),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		Rooms:      make(map[string]map[*Client]bool),
	}

	go chatHub.run()
	log.Println("WebSocket hub initialized")
}

func GetChatHub() *Hub {
	return chatHub
}

func NewClient(conn *websocket.Conn, userID, roomID, userName, avatar string) {
	client := &Client{
		Hub:    chatHub,
		Conn:   conn,
		Send:   make(chan []byte, 256),
		UserID: userID,
		RoomID: roomID,
		Name:   userName,
		Avatar: avatar,
	}

	client.Hub.Register <- client

	go client.writePump()
	go client.readPump()
}

func (c *Client) TrySend(message []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	select {
	case c.Send <- message:
		return true
	default:
		return false
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			wasOnline := h.userConnectionCountLocked(client.RoomID, client.UserID) > 0
			h.Clients[client] = true
			if h.Rooms[client.RoomID] == nil {
				h.Rooms[client.RoomID] = make(map[*Client]bool)
			}
			h.Rooms[client.RoomID][client] = true
			snapshot := h.presenceSnapshotLocked(client.RoomID)
			if b, err := json.Marshal(snapshot); err == nil {
				select {
				case client.Send <- b:
				default:
				}
			}
			h.mu.Unlock()

			if !wasOnline {
				h.BroadcastToRoomExceptSender(client.RoomID, client, h.presenceMessage("user_joined", client))
			}
			log.Printf("Client %s joined room %s", client.UserID, client.RoomID)

		case client := <-h.Unregister:
			h.mu.Lock()
			if _, ok := h.Clients[client]; ok {
				delete(h.Clients, client)
				if h.Rooms[client.RoomID] != nil {
					delete(h.Rooms[client.RoomID], client)
					if len(h.Rooms[client.RoomID]) == 0 {
						delete(h.Rooms, client.RoomID)
					}
				}
				close(client.Send)
				stillOnline := h.userConnectionCountLocked(client.RoomID, client.UserID) > 0
				h.mu.Unlock()
				if !stillOnline {
					h.BroadcastToRoom(client.RoomID, h.presenceMessage("user_left", client))
				}
				log.Printf("Client %s left room %s", client.UserID, client.RoomID)
			} else {
				h.mu.Unlock()
			}

		case message := <-h.Broadcast:
			h.mu.Lock()
			for client := range h.Clients {
				select {
				case client.Send <- message:
				default:
					close(client.Send)
					delete(h.Clients, client)
					if h.Rooms[client.RoomID] != nil {
						delete(h.Rooms[client.RoomID], client)
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) BroadcastToRoom(roomID string, message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if clients, ok := h.Rooms[roomID]; ok {
		for client := range clients {
			select {
			case client.Send <- message:
			default:
				close(client.Send)
				delete(h.Clients, client)
				delete(h.Rooms[roomID], client)
			}
		}
	}
}

func (h *Hub) BroadcastToRoomExceptSender(roomID string, sender *Client, message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if clients, ok := h.Rooms[roomID]; ok {
		for client := range clients {
			if client != sender {
				select {
				case client.Send <- message:
				default:
					close(client.Send)
					delete(h.Clients, client)
					delete(h.Rooms[roomID], client)
				}
			}
		}
	}
}

func (h *Hub) RoomStats() (map[string]int, int) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	roomStats := make(map[string]int, len(h.Rooms))
	totalConnections := 0
	for roomID, clients := range h.Rooms {
		roomStats[roomID] = len(clients)
		totalConnections += len(clients)
	}
	return roomStats, totalConnections
}

func (h *Hub) userConnectionCountLocked(roomID, userID string) int {
	count := 0
	for client := range h.Rooms[roomID] {
		if client.UserID == userID {
			count++
		}
	}
	return count
}

// IsUserActiveInRoom reports whether the user has an open WebSocket connection
// to the room. Chat fanout uses this to suppress duplicate push banners.
func (h *Hub) IsUserActiveInRoom(roomID, userID string) bool {
	if h == nil || roomID == "" || userID == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.userConnectionCountLocked(roomID, userID) > 0
}

// FilterUsersNotInRoom removes users currently viewing a room from a recipient
// list. nil hub or empty inputs return the input unchanged.
func (h *Hub) FilterUsersNotInRoom(roomID string, userIDs []string) []string {
	if h == nil || roomID == "" || len(userIDs) == 0 {
		return userIDs
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Build the in-room set once for the whole recipient list.
	inRoom := make(map[string]struct{})
	for client := range h.Rooms[roomID] {
		inRoom[client.UserID] = struct{}{}
	}

	out := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		if _, viewing := inRoom[id]; viewing {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (h *Hub) presenceSnapshotLocked(roomID string) PresenceSnapshot {
	usersByID := make(map[string]string)
	for client := range h.Rooms[roomID] {
		usersByID[client.UserID] = client.Name
	}

	users := make([]UserPresenceState, 0, len(usersByID))
	for userID, userName := range usersByID {
		users = append(users, UserPresenceState{UserID: userID, UserName: userName})
	}
	return PresenceSnapshot{
		Type:   "presence_snapshot",
		RoomID: roomID,
		Users:  users,
	}
}

func (h *Hub) presenceMessage(presenceType string, client *Client) []byte {
	msg := UserPresenceMessage{
		Type:     presenceType,
		UserID:   client.UserID,
		UserName: client.Name,
		RoomID:   client.RoomID,
	}
	b, _ := json.Marshal(msg)
	return b
}
