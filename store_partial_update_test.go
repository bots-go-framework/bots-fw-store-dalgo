package botsfwstoredalgo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	dalrecord "github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

// These tests pin the partial-update contract: an operation on an existing
// document changes only the fields it owns, so a field that the caller's Go
// type does not model - for example one written by another service whose
// profile uses a different type for the same collection - survives it.

var (
	seededAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	changeAt = time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
)

func seedRaw(t *testing.T, db dal.DB, key *dalrecord.Key, doc map[string]any) {
	t.Helper()
	err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, dalrecord.NewRecordWithData(key, doc))
	})
	if err != nil {
		t.Fatalf("seed %v: %v", key, err)
	}
}

func readRaw(t *testing.T, db dal.DB, key *dalrecord.Key) map[string]any {
	t.Helper()
	doc := map[string]any{}
	if err := db.Get(context.Background(), dalrecord.NewRecordWithData(key, &doc)); err != nil {
		t.Fatalf("read %v: %v", key, err)
	}
	return doc
}

func assertRawField(t *testing.T, doc map[string]any, name string, want any) {
	t.Helper()
	got, ok := doc[name]
	if !ok {
		t.Errorf("field %q is missing from %v", name, doc)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("field %q = %#v, want %#v", name, got, want)
	}
}

func assertRawTime(t *testing.T, doc map[string]any, name string, want time.Time) {
	t.Helper()
	s, _ := doc[name].(string)
	got, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !got.Equal(want) {
		t.Errorf("field %q = %#v, want %v", name, doc[name], want)
	}
}

func foreignFields() map[string]any {
	return map[string]any{
		"foreignString": "kept",
		"foreignMap":    map[string]any{"a": float64(1)},
	}
}

func assertForeignFieldsSurvive(t *testing.T, doc map[string]any) {
	t.Helper()
	assertRawField(t, doc, "foreignString", "kept")
	assertRawField(t, doc, "foreignMap", map[string]any{"a": float64(1)})
}

func newLinkRequest(identity botsfwstore.Identity) botsfwstore.LinkRequest {
	return botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: newValidPlatformUserFactory(identity.BotID),
		NewChatData:         newValidChatFactory(identity.BotUserID),
	}
}

func rawPlatformUser(extra map[string]any) map[string]any {
	doc := map[string]any{
		"appUserID":       "",
		"dtCreated":       seededAt,
		"dtUpdated":       seededAt,
		"isAccessGranted": true,
		"botIDs":          []string{"bot-1"},
		"firstName":       "Ann",
	}
	for k, v := range foreignFields() {
		doc[k] = v
	}
	for k, v := range extra {
		doc[k] = v
	}
	return doc
}

func TestEnsureLinked_LinkingKeepsFieldsTheProfileDoesNotModel(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	userKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
	chatKey := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
	seedRaw(t, db, userKey, rawPlatformUser(nil))
	seedRaw(t, db, chatKey, map[string]any{
		"appUserID":       "",
		"dtCreated":       seededAt,
		"dtUpdated":       seededAt,
		"isAccessGranted": true,
		"botUserIDs":      []string{"user-1"},
		"foreignString":   "kept",
		"foreignMap":      map[string]any{"a": float64(1)},
	})
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))

	linked, err := store.EnsureLinked(context.Background(), newLinkRequest(identity))
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	user := readRaw(t, db, userKey)
	assertForeignFieldsSurvive(t, user)
	assertRawField(t, user, "firstName", "Ann")
	assertRawField(t, user, "isAccessGranted", true)
	assertRawField(t, user, "appUserID", linked.AppUser.ID)
	assertRawField(t, user, "botIDs", []any{"bot-1"})
	assertRawTime(t, user, "dtCreated", seededAt)
	assertRawTime(t, user, "dtUpdated", changeAt)

	chat := readRaw(t, db, chatKey)
	assertForeignFieldsSurvive(t, chat)
	assertRawField(t, chat, "isAccessGranted", true)
	assertRawField(t, chat, "botUserIDs", []any{"user-1"})
	assertRawField(t, chat, "appUserID", linked.AppUser.ID)
	assertRawTime(t, chat, "dtCreated", seededAt)
	assertRawTime(t, chat, "dtUpdated", changeAt)
}

