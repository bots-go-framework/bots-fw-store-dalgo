package botsfwstoredalgo

import (
	"context"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
)

// TestSaveChat_StampsTimestamps is the regression test for a gap found while
// auditing every dal.InsertRecordWithDataAndID/tx.Set call site in this file
// for the same missing-EnsureTimestamps shape as EnsureLinked/
// SetPlatformUserAccessGranted: SaveChat persisted whatever BotChatData it
// was given via tx.Set without ever calling EnsureTimestamps first, unlike
// every other write site here. A caller that passes a chat whose
// DtCreated/DtUpdated are zero - a legacy record, or one it built itself
// without stamping it - would write a record BotChatData.Validate rejects,
// with no self-heal seam to fix it.
func TestSaveChat_StampsTimestamps(t *testing.T) {
	appUsers := &testAppUsers{}
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	store := NewStateStore(dalgo2memory.NewDB(), appUsers, WithClock(func() time.Time { return now }))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}

	chat := &botsfwmodels.ChatBaseData{BotUserIDs: []string{identity.BotUserID}} // zero DtCreated/DtUpdated
	if err := store.SaveChat(context.Background(), identity, chat); err != nil {
		t.Fatalf("SaveChat() error: %v", err)
	}
	if err := chat.Validate(); err != nil {
		t.Fatalf("chat failed Validate() after SaveChat(): %v", err)
	}
	if !chat.DtCreated.Equal(now) {
		t.Errorf("DtCreated = %v, want %v", chat.DtCreated, now)
	}
	if !chat.DtUpdated.Equal(now) {
		t.Errorf("DtUpdated = %v, want %v", chat.DtUpdated, now)
	}
}

// TestSaveChat_PreservesDtCreatedAndAdvancesDtUpdated proves SaveChat's
// stamping does not rewrite an already-valid DtCreated on every save - the
// same non-destructive contract EnsureTimestamps gives every other caller.
func TestSaveChat_PreservesDtCreatedAndAdvancesDtUpdated(t *testing.T) {
	appUsers := &testAppUsers{}
	createdAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	savedAt := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	store := NewStateStore(dalgo2memory.NewDB(), appUsers, WithClock(func() time.Time { return savedAt }))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}

	chat := &botsfwmodels.ChatBaseData{
		BotBaseData: botsfwmodels.BotBaseData{DtCreated: createdAt, DtUpdated: createdAt},
		BotUserIDs:  []string{identity.BotUserID},
	}
	if err := store.SaveChat(context.Background(), identity, chat); err != nil {
		t.Fatalf("SaveChat() error: %v", err)
	}
	if !chat.DtCreated.Equal(createdAt) {
		t.Errorf("DtCreated = %v, want unchanged %v", chat.DtCreated, createdAt)
	}
	if !chat.DtUpdated.Equal(savedAt) {
		t.Errorf("DtUpdated = %v, want %v", chat.DtUpdated, savedAt)
	}
}
