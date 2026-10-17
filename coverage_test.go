package botsfwstoredalgo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	dalrecord "github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

func assertPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	f()
}

type faultyDB struct {
	dal.DB
	failGet                        bool
	failTxGet                      bool
	failSet                        bool
	failChatGet                    bool
	failPlatformUserUpdate         bool
	failChatSet                    bool
	failChatUpdate                 bool
	failUpdate                     bool
	failExists                     bool
	simulateConcurrentPlatformUser bool
}

func (d *faultyDB) RunReadwriteTransaction(ctx context.Context, f dal.RWTxWorker, opts ...dal.TransactionOption) error {
	return d.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return f(ctx, &faultyTx{ReadwriteTransaction: tx, parent: d})
	}, opts...)
}

func (d *faultyDB) RunReadonlyTransaction(ctx context.Context, f dal.ROTxWorker, opts ...dal.TransactionOption) error {
	return d.DB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		return f(ctx, &faultyROTx{ReadTransaction: tx, parent: d})
	}, opts...)
}

type faultyTx struct {
	dal.ReadwriteTransaction
	parent *faultyDB
}

func (t *faultyTx) Get(ctx context.Context, rec dalrecord.Record) error {
	if t.parent.failGet || t.parent.failTxGet {
		return errors.New("injected tx.Get failure")
	}
	if t.parent.failChatGet && rec.Key().Collection() == botChatsCollection {
		return errors.New("injected tx.Get chat failure")
	}
	if t.parent.simulateConcurrentPlatformUser && rec.Key().Collection() == botUsersCollection {
		if pu, ok := rec.Data().(botsfwmodels.PlatformUserData); ok {
			pu.SetAppUserID("concurrent-other-app-user")
		}
		return nil
	}
	return t.ReadwriteTransaction.Get(ctx, rec)
}

func (t *faultyTx) Set(ctx context.Context, rec dalrecord.Record) error {
	if t.parent.failSet {
		return errors.New("injected tx.Set failure")
	}
	if t.parent.failChatSet && rec.Key().Collection() == botChatsCollection {
		return errors.New("injected tx.Set chat failure")
	}
	return t.ReadwriteTransaction.Set(ctx, rec)
}

func (t *faultyTx) Update(ctx context.Context, key *dalrecord.Key, updates []update.Update, opts ...dal.Precondition) error {
	if t.parent.failUpdate {
		return errors.New("injected tx.Update failure")
	}
	if t.parent.failPlatformUserUpdate && key.Collection() == botUsersCollection {
		return errors.New("injected tx.Update platform user failure")
	}
	if t.parent.failChatUpdate && key.Collection() == botChatsCollection {
		return errors.New("injected tx.Update chat failure")
	}
	return t.ReadwriteTransaction.Update(ctx, key, updates, opts...)
}

func (t *faultyTx) Exists(ctx context.Context, key *dalrecord.Key) (bool, error) {
	if t.parent.failExists {
		return false, errors.New("injected tx.Exists failure")
	}
	return t.ReadwriteTransaction.Exists(ctx, key)
}

func (t *faultyTx) Insert(ctx context.Context, rec dalrecord.Record, opts ...dal.InsertOption) error {
	if t.parent.failSet {
		return errors.New("injected tx.Insert failure")
	}
	return t.ReadwriteTransaction.Insert(ctx, rec, opts...)
}

type faultyROTx struct {
	dal.ReadTransaction
	parent *faultyDB
}

func (t *faultyROTx) Get(ctx context.Context, rec dalrecord.Record) error {
	if t.parent.failGet {
		return errors.New("injected roTx.Get failure")
	}
	return t.ReadTransaction.Get(ctx, rec)
}

type invalidPlatformUserData struct {
	botsfwmodels.PlatformUserBaseDbo
}

func (invalidPlatformUserData) Validate() error {
	return errors.New("invalid platform user data")
}

type validPlatformUserData struct {
	botsfwmodels.PlatformUserBaseDbo
}

func (validPlatformUserData) Validate() error {
	return nil
}