func TestEnsureLinked_LinkingHealsLegacyRecordAndKeepsUnmodelledFields(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	userKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
	chatKey := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
	// Legacy records: no dtCreated, no botIDs / botUserIDs set by this profile.
	user := rawPlatformUser(nil)
	delete(user, "dtCreated")
	delete(user, "botIDs")
	seedRaw(t, db, userKey, user)
	seedRaw(t, db, chatKey, map[string]any{
		"appUserID":     "",
		"dtUpdated":     seededAt,
		"botUserIDs":    []string{"user-1"},
		"foreignString": "kept",
		"foreignMap":    map[string]any{"a": float64(1)},
	})
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))

	if _, err := store.EnsureLinked(context.Background(), newLinkRequest(identity)); err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	userDoc := readRaw(t, db, userKey)
	assertForeignFieldsSurvive(t, userDoc)
	assertRawTime(t, userDoc, "dtCreated", changeAt)
	assertRawField(t, userDoc, "botIDs", []any{"bot-1"})
	chatDoc := readRaw(t, db, chatKey)
	assertForeignFieldsSurvive(t, chatDoc)
	assertRawTime(t, chatDoc, "dtCreated", changeAt)
}

func TestSetPlatformUserAccessGranted_KeepsFieldsTheProfileDoesNotModel(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}
	userKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
	doc := rawPlatformUser(map[string]any{"appUserID": "app-9", "isAccessGranted": false})
	delete(doc, "dtCreated")
	delete(doc, "botIDs")
	seedRaw(t, db, userKey, doc)
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))

	result, err := store.SetPlatformUserAccessGranted(context.Background(), identity, func() botsfwmodels.PlatformUserData {
		return &botsfwmodels.PlatformUserBaseDbo{}
	}, true)
	if err != nil || !result.Data.IsAccessGranted() {
		t.Fatalf("SetPlatformUserAccessGranted() = %#v, %v", result, err)
	}

	got := readRaw(t, db, userKey)
	assertForeignFieldsSurvive(t, got)
	assertRawField(t, got, "firstName", "Ann")
	assertRawField(t, got, "appUserID", "app-9")
	assertRawField(t, got, "isAccessGranted", true)
	assertRawField(t, got, "botIDs", []any{"bot-1"})
	assertRawTime(t, got, "dtCreated", changeAt)
	assertRawTime(t, got, "dtUpdated", changeAt)
}

// noteChat is one service's profile of a chat document: the framework base
// plus its own fields. JSON tags equal the Firestore names so the in-memory
// test database, which stores JSON, sees the same field names Firestore does.
type noteChat struct {
	botsfwmodels.ChatBaseData
	Note  string `json:"note,omitempty" firestore:"note,omitempty"`
	Count int    `json:"count" firestore:"count"`
}

func TestSaveChat_KeepsFieldsTheProfileDoesNotModel(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	chatKey := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))
	ctx := context.Background()

	// First save inserts the whole document.
	chat := &noteChat{
		ChatBaseData: botsfwmodels.ChatBaseData{
			BotBaseData: botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt, AppUserID: "app-1"},
			BotUserIDs:  []string{"user-1"},
		},
		Note:  "first",
		Count: 1,
	}
	if err := store.SaveChat(ctx, identity, chat); err != nil {
		t.Fatalf("first SaveChat() error: %v", err)
	}
	first := readRaw(t, db, chatKey)
	assertRawField(t, first, "note", "first")
	assertRawField(t, first, "count", float64(1))

	// Another service's profile adds fields this type does not model.
	err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, chatKey, []update.Update{
			update.ByFieldName("foreignString", "kept"),
			update.ByFieldName("foreignMap", map[string]any{"a": float64(1)}),
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second save: change one field, clear the omitempty one.
	chat.Count = 2
	chat.Note = ""
	if err := store.SaveChat(ctx, identity, chat); err != nil {
		t.Fatalf("second SaveChat() error: %v", err)
	}
	got := readRaw(t, db, chatKey)
	assertForeignFieldsSurvive(t, got)
	assertRawField(t, got, "count", float64(2))
	if _, has := got["note"]; has {
		t.Errorf("cleared omitempty field %q is still stored: %v", "note", got["note"])
	}
	assertRawField(t, got, "appUserID", "app-1")
	assertRawTime(t, got, "dtCreated", seededAt)
	assertRawTime(t, got, "dtUpdated", changeAt)
}

