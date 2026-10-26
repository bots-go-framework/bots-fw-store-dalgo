package botsfwstoredalgo

import (
	"context"
	"errors"
	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"reflect"
	"testing"
	"time"
)

func TestConfiguredKeysIsolateWebhookStateAndCopyAncestry(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	ctx := context.Background()
	now := time.Now().UTC()
	parent := record.NewKeyWithID("ext", "owner")
	keys := NewKeys(parent, "platforms")
	parent.ID = "changed"
	if got := keys.BotChat("telegram", "bot", "chat").String(); got != "ext/owner/platforms/telegram/bots/bot/botChats/chat" {
		t.Fatal(got)
	}
	returned := keys.Platform("telegram")
	returned.Parent().ID = "mutated"
	if keys.Platform("telegram").Parent().ID != "owner" {
		t.Fatal("returned key mutated configuration")
	}
	update := botsfwstore.WebhookUpdateKey{PlatformID: "telegram", BotID: "bot", UpdateID: "same"}
	for _, k := range []Keys{DefaultKeys(), NewKeys(record.NewKeyWithID("ext", "foreign"), "platforms")} {
		if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Set(ctx, record.NewRecordWithData(k.WebhookUpdate(update), &webhookUpdateData{Status: "completed", Attempts: 9}))
		}); err != nil {
			t.Fatal(err)
		}
	}
	store := NewStateStore(db, &testAppUsers{}, WithKeys(keys), WithClock(func() time.Time { return now }))
	claim, err := store.ClaimWebhookUpdate(ctx, update, now.Add(time.Minute))
	if err != nil || !claim.CanDispatch() || claim.Attempts != 1 {
		t.Fatal(claim, err)
	}
	if err := store.CompleteWebhookUpdate(ctx, update, claim.LeaseID); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ClaimWebhookUpdate(ctx, update, now.Add(time.Minute))
	if err != nil || completed.Status != botsfwstore.WebhookUpdateClaimCompleted {
		t.Fatal(completed, err)
	}
	for _, k := range []Keys{DefaultKeys(), NewKeys(record.NewKeyWithID("ext", "foreign"), "platforms")} {
		var row webhookUpdateData
		if err := db.Get(ctx, record.NewRecordWithData(k.WebhookUpdate(update), &row)); err != nil || row.Attempts != 9 {
			t.Fatal(row, err)
		}
	}
}
func TestInvalidKeyConfigurationPanics(t *testing.T) {
	for _, f := range []func(){func() { NewKeys(nil, "") }, func() { NewStateStore(dalgo2memory.NewDB(), &testAppUsers{}, WithKeys(Keys{})) }, func() { NewKeys(record.NewIncompleteKey("ext", reflect.String, nil), "platforms") }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("missing panic")
				}
			}()
			f()
		}()
	}
}

func TestConfiguredIdentityNeverReadsHistoricalBinding(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	ctx := context.Background()
	now := time.Now().UTC()
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "user", ChatID: "chat"}
	old := &botsfwmodels.PlatformUserBaseDbo{BotBaseData: botsfwmodels.BotBaseData{AppUserID: "old-owner", DtCreated: now, DtUpdated: now}, WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{"bot"}}}
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, "telegram", "user", old)
	}); err != nil {
		t.Fatal(err)
	}
	keys := NewKeys(record.NewKeyWithID("ext", "owner"), "platforms")
	app := new(testAppUsers)
	s := NewStateStore(db, app, WithKeys(keys))
	linked, err := s.EnsureLinked(ctx, botsfwstore.LinkRequest{Identity: identity, ReadPlatformUserData: func() botsfwmodels.PlatformUserData { return new(botsfwmodels.PlatformUserBaseDbo) }, NewPlatformUserData: newValidPlatformUserFactory("bot"), NewChatData: newValidChatFactory("user")})
	if err != nil || linked.AppUser.ID != "app-1" || app.prepareCalls != 1 {
		t.Fatal(linked, err, app.prepareCalls)
	}
	for _, key := range []*record.Key{keys.PlatformUser("telegram", "user"), keys.BotChat("telegram", "bot", "chat")} {
		if exists, err := db.Exists(ctx, key); err != nil || !exists {
			t.Fatal(key, exists, err)
		}
	}
	if got, err := keys.GetPlatformUser(ctx, db, "telegram", "user", new(botsfwmodels.PlatformUserBaseDbo)); err != nil || got.Data.GetAppUserID() != "app-1" {
		t.Fatal(got, err)
	}
	if got, err := GetPlatformUser(ctx, db, "telegram", "user", new(botsfwmodels.PlatformUserBaseDbo)); err != nil || got.Data.GetAppUserID() != "old-owner" {
		t.Fatal(got, err)
	}
}

type invalidParentID string

func (invalidParentID) Validate() error { return errors.New("invalid owner") }
func TestConfiguredKeysRejectInvalidParentID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid parent accepted")
		}
	}()
	NewKeys(record.NewKeyWithID("ext", invalidParentID("bad")), "platforms")
}
