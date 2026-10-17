package botsfwstoredalgo

import (
	"context"
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

func TestEnsureLinked_LinkingKeepsPlatformUserFieldsTheProfileDoesNotModel(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}
	userKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
	seedRaw(t, db, userKey, rawPlatformUser(nil))
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

}

func TestEnsureLinked_LinkingHealsLegacyPlatformUserAndKeepsUnmodelledFields(t *testing.T) {
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}
	userKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
	// Legacy record: no dtCreated, no botIDs.
	user := rawPlatformUser(nil)
	delete(user, "dtCreated")
	delete(user, "botIDs")
	seedRaw(t, db, userKey, user)
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))

	if _, err := store.EnsureLinked(context.Background(), newLinkRequest(identity)); err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}

	userDoc := readRaw(t, db, userKey)
	assertForeignFieldsSurvive(t, userDoc)
	assertRawTime(t, userDoc, "dtCreated", changeAt)
	assertRawField(t, userDoc, "botIDs", []any{"bot-1"})
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

// TestFieldNameConstantsMatchModelTags pins the hand-written field names to
// the Firestore and JSON tags of the framework models they address.
func TestFieldNameConstantsMatchModelTags(t *testing.T) {
	tagName := func(typ reflect.Type, goName string) string {
		f, ok := typ.FieldByName(goName)
		if !ok {
			t.Fatalf("%v has no field %s", typ, goName)
		}
		name, _, _ := strings.Cut(f.Tag.Get("firestore"), ",")
		// dalgo2memory stores JSON, so the json name must agree too.
		if jsonName, _, _ := strings.Cut(f.Tag.Get("json"), ","); jsonName != name {
			t.Errorf("%v.%s json name %q differs from firestore name %q", typ, goName, jsonName, name)
		}
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

func rawKeysAreUnique(t *testing.T, doc map[string]any) {
	t.Helper()
	seen := map[string]string{}
	for k := range doc {
		if other, dup := seen[strings.ToLower(k)]; dup {
			t.Errorf("document holds keys %q and %q that differ only in case: %v", other, k, doc)
		}
		seen[strings.ToLower(k)] = k
	}
}

// TestPlatformUserUpdates_OnPlainFrameworkTypeInMemoryDB runs the unmodified
// framework platform-user type (no extra json tags) through the field-update
// paths on dalgo2memory, which stores JSON, and checks the values read back as
// a typed record, that the stored keys are not duplicated in another case, and
// that a field the type does not model survives.
func TestPlatformUserUpdates_OnPlainFrameworkTypeInMemoryDB(t *testing.T) {
	ctx := context.Background()
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1"}
	userKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
	err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: seededAt},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{"other-bot"}},
			FirstName:          "Ann",
			LastName:           "Lee",
			UserName:           "annlee",
		}); err != nil {
			return err
		}
		return tx.Update(ctx, userKey, []update.Update{update.ByFieldName("foreignString", "kept")})
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))
	readTyped := func() *botsfwmodels.PlatformUserBaseDbo {
		data := &botsfwmodels.PlatformUserBaseDbo{}
		if _, err := GetPlatformUser(ctx, db, identity.PlatformID, identity.BotUserID, data); err != nil {
			t.Fatal(err)
		}
		return data
	}

	linked, err := store.EnsureLinked(ctx, newLinkRequest(identity))
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}
	got := readTyped()
	if got.AppUserID != linked.AppUser.ID || got.AppUserID == "" {
		t.Errorf("AppUserID = %q, want %q", got.AppUserID, linked.AppUser.ID)
	}
	if !reflect.DeepEqual(got.BotIDs, []string{"other-bot", "bot-1"}) {
		t.Errorf("BotIDs = %v, want the existing id plus this bot", got.BotIDs)
	}
	if !got.DtCreated.Equal(seededAt) || !got.DtUpdated.Equal(changeAt) {
		t.Errorf("DtCreated/DtUpdated = %v/%v, want %v/%v", got.DtCreated, got.DtUpdated, seededAt, changeAt)
	}
	if got.FirstName != "Ann" || got.LastName != "Lee" || got.UserName != "annlee" {
		t.Errorf("modelled names changed: %+v", got)
	}
	raw := readRaw(t, db, userKey)
	rawKeysAreUnique(t, raw)
	assertRawField(t, raw, "foreignString", "kept")

	for _, grant := range []bool{true, false} {
		result, err := store.SetPlatformUserAccessGranted(ctx, identity, func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		}, grant)
		if err != nil || result.Data.IsAccessGranted() != grant {
			t.Fatalf("SetPlatformUserAccessGranted(%v) = %#v, %v", grant, result, err)
		}
		got = readTyped()
		if got.AccessGranted != grant || got.AppUserID != linked.AppUser.ID || got.FirstName != "Ann" {
			t.Errorf("after access=%v, read back %+v", grant, got)
		}
		raw = readRaw(t, db, userKey)
		rawKeysAreUnique(t, raw)
		assertRawField(t, raw, "foreignString", "kept")
	}
}

// TestSaveChat_ClearedFieldsAreClearedOnInMemoryDB guards the chat path, which
// still replaces the whole document: a framework chat type has no json tags, so
// a field-by-firestore-name delete would miss the stored key on dalgo2memory
// and a cleared wizard state would read back.
func TestSaveChat_ClearedFieldsAreClearedOnInMemoryDB(t *testing.T) {
	ctx := context.Background()
	db := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return changeAt }))
	chat := &botsfwmodels.ChatBaseData{BotUserIDs: []string{"user-1"}, Title: "T"}
	chat.SetAwaitingReplyTo("x")
	if err := store.SaveChat(ctx, identity, chat); err != nil {
		t.Fatal(err)
	}
	chat.SetAwaitingReplyTo("")
	chat.Title = ""
	if err := store.SaveChat(ctx, identity, chat); err != nil {
		t.Fatal(err)
	}
	got := &botsfwmodels.ChatBaseData{}
	if _, err := dal.GetRecordWithIDIntoData(ctx, db, NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID), identity.ChatID, got); err != nil {
		t.Fatal(err)
	}
	if got.GetAwaitingReplyTo() != "" || got.Title != "" {
		t.Errorf("cleared fields read back as awaitingReplyTo=%q title=%q", got.GetAwaitingReplyTo(), got.Title)
	}
}