func TestSaveChat_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	chatKey := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
	newChat := func() *botsfwmodels.ChatBaseData {
		return &botsfwmodels.ChatBaseData{
			BotBaseData: botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt},
			BotUserIDs:  []string{"user-1"},
		}
	}
	seeded := func() *faultyDB {
		db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
		seedRaw(t, db, chatKey, map[string]any{"botUserIDs": []string{"user-1"}})
		return &faultyDB{DB: db}
	}

	t.Run("exists fails", func(t *testing.T) {
		fDB := seeded()
		fDB.failExists = true
		err := NewStateStore(fDB, &testAppUsers{}).SaveChat(ctx, identity, newChat())
		if err == nil || !strings.Contains(err.Error(), "tx.Exists") {
			t.Fatalf("SaveChat() error = %v, want the tx.Exists failure", err)
		}
	})
	t.Run("first insert fails", func(t *testing.T) {
		fDB := &faultyDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile()), failChatSet: true}
		err := NewStateStore(fDB, &testAppUsers{}).SaveChat(ctx, identity, newChat())
		if err == nil || !strings.Contains(err.Error(), "tx.Set chat") {
			t.Fatalf("SaveChat() error = %v, want the tx.Set failure", err)
		}
	})
	t.Run("update fails", func(t *testing.T) {
		fDB := seeded()
		fDB.failChatUpdate = true
		err := NewStateStore(fDB, &testAppUsers{}).SaveChat(ctx, identity, newChat())
		if err == nil || !strings.Contains(err.Error(), "tx.Update chat") {
			t.Fatalf("SaveChat() error = %v, want the tx.Update failure", err)
		}
	})
	t.Run("field enumeration fails", func(t *testing.T) {
		defer func(orig func(any) ([]update.Update, error)) { chatFieldUpdates = orig }(chatFieldUpdates)
		chatFieldUpdates = func(any) ([]update.Update, error) { return nil, errors.New("enumeration failed") }
		err := NewStateStore(seeded(), &testAppUsers{}).SaveChat(ctx, identity, newChat())
		if err == nil || err.Error() != "enumeration failed" {
			t.Fatalf("SaveChat() error = %v, want the enumeration failure", err)
		}
	})
}

// TestFieldNameConstantsMatchModelTags pins the hand-written field names to
// the Firestore tags of the framework models they address.
func TestFieldNameConstantsMatchModelTags(t *testing.T) {
	tagName := func(typ reflect.Type, goName string) string {
		f, ok := typ.FieldByName(goName)
		if !ok {
			t.Fatalf("%v has no field %s", typ, goName)
		}
		name, _, _ := strings.Cut(f.Tag.Get("firestore"), ",")
		return name
	}
	base := reflect.TypeOf(botsfwmodels.BotBaseData{})
	for goName, want := range map[string]string{
		"AppUserID":     fieldAppUserID,
		"DtCreated":     fieldDtCreated,
		"DtUpdated":     fieldDtUpdated,
		"AccessGranted": fieldAccessGranted,
	} {
		if got := tagName(base, goName); got != want {
			t.Errorf("BotBaseData.%s firestore name = %q, constant = %q", goName, got, want)
		}
	}
	if got := tagName(reflect.TypeOf(botsfwmodels.WithBotIDs{}), "BotIDs"); got != fieldBotIDs {
		t.Errorf("WithBotIDs.BotIDs firestore name = %q, constant = %q", got, fieldBotIDs)
	}
}
