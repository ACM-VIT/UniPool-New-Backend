package chat

import (
    "encoding/json"
    "log"
    "strconv"
    "time"

    "github.com/gofiber/fiber/v2"
    "github.com/gofiber/websocket/v2"
    "github.com/google/uuid"
    "unipool-backend/database"
    "unipool-backend/initializer"
    "unipool-backend/models"
    "unipool-backend/services"
)

func GetRideMessages(c *fiber.Ctx) error {
    rideID := c.Params("ride_id")
    rideUUID, err := uuid.Parse(rideID)
    if err != nil {
        return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid ride id"})
    }

    var messages []models.Message
    if err := database.Database.Db.Preload("Sender").Where("ride_id = ?", rideUUID).Order("created_at asc").Find(&messages).Error; err != nil {
        return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch messages"})
    }

    // Transform messages to include complete user details
    var transformedMessages []fiber.Map
    for _, msg := range messages {
        transformedMessage := fiber.Map{
            "id":         msg.ID.String(),
            "content":    msg.Content,
            "sender_id":  msg.SenderID.String(),
            "timestamp":  msg.CreatedAt.Format(time.RFC3339),
            "sender": fiber.Map{
                "name":                msg.Sender.Name,
                "profile_picture_url": msg.Sender.ProfilePictureURL,
            },
        }
        transformedMessages = append(transformedMessages, transformedMessage)
    }

    return c.JSON(fiber.Map{"messages": transformedMessages, "count": len(transformedMessages)})
}

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

	msg := models.Message{RideID: rideUUID, SenderID: user.ID, Content: body.Content}
	if err := database.Database.Db.Create(&msg).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to save message"})
	}
	database.Database.Db.Preload("Sender").First(&msg, msg.ID)

	// Create enhanced chat message with complete user details
	chatMsg := initializer.ChatMessage{
		Type:      "message",
		RoomID:    rideID,
		SenderID:  user.ID.String(),
		Content:   body.Content,
		Timestamp: msg.CreatedAt.Format(time.RFC3339),
		MessageID: msg.ID.String(),
		TempID:    body.TempID,
		Sender: &initializer.UserInfo{
			Name:              user.Name,
			ProfilePictureURL: user.ProfilePictureURL,
		},
	}

	if hub := initializer.GetChatHub(); hub != nil {
		if b, err := json.Marshal(chatMsg); err == nil {
			hub.BroadcastToRoom(rideID, b)
		}
	}

	// Send FCM notifications to other participants
	fcmService := services.GetFCMService()
	if fcmService != nil {
		go func() {
			// Get ride details
			var ride models.Ride
			if err := database.Database.Db.Preload("HostUser").First(&ride, rideUUID).Error; err != nil {
				log.Printf("Error fetching ride for notifications: %v", err)
				return
			}

			// Get all bookings for this ride
			var bookings []models.Booking
			if err := database.Database.Db.Preload("Passenger").Where("ride_id = ? AND request_status = ?", rideUUID, "accepted").Find(&bookings).Error; err != nil {
				log.Printf("Error fetching bookings for notifications: %v", err)
				return
			}

			rideRoute := ride.StartLocation + " to " + ride.EndLocation
			
			// Notify ride host if the sender is not the host
			if ride.HostUserID != user.ID {
				if err := fcmService.SendChatMessageNotification(ride.HostUserID, user.Name, body.Content, rideRoute, rideUUID); err != nil {
					log.Printf("Error sending chat notification to host: %v", err)
				}
			}

			// Notify all passengers except the sender
			for _, booking := range bookings {
				if booking.PassengerID != user.ID {
					if err := fcmService.SendChatMessageNotification(booking.PassengerID, user.Name, body.Content, rideRoute, rideUUID); err != nil {
						log.Printf("Error sending chat notification to passenger %s: %v", booking.PassengerID, err)
					}
				}
			}
		}()
	}

	// Return enhanced message structure
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": fiber.Map{
			"id":         msg.ID.String(),
			"content":    msg.Content,
			"sender_id":  msg.SenderID.String(),
			"ride_id":    msg.RideID.String(),
			"timestamp":  msg.CreatedAt.Format(time.RFC3339),
			"sender": fiber.Map{
				"name":                user.Name,
				"profile_picture_url": user.ProfilePictureURL,
			},
		},
	})
}

func GetUserChats(c *fiber.Ctx) error {
	user, ok := c.Locals("user").(models.User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "User not authenticated or found"})
	}
	userUUID := user.ID

    var rides []models.Ride
    if err := database.Database.Db.Preload("HostUser").Where("host_user_id = ?", userUUID).
        Or("id IN (SELECT ride_id FROM bookings WHERE user_id = ?)", userUUID).
        Order("created_at desc").Find(&rides).Error; err != nil {
        return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to fetch chats"})
    }

    var chatRooms []fiber.Map
    for _, ride := range rides {
        var last models.Message
        database.Database.Db.Preload("Sender").Where("ride_id = ?", ride.ID).Order("created_at desc").First(&last)

        room := fiber.Map{
            "id":           ride.ID.String(),
            "title":        ride.StartLocation + " to " + ride.EndLocation,
            "subtitle":     "Trip on " + ride.StartTime.Format("Mon 2 Jan 2006"),
            "participants": strconv.Itoa(int(ride.BookedSeats)) + " passengers",
        }
        if last.ID != uuid.Nil {
            room["last_message"] = fiber.Map{
                "content":   last.Content,
                "sender":    last.Sender.Name,
                "timestamp": last.CreatedAt,
            }
        }
        chatRooms = append(chatRooms, room)
    }

    return c.JSON(fiber.Map{"chat_rooms": chatRooms, "count": len(chatRooms)})
}

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

    // Validate user and room IDs
    if _, err := uuid.Parse(userID); err != nil {
        log.Printf("WebSocket connection rejected: invalid user_id format")
        c.WriteMessage(websocket.CloseMessage, []byte("invalid user_id format"))
        c.Close()
        return
    }

    if _, err := uuid.Parse(roomID); err != nil {
        log.Printf("WebSocket connection rejected: invalid room_id format")
        c.WriteMessage(websocket.CloseMessage, []byte("invalid room_id format"))
        c.Close()
        return
    }

    log.Printf("WebSocket connection established for user %s in room %s", userID, roomID)
    initializer.NewClient(c, userID, roomID)
    select {}
}

// Debug endpoint to check active connections
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
        "room_stats":       roomStats,
        "timestamp":        time.Now().Format(time.RFC3339),
    })
}
