package initializer

import (
	"log"

	websocket "github.com/gofiber/websocket/v2"
)


type Hub struct {
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
}

type ChatMessage struct {
	Type      string `json:"type"` 
	RoomID    string `json:"room_id"`
	SenderID  string `json:"sender_id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	MessageID string `json:"message_id"`
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

func NewClient(conn *websocket.Conn, userID, roomID string) {
	client := &Client{
		Hub:    chatHub,
		Conn:   conn,
		Send:   make(chan []byte, 256),
		UserID: userID,
		RoomID: roomID,
	}

	client.Hub.Register <- client

	go client.writePump()
	go client.readPump()
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.Register:
			h.Clients[client] = true
			if h.Rooms[client.RoomID] == nil {
				h.Rooms[client.RoomID] = make(map[*Client]bool)
			}
			h.Rooms[client.RoomID][client] = true
			log.Printf("Client %s joined room %s", client.UserID, client.RoomID)

		case client := <-h.Unregister:
			if _, ok := h.Clients[client]; ok {
				delete(h.Clients, client)
				if h.Rooms[client.RoomID] != nil {
					delete(h.Rooms[client.RoomID], client)
					if len(h.Rooms[client.RoomID]) == 0 {
						delete(h.Rooms, client.RoomID)
					}
				}
				close(client.Send)
				log.Printf("Client %s left room %s", client.UserID, client.RoomID)
			}

		case message := <-h.Broadcast:
			for client := range h.Clients {
				select {
				case client.Send <- message:
				default:
					close(client.Send)
					delete(h.Clients, client)
				}
			}
		}
	}
}

func (h *Hub) BroadcastToRoom(roomID string, message []byte) {
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