func TestConstructorsAndKeyPanics(t *testing.T) {
	appUsers := &testAppUsers{}
	assertPanic(t, func() { NewStateStore(nil, appUsers) })
	assertPanic(t, func() { NewStateStoreWithProvider(nil, appUsers) })
	assertPanic(t, func() {
		NewStateStoreWithProvider(func(context.Context) (dal.DB, error) { return nil, nil }, nil)
	})
	assertPanic(t, func() { NewPlatformKey("") })
	assertPanic(t, func() { NewBotKey("", "bot") })
	assertPanic(t, func() { NewBotKey("tg", "") })
	assertPanic(t, func() { NewPlatformUserKey("", "u") })
	assertPanic(t, func() { NewPlatformUserKey("tg", "") })
	assertPanic(t, func() { NewBotChatKey("tg", "bot", "") })
	assertPanic(t, func() { NewWebhookUpdateKey(botsfwstore.WebhookUpdateKey{}) })
}

func TestDBProviderErrors(t *testing.T) {
	ctx := context.Background()
	appUsers := &testAppUsers{}
	storeErr := NewStateStoreWithProvider(func(context.Context) (dal.DB, error) {
		return nil, errors.New("db provider failed")
	}, appUsers)
	storeNil := NewStateStoreWithProvider(func(context.Context) (dal.DB, error) {
		return nil, nil
	}, appUsers)

	key := botsfwstore.WebhookUpdateKey{PlatformID: "tg", BotID: "bot", UpdateID: "1"}
	identity := botsfwstore.Identity{PlatformID: "tg", BotID: "bot", BotUserID: "u1"}
	chatIdentity := botsfwstore.Identity{PlatformID: "tg", BotID: "bot", BotUserID: "u1", ChatID: "c1"}

	// ClaimWebhookUpdate
	if _, err := storeErr.ClaimWebhookUpdate(ctx, key, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected error from ClaimWebhookUpdate on db error")
	}
	// CompleteWebhookUpdate
	if err := storeNil.CompleteWebhookUpdate(ctx, key, "lease1"); err == nil {
		t.Fatal("expected error from CompleteWebhookUpdate on nil db")
	}
	// EnsureLinked
	req := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: func(string) (botsfwmodels.PlatformUserData, error) {
			return &botsfwmodels.PlatformUserBaseDbo{}, nil
		},
	}
	if _, err := storeErr.EnsureLinked(ctx, req); err == nil {
		t.Fatal("expected error from EnsureLinked on db error")
	}
	// PlatformUser
	if _, err := storeErr.PlatformUser(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }); err == nil {
		t.Fatal("expected error from PlatformUser on db error")
	}
	// AppUser
	if _, err := storeErr.AppUser(ctx, "bot", "app1"); err == nil {
		t.Fatal("expected error from AppUser on db error")
	}
	// SaveChat
	if err := storeErr.SaveChat(ctx, chatIdentity, &botsfwmodels.ChatBaseData{}); err == nil {
		t.Fatal("expected error from SaveChat on db error")
	}
	// SetPlatformUserAccessGranted
	if _, err := storeErr.SetPlatformUserAccessGranted(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }, true); err == nil {
		t.Fatal("expected error from SetPlatformUserAccessGranted on db error")
	}
}

