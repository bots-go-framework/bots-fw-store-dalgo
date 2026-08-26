package botsfwstoredalgo

import (
	"context"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
)

// stepClock returns times[i] on the i-th call, then keeps returning the last
// entry for any further calls. Tests use it instead of time.Now so
// DtCreated/DtUpdated assertions are deterministic and an "advances
// DtUpdated" assertion can prove the value actually moved.
type stepClock struct {
	times []time.Time
	calls []time.Time
}

func newStepClock(times ...time.Time) *stepClock {
	return &stepClock{times: times}
}

func (c *stepClock) now() time.Time {
	i := len(c.calls)
	if i >= len(c.times) {
		i = len(c.times) - 1
	}
	t := c.times[i]
	c.calls = append(c.calls, t)
	return t
}

func newValidPlatformUserFactory(botID string) func(appUserID string) (botsfwmodels.PlatformUserData, error) {
	return func(appUserID string) (botsfwmodels.PlatformUserData, error) {
		return &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{AppUserID: appUserID},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{botID}},
		}, nil
	}
}

func newValidChatFactory(botUserID string) func(appUserID string, accessGranted bool) (botsfwmodels.BotChatData, error) {
	return func(appUserID string, _ bool) (botsfwmodels.BotChatData, error) {
		return &botsfwmodels.ChatBaseData{
			BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID},
			BotUserIDs:  []string{botUserID},
		}, nil
	}
}

// TestEnsureLinked_StampsTimestampsForNewRecords is the regression test for
// the bug that stopped a brand-new platform user from ever being persisted:
// BotBaseData.Validate requires non-zero DtCreated/DtUpdated, but nothing
// stamped either field on a record built by request.NewPlatformUserData or
// NewChatData - those factories only run platform field setters such as
// bots-fw-telegram's SetBotUserFields/SetBotChatFields, which populate
// FirstName/LastName/ids/access and nothing else.
//
// dalgo2memory (used here, and by the framework's other EnsureLinked tests)
// does not call Validate on insert - unlike a real Firestore-backed dalgo DB
// - so EnsureLinked returning a nil error is not proof the persisted record
// is valid. Calling Validate ourselves on what EnsureLinked actually
// returned is the only way to catch the bug at this layer.
func TestEnsureLinked_StampsTimestampsForNewRecords(t *testing.T) {
	appUsers := &testAppUsers{}
	clock := newStepClock(time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC))
	store := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appUsers, WithClock(clock.now))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newValidPlatformUserFactory(identity.BotID),
		NewChatData:         newValidChatFactory(identity.BotUserID),
	}

	linked, err := store.EnsureLinked(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	if err := linked.PlatformUser.Data.BaseData().Validate(); err != nil {
		t.Fatalf("newly-created PlatformUserData failed Validate(): %v", err)
	}
	if err := linked.ChatData.Base().Validate(); err != nil {
		t.Fatalf("newly-created BotChatData failed Validate(): %v", err)
	}

	wantTime := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	if got := linked.PlatformUser.Data.BaseData().DtCreated; !got.Equal(wantTime) {
		t.Errorf("PlatformUser DtCreated = %v, want %v", got, wantTime)
	}
	if got := linked.PlatformUser.Data.BaseData().DtUpdated; !got.Equal(wantTime) {
		t.Errorf("PlatformUser DtUpdated = %v, want %v", got, wantTime)
	}
	if got := linked.ChatData.Base().DtCreated; !got.Equal(wantTime) {
		t.Errorf("ChatData DtCreated = %v, want %v", got, wantTime)
	}
	if got := linked.ChatData.Base().DtUpdated; !got.Equal(wantTime) {
		t.Errorf("ChatData DtUpdated = %v, want %v", got, wantTime)
	}
}

// TestEnsureLinked_RelinkPreservesDtCreatedAndAdvancesDtUpdated covers the
// other two stamping sites inside EnsureLinked: a platform user or bot chat
// that already exists (e.g. pre-provisioned by an application flow) but has
// no AppUserID yet, being linked to an application user for the first time.
// This must refresh DtUpdated without rewriting the original DtCreated. The
// bot-chat branch previously did not stamp DtUpdated at all - it only set
// AppUserID - which is a second, related gap this closes.
func TestEnsureLinked_RelinkPreservesDtCreatedAndAdvancesDtUpdated(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	appUsers := &testAppUsers{}
	seededAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	relinkAt := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	clock := newStepClock(relinkAt)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}

	// Seed a platform-user record and a chat record directly, bypassing
	// EnsureLinked, the way a pre-provisioned identity (or an old row
	// written before this fix) looks: valid timestamps, no AppUserID yet.
	seedPlatformUser := &botsfwmodels.PlatformUserBaseDbo{
		BotBaseData:        botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt},
		WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{identity.BotID}},
	}
	seedChat := &botsfwmodels.ChatBaseData{
		BotBaseData: botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt},
		BotUserIDs:  []string{identity.BotUserID},
	}
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, seedPlatformUser); err != nil {
			return err
		}
		_, err := dal.InsertRecordWithDataAndID(ctx, tx, NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID), identity.ChatID, seedChat)
		return err
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	store := NewStateStore(db, appUsers, WithClock(clock.now))
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newValidPlatformUserFactory(identity.BotID),
		NewChatData:         newValidChatFactory(identity.BotUserID),
	}

	linked, err := store.EnsureLinked(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	if err := linked.PlatformUser.Data.BaseData().Validate(); err != nil {
		t.Fatalf("relinked PlatformUserData failed Validate(): %v", err)
	}

	if got := linked.PlatformUser.Data.BaseData().DtCreated; !got.Equal(seededAt) {
		t.Errorf("PlatformUser DtCreated = %v, want unchanged %v", got, seededAt)
	}
	if got := linked.PlatformUser.Data.BaseData().DtUpdated; !got.Equal(relinkAt) {
		t.Errorf("PlatformUser DtUpdated = %v, want %v", got, relinkAt)
	}
	if got := linked.ChatData.Base().DtCreated; !got.Equal(seededAt) {
		t.Errorf("ChatData DtCreated = %v, want unchanged %v", got, seededAt)
	}
	if got := linked.ChatData.Base().DtUpdated; !got.Equal(relinkAt) {
		t.Errorf("ChatData DtUpdated = %v, want %v (bot-chat relink previously never stamped DtUpdated at all)", got, relinkAt)
	}
	if got := linked.ChatData.GetAppUserID(); got != linked.AppUser.ID {
		t.Errorf("ChatData AppUserID = %q, want %q", got, linked.AppUser.ID)
	}
}

// TestStateStore_WithClock_DefaultsToWallClock proves WithClock is optional:
// omitting it still produces valid, non-zero timestamps sourced from
// time.Now, so existing callers of NewStateStore/NewStateStoreWithProvider
// keep working unchanged.
func TestStateStore_WithClock_DefaultsToWallClock(t *testing.T) {
	appUsers := &testAppUsers{}
	store := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appUsers) // no WithClock
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}
	before := time.Now().UTC()
	linked, err := store.EnsureLinked(context.Background(), botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newValidPlatformUserFactory(identity.BotID),
	})
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}
	got := linked.PlatformUser.Data.BaseData().DtCreated
	if got.Before(before) || got.After(after) {
		t.Fatalf("DtCreated = %v, want between %v and %v", got, before, after)
	}
	if err := linked.PlatformUser.Data.BaseData().Validate(); err != nil {
		t.Fatalf("PlatformUserData failed Validate(): %v", err)
	}
}
