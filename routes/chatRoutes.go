package routes

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"unipool-backend/database"
	"unipool-backend/initializer"
	"unipool-backend/models"
)

func GetRideMessages(c *gin.Context) {
	rideID := c.Param("ride_id")
	
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	var messages []models.Message
	result := database.DB.Preload("Sender").Where("ride_id = ?", rideUUID).Order("created_at ASC").Find(&messages)
	
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch messages"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"messages": messages,
		"count":    len(messages),
	})
}

func SendMessage(c *gin.Context) {
	rideID := c.Param("ride_id")
	
	rideUUID, err := uuid.Parse(rideID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	var requestBody struct {
		Content  string `json:"content" binding:"required"`
		SenderID string `json:"sender_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	senderUUID, err := uuid.Parse(requestBody.SenderID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid sender ID"})
		return
	}

	message := models.Message{
		RideID:   rideUUID,
		SenderID: senderUUID,
		Content:  requestBody.Content,
	}

	if err := database.DB.Create(&message).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save message"})
		return
	}

	database.DB.Preload("Sender").First(&message, message.ID)

	chatMessage := initializer.ChatMessage{
		Type:      "message",
		RoomID:    rideID,
		SenderID:  requestBody.SenderID,
		Content:   requestBody.Content,
		Timestamp: time.Now().Format(time.RFC3339),
		MessageID: message.ID.String(),
	}

	hub := initializer.GetChatHub()
	if hub != nil {
		messageBytes, _ := json.Marshal(chatMessage)
		hub.BroadcastToRoom(rideID, messageBytes)
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": message,
		"status":  "sent",
	})
}

func GetUserChats(c *gin.Context) {
	userID := c.Param("user_id")
	
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	var rides []models.Ride
	result := database.DB.Preload("HostUser").Where("host_user_id = ?", userUUID).
		Or("id IN (SELECT ride_id FROM bookings WHERE user_id = ?)", userUUID).
		Order("created_at DESC").Find(&rides)
	
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch chats"})
		return
	}

	var chatRooms []gin.H
	for _, ride := range rides {
		var lastMessage models.Message
		database.DB.Preload("Sender").Where("ride_id = ?", ride.ID).Order("created_at DESC").First(&lastMessage)

		chatRoom := gin.H{
			"id":            ride.ID.String(),
			"title":         ride.StartLocation + " to " + ride.EndLocation,
			"subtitle":      "Trip on " + ride.StartTime.Format("Mon 2 Jan 2006"),
			"participants":  strconv.Itoa(int(ride.BookedSeats)) + " passengers",
			"last_message":  nil,
		}

		if lastMessage.ID != uuid.Nil {
			chatRoom["last_message"] = gin.H{
				"content":   lastMessage.Content,
				"sender":    lastMessage.Sender.Name,
				"timestamp": lastMessage.CreatedAt,
			}
		}

		chatRooms = append(chatRooms, chatRoom)
	}

	c.JSON(http.StatusOK, gin.H{
		"chat_rooms": chatRooms,
		"count":      len(chatRooms),
	})
}

func WebSocketHandler(c *gin.Context) {
	initializer.HandleWebSocket(c)
}