func TestWebhookUpdate_EdgeCases(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	fDB := &faultyDB{DB: memDB}
	store := NewStateStore(fDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	validKey := botsfwstore.WebhookUpdateKey{PlatformID: "telegram", BotID: "bot", UpdateID: "u1"}

	// 1. Invalid key validation
	if _, err := store.ClaimWebhookUpdate(ctx, botsfwstore.WebhookUpdateKey{}, now.Add(time.Hour)); err == nil {
		t.Fatal("expected key validation error")
	}
	// 2. leaseUntil in the past or zero
	if _, err := store.ClaimWebhookUpdate(ctx, validKey, time.Time{}); err == nil {
		t.Fatal("expected error on zero leaseUntil")
	}
	if _, err := store.ClaimWebhookUpdate(ctx, validKey, now.Add(-time.Hour)); err == nil {
		t.Fatal("expected error on past leaseUntil")
	}

	// 3. DB Get failure inside tx
	fDB.failGet = true
	if _, err := store.ClaimWebhookUpdate(ctx, validKey, now.Add(time.Hour)); err == nil {
		t.Fatal("expected error when tx.Get fails")
	}
	fDB.failGet = false

	// 5. DB Set failure inside tx
	fDB.failSet = true
	if _, err := store.ClaimWebhookUpdate(ctx, validKey, now.Add(time.Hour)); err == nil {
		t.Fatal("expected error when tx.Set fails")
	}
	fDB.failSet = false

	// 6. CompleteWebhookUpdate / FailWebhookUpdate errors
	if err := store.CompleteWebhookUpdate(ctx, botsfwstore.WebhookUpdateKey{}, "lease1"); err == nil {
		t.Fatal("expected key validation error")
	}
	if err := store.CompleteWebhookUpdate(ctx, validKey, ""); err == nil {
		t.Fatal("expected error on empty leaseID")
	}
	// When update record does not exist
	if err := store.CompleteWebhookUpdate(ctx, validKey, "lease1"); err == nil {
		t.Fatal("expected error when record not found")
	}
}

func TestPlatformUser_Methods(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	fDB := &faultyDB{DB: memDB}
	store := NewStateStore(fDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "u1", ChatID: "c1"}

	// 1. GetPlatformUser
	puData := &botsfwmodels.PlatformUserBaseDbo{}
	_, err := GetPlatformUser(ctx, memDB, "telegram", "u1", puData)
	if err == nil {
		t.Fatal("expected not found from GetPlatformUser")
	}

	// 2. CreatePlatformUserRecord validation
	_ = memDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := CreatePlatformUserRecord(ctx, tx, "telegram", "u_inv", &invalidPlatformUserData{}); err == nil {
			t.Fatal("expected validation error from CreatePlatformUserRecord")
		}
		if err := CreatePlatformUserRecord(ctx, tx, "telegram", "u_val", &validPlatformUserData{}); err != nil {
			t.Fatalf("expected nil from valid CreatePlatformUserRecord, got: %v", err)
		}
		return nil
	})

	// 3. PlatformUser method
	if _, err := store.PlatformUser(ctx, botsfwstore.Identity{}, nil); err == nil {
		t.Fatal("expected identity validation error")
	}
	if _, err := store.PlatformUser(ctx, identity, nil); err == nil {
		t.Fatal("expected nil newData error")
	}
	if _, err := store.PlatformUser(ctx, identity, func() botsfwmodels.PlatformUserData { return nil }); err == nil {
		t.Fatal("expected nil newData return error")
	}
	if _, err := store.PlatformUser(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }); !errors.Is(err, botsfwstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	// Insert platform user and verify PlatformUser succeeds
	err = memDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now, AppUserID: "app1"},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{"bot"}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	pu, err := store.PlatformUser(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} })
	if err != nil || pu.ID != identity.BotUserID {
		t.Fatalf("PlatformUser() failed: %v", err)
	}
}

func TestAppUser_Methods(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	appUsers := &testAppUsers{}
	store := NewStateStore(memDB, appUsers, WithClock(func() time.Time { return now }))

	// Validation
	if _, err := store.AppUser(ctx, "", "app1"); err == nil {
		t.Fatal("expected error on empty botID")
	}
	if _, err := store.AppUser(ctx, "bot", ""); err == nil {
		t.Fatal("expected error on empty appUserID")
	}

	// Happy path
	user, err := store.AppUser(ctx, "bot", "app1")
	if err != nil || user.ID != "app1" {
		t.Fatalf("AppUser() = %#v, %v", user, err)
	}
}

func TestSaveChat_Methods(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	store := NewStateStore(memDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "u1", ChatID: "c1"}

	if err := store.SaveChat(ctx, botsfwstore.Identity{}, nil); err == nil {
		t.Fatal("expected validation error")
	}
	noChatID := identity
	noChatID.ChatID = ""
	if err := store.SaveChat(ctx, noChatID, &botsfwmodels.ChatBaseData{}); err == nil {
		t.Fatal("expected empty chat ID error")
	}
	if err := store.SaveChat(ctx, identity, nil); err == nil {
		t.Fatal("expected nil data error")
	}

	chatData := &botsfwmodels.ChatBaseData{
		BotBaseData: botsfwmodels.BotBaseData{AppUserID: "app1"},
		BotUserIDs:  []string{identity.BotUserID},
	}
	if err := store.SaveChat(ctx, identity, chatData); err != nil {
		t.Fatalf("SaveChat() error: %v", err)
	}
}

