package chat

import (
	"testing"
	"time"
	"unipool-backend/services"

	"github.com/google/uuid"
)

func resetChatPushThrottleForTest() {
	chatPushThrottle.Lock()
	defer chatPushThrottle.Unlock()
	chatPushThrottle.lastSent = map[chatPushThrottleKey]time.Time{}
}

func TestFilterChatPushThrottleAllowsAllTokensForFirstUserPush(t *testing.T) {
	resetChatPushThrottleForTest()
	defer resetChatPushThrottleForTest()

	userID := uuid.New()
	recipients := []services.FCMRecipient{
		{UserID: userID, Token: "phone"},
		{UserID: userID, Token: "tablet"},
	}

	allowed := filterChatPushThrottle("room-1", recipients)
	if len(allowed) != len(recipients) {
		t.Fatalf("expected all tokens on first allowed push, got %d", len(allowed))
	}

	allowed = filterChatPushThrottle("room-1", recipients)
	if len(allowed) != 0 {
		t.Fatalf("expected repeat push to be throttled, got %d tokens", len(allowed))
	}
}

func TestFilterChatPushThrottleScopesByRoomAndWindow(t *testing.T) {
	resetChatPushThrottleForTest()
	defer resetChatPushThrottleForTest()

	userID := uuid.New()
	recipients := []services.FCMRecipient{{UserID: userID, Token: "phone"}}

	if got := filterChatPushThrottle("room-1", recipients); len(got) != 1 {
		t.Fatalf("expected first room-1 push to pass, got %d", len(got))
	}
	if got := filterChatPushThrottle("room-2", recipients); len(got) != 1 {
		t.Fatalf("expected different room push to pass, got %d", len(got))
	}

	chatPushThrottle.Lock()
	chatPushThrottle.lastSent[chatPushThrottleKey{roomID: "room-1", userID: userID}] = time.Now().Add(-chatPushThrottleWindow - time.Second)
	chatPushThrottle.Unlock()

	if got := filterChatPushThrottle("room-1", recipients); len(got) != 1 {
		t.Fatalf("expected expired room-1 throttle to pass, got %d", len(got))
	}
}
