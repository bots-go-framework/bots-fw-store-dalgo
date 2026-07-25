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

// newRealisticPlatformUserFactory mirrors what a platform adapter's field
// setter actually does in production - see bots-fw-telegram's
// SetBotUserFields, which receives a botID parameter and never uses it: it
// populates AppUserID and nothing else, in particular never BotIDs. Compare
// newValidPlatformUserFactory in store_timestamps_test.go, which sets BotIDs
// itself and so cannot catch the bug this file is about.
func newRealisticPlatformUserFactory() func(appUserID string) (botsfwmodels.PlatformUserData, error) {
	return func(appUserID string) (botsfwmodels.PlatformUserData, error) {
		return &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID},
		}, nil
	}
}

// TestEnsureLinked_StampsBotIDForNewPlatformUser is the regression test for
// the bug that stopped a brand-new platform user from ever being persisted
// against a dalgo backend that validates on insert (a real Firestore-backed
// one does; dalgo2memory, used here, does not - see the package doc on that
// gap, and TestEnsureLinked_NewUserFirstStart_EndToEnd below for why calling
// Validate ourselves on what EnsureLinked returned is the only way to catch
// this at this layer): PlatformUserBaseDbo embeds WithRequiredBotIDs, whose
// Validate requires a non-empty BotIDs, but nothing populated it -
// request.NewPlatformUserData only ever runs a platform adapter's field
// setter, which never touches BotIDs.
func TestEnsureLinked_StampsBotIDForNewPlatformUser(t *testing.T) {
	appUsers := &testAppUsers{}
	clock := newStepClock(time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC))
	store := NewStateStore(dalgo2memory.NewDB(), appUsers, WithClock(clock.now))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newRealisticPlatformUserFactory(),
	}

	linked, err := store.EnsureLinked(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	if err := linked.PlatformUser.Data.BaseData().Validate(); err != nil {
		t.Fatalf("newly-created PlatformUserData failed Validate(): %v", err)
	}
	if got := linked.PlatformUser.Data.BaseData().BotIDs; len(got) != 1 || got[0] != identity.BotID {
		t.Fatalf("BotIDs = %v, want [%s]", got, identity.BotID)
	}
}

// TestEnsureLinked_RelinkStampsBotIDWithoutClobbering covers the relink
// branch: a platform-user record pre-provisioned by an application flow, or
// written before BotIDs existed, with no BotIDs of its own but already
// shared by a different bot, being linked to an application user for the
// first time by a second bot.
func TestEnsureLinked_RelinkStampsBotIDWithoutClobbering(t *testing.T) {
	db := dalgo2memory.NewDB()
	appUsers := &testAppUsers{}
	seededAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	relinkAt := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	clock := newStepClock(relinkAt)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-2", BotUserID: "user-1"}

	seedPlatformUser := &botsfwmodels.PlatformUserBaseDbo{
		BotBaseData:        botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt},
		WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{"bot-1"}},
	}
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, seedPlatformUser)
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	store := NewStateStore(db, appUsers, WithClock(clock.now))
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newRealisticPlatformUserFactory(),
	}

	linked, err := store.EnsureLinked(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	if err := linked.PlatformUser.Data.BaseData().Validate(); err != nil {
		t.Fatalf("relinked PlatformUserData failed Validate(): %v", err)
	}
	got := linked.PlatformUser.Data.BaseData().BotIDs
	if len(got) != 2 || got[0] != "bot-1" || got[1] != "bot-2" {
		t.Fatalf("BotIDs = %v, want [bot-1 bot-2] (existing id preserved, new one appended)", got)
	}
}

// TestSetPlatformUserAccessGranted_StampsBotID covers the third write site
// that touches an existing PlatformUserBaseDbo: SetPlatformUserAccessGranted
// loads a record, flips AccessGranted, and rewrites it via tx.Set - the same
// shape of relink-style write as EnsureLinked's relink branch, and given the
// same self-heal treatment for consistency.
//
// The seed is written with dal.InsertRecordWithDataAndID directly, bypassing
// CreatePlatformUserRecord's own Validate() call, to simulate a legacy record
// that already has no BotIDs sitting in the database - exactly the record
// this test needs to prove SetPlatformUserAccessGranted repairs. Seeding
// through CreatePlatformUserRecord would fail instead of seeding, since a
// record with empty BotIDs is invalid by construction.
func TestSetPlatformUserAccessGranted_StampsBotID(t *testing.T) {
	db := dalgo2memory.NewDB()
	appUsers := &testAppUsers{}
	seededAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}

	seedPlatformUser := &botsfwmodels.PlatformUserBaseDbo{
		BotBaseData: botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt, AppUserID: "app-1"},
	}
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		_, err := dal.InsertRecordWithDataAndID(ctx, tx, NewPlatformUserKey(identity.PlatformID, identity.BotUserID), identity.BotUserID, seedPlatformUser)
		return err
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	store := NewStateStore(db, appUsers, WithClock(func() time.Time { return seededAt.Add(time.Hour) }))
	newData := func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }

	result, err := store.SetPlatformUserAccessGranted(context.Background(), identity, newData, true)
	if err != nil {
		t.Fatalf("SetPlatformUserAccessGranted() error: %v", err)
	}
	if err := result.Data.BaseData().Validate(); err != nil {
		t.Fatalf("PlatformUserData failed Validate() after SetPlatformUserAccessGranted: %v", err)
	}
	if got := result.Data.BaseData().BotIDs; len(got) != 1 || got[0] != identity.BotID {
		t.Fatalf("BotIDs = %v, want [%s]", got, identity.BotID)
	}
}

// TestEnsureLinked_NewUserFirstStart_EndToEnd is the end-to-end question all
// three bugs found in this audit (the inverted BotUserIDs trim check fixed
// in 1b35527, the missing timestamp stamping fixed in 6842418/15c74b7, and
// the missing BotIDs stamping fixed alongside this test) were blocking: for
// a brand-new platform user's very first /start, using factories realistic
// to what a platform adapter actually populates - newRealisticPlatformUserFactory
// above (AppUserID only, matching bots-fw-telegram's SetBotUserFields) and
// newValidChatFactory from store_timestamps_test.go (AppUserID and
// BotUserIDs, matching SetBotChatFields) - does EnsureLinked produce a
// PlatformUserData and a BotChatData that both pass Validate?
func TestEnsureLinked_NewUserFirstStart_EndToEnd(t *testing.T) {
	appUsers := &testAppUsers{}
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	store := NewStateStore(dalgo2memory.NewDB(), appUsers, WithClock(func() time.Time { return now }))
	identity := botsfwstore.Identity{
		PlatformID: "telegram",
		BotID:      "bot-1",
		BotUserID:  "user-1",
		ChatID:     "chat-1",
		FirstName:  "Ada",
	}
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newRealisticPlatformUserFactory(),
		NewChatData:         newValidChatFactory(identity.BotUserID),
	}

	linked, err := store.EnsureLinked(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}
	if err := linked.PlatformUser.Data.BaseData().Validate(); err != nil {
		t.Fatalf("a real new user's first /start would not persist: PlatformUserData failed Validate(): %v", err)
	}
	if err := linked.ChatData.Base().Validate(); err != nil {
		t.Fatalf("a real new user's first /start would not persist: ChatData failed Validate(): %v", err)
	}
}