func TestSetPlatformUserAccessGranted_Methods(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	fDB := &faultyDB{DB: memDB}
	store := NewStateStore(fDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "u1", ChatID: "c1"}

	if _, err := store.SetPlatformUserAccessGranted(ctx, botsfwstore.Identity{}, nil, true); err == nil {
		t.Fatal("expected identity validation error")
	}
	if _, err := store.SetPlatformUserAccessGranted(ctx, identity, nil, true); err == nil {
		t.Fatal("expected nil newData error")
	}
	if _, err := store.SetPlatformUserAccessGranted(ctx, identity, func() botsfwmodels.PlatformUserData { return nil }, true); err == nil {
		t.Fatal("expected nil newData return error")
	}
	// User not found
	if _, err := store.SetPlatformUserAccessGranted(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }, true); !errors.Is(err, botsfwstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	// Insert platform user
	err := memDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now, AppUserID: "app1"},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{"bot"}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fail on Update
	fDB.failUpdate = true
	if _, err := store.SetPlatformUserAccessGranted(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }, true); err == nil {
		t.Fatal("expected error on tx.Update failure")
	}
	fDB.failUpdate = false

	// Happy path
	pu, err := store.SetPlatformUserAccessGranted(ctx, identity, func() botsfwmodels.PlatformUserData { return &botsfwmodels.PlatformUserBaseDbo{} }, true)
	if err != nil || !pu.Data.IsAccessGranted() {
		t.Fatalf("SetPlatformUserAccessGranted failed: %v", err)
	}
}

type faultyAppUsers struct {
	testAppUsers
	failPrepare  bool
	emptyPrepID  bool
	failEnsure   bool
	emptyEnsID   bool
	diffEnsID    bool
	failAppUser  bool
	emptyAppUser bool
	diffAppUser  bool
}

func (s *faultyAppUsers) PrepareAppUser(ctx context.Context, id botsfwstore.Identity) (botsfwstore.AppUser, error) {
	if s.failPrepare {
		return botsfwstore.AppUser{}, errors.New("prepare failed")
	}
	if s.emptyPrepID {
		return botsfwstore.AppUser{ID: ""}, nil
	}
	return s.testAppUsers.PrepareAppUser(ctx, id)
}

func (s *faultyAppUsers) EnsureAppUser(ctx context.Context, tx dal.ReadwriteTransaction, id botsfwstore.Identity, prepared botsfwstore.AppUser) (botsfwstore.AppUser, error) {
	if s.failEnsure {
		return botsfwstore.AppUser{}, errors.New("ensure failed")
	}
	if s.emptyEnsID {
		return botsfwstore.AppUser{ID: ""}, nil
	}
	if s.diffEnsID {
		return botsfwstore.AppUser{ID: "diff-id"}, nil
	}
	return s.testAppUsers.EnsureAppUser(ctx, tx, id, prepared)
}

func (s *faultyAppUsers) AppUser(ctx context.Context, tx dal.ReadSession, botID string, id string) (botsfwstore.AppUser, error) {
	if s.failAppUser {
		return botsfwstore.AppUser{}, errors.New("appUser failed")
	}
	if s.emptyAppUser {
		return botsfwstore.AppUser{ID: ""}, nil
	}
	if s.diffAppUser {
		return botsfwstore.AppUser{ID: "diff-loaded-id"}, nil
	}
	return s.testAppUsers.AppUser(ctx, tx, botID, id)
}

func TestEnsureLinked_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "u1", ChatID: "c1"}

	// 1. request.Validate error
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	store := NewStateStore(memDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := store.EnsureLinked(ctx, botsfwstore.LinkRequest{}); err == nil {
		t.Fatal("expected request validation error")
	}

	baseReq := func() botsfwstore.LinkRequest {
		return botsfwstore.LinkRequest{
			Identity: identity,
			ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
				return &botsfwmodels.PlatformUserBaseDbo{}
			},
			NewPlatformUserData: func(appUserID string) (botsfwmodels.PlatformUserData, error) {
				return &botsfwmodels.PlatformUserBaseDbo{BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID}}, nil
			},
			NewChatData: func(appUserID string, _ bool) (botsfwmodels.BotChatData, error) {
				return &botsfwmodels.ChatBaseData{
					BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID},
					BotUserIDs:  []string{identity.BotUserID},
				}, nil
			},
		}
	}

	// 2. readPlatformUser factory returns nil
	reqNilFactory := baseReq()
	reqNilFactory.ReadPlatformUserData = func() botsfwmodels.PlatformUserData { return nil }
	if _, err := store.EnsureLinked(ctx, reqNilFactory); err == nil {
		t.Fatal("expected error when ReadPlatformUserData returns nil")
	}

	// 3. readPlatformUser DB Get error
	fDB := &faultyDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile()), failGet: true}
	fStore := NewStateStore(fDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := fStore.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error when readPlatformUser tx.Get fails")
	}

	// 4. PrepareAppUser failure
	appFail := &faultyAppUsers{failPrepare: true}
	storeFail := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appFail, WithClock(func() time.Time { return now }))
	if _, err := storeFail.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error on PrepareAppUser failure")
	}

	// 5. PrepareAppUser returns empty ID
	appEmptyID := &faultyAppUsers{emptyPrepID: true}
	storeEmptyID := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appEmptyID, WithClock(func() time.Time { return now }))
	if _, err := storeEmptyID.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error on empty prepared app user ID")
	}

	// 6. Inside tx: ReadPlatformUserData returns nil on second call
	count := 0
	reqNilSecond := baseReq()
	reqNilSecond.ReadPlatformUserData = func() botsfwmodels.PlatformUserData {
		count++
		if count > 1 {
			return nil
		}
		return &botsfwmodels.PlatformUserBaseDbo{}
	}
	if _, err := store.EnsureLinked(ctx, reqNilSecond); err == nil {
		t.Fatal("expected error when ReadPlatformUserData returns nil inside tx")
	}

	// 7. Inside tx: GetRecordWithIDIntoData for platform user fails
	fDB2 := &faultyDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile()), failTxGet: true}
	storeFDB2 := NewStateStore(fDB2, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := storeFDB2.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error when tx.Get fails inside EnsureLinked")
	}

	// 8. NewPlatformUserData returns error
	reqNewPUErr := baseReq()
	reqNewPUErr.NewPlatformUserData = func(string) (botsfwmodels.PlatformUserData, error) {
		return nil, errors.New("new platform user err")
	}
	if _, err := store.EnsureLinked(ctx, reqNewPUErr); err == nil {
		t.Fatal("expected error from NewPlatformUserData")
	}

	// 9. NewPlatformUserData returns nil
	reqNewPUNil := baseReq()
	reqNewPUNil.NewPlatformUserData = func(string) (botsfwmodels.PlatformUserData, error) {
		return nil, nil
	}
	if _, err := store.EnsureLinked(ctx, reqNewPUNil); err == nil {
		t.Fatal("expected error from NewPlatformUserData returning nil")
	}

	// 10. NewChatData returns error
	reqNewChatErr := baseReq()
	reqNewChatErr.NewChatData = func(string, bool) (botsfwmodels.BotChatData, error) {
		return nil, errors.New("new chat err")
	}
	if _, err := store.EnsureLinked(ctx, reqNewChatErr); err == nil {
		t.Fatal("expected error from NewChatData")
	}

	// 11. NewChatData returns nil
	reqNewChatNil := baseReq()
	reqNewChatNil.NewChatData = func(string, bool) (botsfwmodels.BotChatData, error) {
		return nil, nil
	}
	if _, err := store.EnsureLinked(ctx, reqNewChatNil); err == nil {
		t.Fatal("expected error from NewChatData returning nil")
	}

	// 12. EnsureAppUser fails
	appEnsureFail := &faultyAppUsers{failEnsure: true}
	storeEnsureFail := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appEnsureFail, WithClock(func() time.Time { return now }))
	if _, err := storeEnsureFail.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error from EnsureAppUser")
	}

	// 13. EnsureAppUser returns empty ID
	appEnsureEmpty := &faultyAppUsers{emptyEnsID: true}
	storeEnsureEmpty := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appEnsureEmpty, WithClock(func() time.Time { return now }))
	if _, err := storeEnsureEmpty.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error when EnsureAppUser returns empty ID")
	}

	// 14. EnsureAppUser returns diff ID
	appEnsureDiff := &faultyAppUsers{diffEnsID: true}
	storeEnsureDiff := NewStateStore(dalgo2memory.New(dalgo2memory.FirestoreProfile()), appEnsureDiff, WithClock(func() time.Time { return now }))
	if _, err := storeEnsureDiff.EnsureLinked(ctx, baseReq()); !errors.Is(err, botsfwstore.ErrIdentityConflict) {
		t.Fatalf("expected ErrIdentityConflict, got: %v", err)
	}

	// 15. tx.Insert platform user fails
	fDBInsert := &faultyDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile()), failSet: true}
	storeInsertFail := NewStateStore(fDBInsert, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := storeInsertFail.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error when tx.Insert platform user fails")
	}

	// 16. ChatID == "" happy path
	reqNoChat := baseReq()
	reqNoChat.Identity.ChatID = ""
	resNoChat, err := store.EnsureLinked(ctx, reqNoChat)
	if err != nil || resNoChat.ChatData != nil {
		t.Fatalf("expected nil chatData when ChatID is empty, got: %v", err)
	}
}

func TestEnsureLinked_ExistingUserAndChatBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "u_exist", ChatID: "c_exist"}

	baseReq := func() botsfwstore.LinkRequest {
		return botsfwstore.LinkRequest{
			Identity: identity,
			ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
				return &botsfwmodels.PlatformUserBaseDbo{}
			},
			NewPlatformUserData: func(appUserID string) (botsfwmodels.PlatformUserData, error) {
				return &botsfwmodels.PlatformUserBaseDbo{BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID}}, nil
			},
			NewChatData: func(appUserID string, _ bool) (botsfwmodels.BotChatData, error) {
				return &botsfwmodels.ChatBaseData{
					BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID},
					BotUserIDs:  []string{identity.BotUserID},
				}, nil
			},
		}
	}

	// 1. Linking existing platform user that has NO AppUserID (platformAppUserID == "")
	memDB := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	store := NewStateStore(memDB, &testAppUsers{}, WithClock(func() time.Time { return now }))
	err := memDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{"bot"}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := store.EnsureLinked(ctx, baseReq())
	if err != nil || linked.PlatformUser.Data.GetAppUserID() != "app-1" {
		t.Fatalf("EnsureLinked for existing unlinked user failed: %v", err)
	}

	// 2. Existing platform user already linked to "app-1":
	// AppUser lookup fails:
	appUsersFail := &faultyAppUsers{failAppUser: true}
	storeAppUserFail := NewStateStore(memDB, appUsersFail, WithClock(func() time.Time { return now }))
	if _, err := storeAppUserFail.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error when AppUser fails")
	}

	// 3. AppUser lookup returns different ID
	appUsersDiff := &faultyAppUsers{diffAppUser: true}
	storeAppUserDiff := NewStateStore(memDB, appUsersDiff, WithClock(func() time.Time { return now }))
	if _, err := storeAppUserDiff.EnsureLinked(ctx, baseReq()); !errors.Is(err, botsfwstore.ErrIdentityConflict) {
		t.Fatalf("expected ErrIdentityConflict when AppUser returns diff ID, got: %v", err)
	}

	// 4. Chat exists with empty AppUserID
	identityEmptyChat := identity
	identityEmptyChat.ChatID = "c_unlinked"
	reqEmptyChat := baseReq()
	reqEmptyChat.Identity = identityEmptyChat
	err = memDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		chatKey := NewBotChatKey(identityEmptyChat.PlatformID, identityEmptyChat.BotID, identityEmptyChat.ChatID)
		return tx.Insert(ctx, dalrecord.NewRecordWithData(chatKey, &botsfwmodels.ChatBaseData{
			BotBaseData: botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now},
			BotUserIDs:  []string{identityEmptyChat.BotUserID},
		}))
	})
	if err != nil {
		t.Fatal(err)
	}
	linkedWithChat, err := store.EnsureLinked(ctx, reqEmptyChat)
	if err != nil || linkedWithChat.ChatData.GetAppUserID() != "app-1" {
		t.Fatalf("EnsureLinked for existing unlinked chat failed: %v", err)
	}

	// 5. Chat exists with conflicting AppUserID
	identityConflictChat := identity
	identityConflictChat.ChatID = "c_conflict"
	reqConflictChat := baseReq()
	reqConflictChat.Identity = identityConflictChat
	err = memDB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		chatKey := NewBotChatKey(identityConflictChat.PlatformID, identityConflictChat.BotID, identityConflictChat.ChatID)
		return tx.Insert(ctx, dalrecord.NewRecordWithData(chatKey, &botsfwmodels.ChatBaseData{
			BotBaseData: botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now, AppUserID: "other-app-user"},
			BotUserIDs:  []string{identityConflictChat.BotUserID},
		}))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureLinked(ctx, reqConflictChat); !errors.Is(err, botsfwstore.ErrIdentityConflict) {
		t.Fatalf("expected ErrIdentityConflict when chat has conflicting AppUserID, got: %v", err)
	}
}

type wrapperPlatformUserData struct {
	botsfwmodels.PlatformUserData
}

func (w *wrapperPlatformUserData) GetAppUserID() string {
	return ""
}

func TestEnsureLinked_RemainingGaps(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot", BotUserID: "u_gap", ChatID: "c_gap"}

	baseReq := func() botsfwstore.LinkRequest {
		return botsfwstore.LinkRequest{
			Identity: identity,
			ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
				return &botsfwmodels.PlatformUserBaseDbo{}
			},
			NewPlatformUserData: func(appUserID string) (botsfwmodels.PlatformUserData, error) {
				return &botsfwmodels.PlatformUserBaseDbo{BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID}}, nil
			},
			NewChatData: func(appUserID string, _ bool) (botsfwmodels.BotChatData, error) {
				return &botsfwmodels.ChatBaseData{
					BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID},
					BotUserIDs:  []string{identity.BotUserID},
				}, nil
			},
		}
	}

	// 1. store.go:350: platformAppUserID == "" && !preparedAppUser
	memDB1 := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	_ = memDB1.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now, AppUserID: "app1"},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{identity.BotID}},
		})
	})
	store1 := NewStateStore(memDB1, &testAppUsers{}, WithClock(func() time.Time { return now }))
	req1 := baseReq()
	callCount := 0
	req1.ReadPlatformUserData = func() botsfwmodels.PlatformUserData {
		callCount++
		if callCount > 1 {
			return &wrapperPlatformUserData{PlatformUserData: &botsfwmodels.PlatformUserBaseDbo{}}
		}
		return &botsfwmodels.PlatformUserBaseDbo{}
	}
	if _, err := store1.EnsureLinked(ctx, req1); err == nil || err.Error() != "application user was not prepared for an unlinked platform identity" {
		t.Fatalf("expected 'application user was not prepared for an unlinked platform identity', got: %v", err)
	}

	// 2. store.go:355: preparedAppUser && platformAppUserID != prepared.ID
	fDB2 := &faultyDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile()), simulateConcurrentPlatformUser: true}
	store2 := NewStateStore(fDB2, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := store2.EnsureLinked(ctx, baseReq()); !errors.Is(err, botsfwstore.ErrIdentityConflict) {
		t.Fatalf("expected ErrIdentityConflict when platformAppUserID != prepared.ID, got: %v", err)
	}

	// 3. store.go:362: RequireAppUserID(resolvedAppUser.ID) returns error (emptyAppUser)
	memDB3 := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	_ = memDB3.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now, AppUserID: "app1"},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{identity.BotID}},
		})
	})
	store3 := NewStateStore(memDB3, &faultyAppUsers{emptyAppUser: true}, WithClock(func() time.Time { return now }))
	if _, err := store3.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error on emptyAppUser")
	}

	// 4. store.go:422: tx.Get on chatKey fails with non-not-found error
	fDB4 := &faultyDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile()), failChatGet: true}
	store4 := NewStateStore(fDB4, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := store4.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error on chatKey tx.Get failure")
	}

	// 5. tx.Update on platformUserKey fails when platformAppUserID == ""
	memDB5 := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	_ = memDB5.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return CreatePlatformUserRecord(ctx, tx, identity.PlatformID, identity.BotUserID, &botsfwmodels.PlatformUserBaseDbo{
			BotBaseData:        botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now},
			WithRequiredBotIDs: botsfwmodels.WithRequiredBotIDs{BotIDs: []string{identity.BotID}},
		})
	})
	fDB5 := &faultyDB{DB: memDB5, failPlatformUserUpdate: true}
	store5 := NewStateStore(fDB5, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := store5.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error on platformUser tx.Update failure")
	}

	// 6. tx.Update on chatKey fails when chatData.GetAppUserID() == ""
	memDB6 := dalgo2memory.New(dalgo2memory.FirestoreProfile())
	_ = memDB6.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		chatKey := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
		return tx.Insert(ctx, dalrecord.NewRecordWithData(chatKey, &botsfwmodels.ChatBaseData{
			BotBaseData: botsfwmodels.BotBaseData{DtCreated: now, DtUpdated: now},
			BotUserIDs:  []string{identity.BotUserID},
		}))
	})
	fDB6 := &faultyDB{DB: memDB6, failChatUpdate: true}
	store6 := NewStateStore(fDB6, &testAppUsers{}, WithClock(func() time.Time { return now }))
	if _, err := store6.EnsureLinked(ctx, baseReq()); err == nil {
		t.Fatal("expected error on chat tx.Update failure")
	}
}
