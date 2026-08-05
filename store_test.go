package botsfwstoredalgo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func TestWebhookUpdateInbox_ClaimCompleteAndLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	db := dalgo2memory.NewDB()
	store := NewStateStore(db, &testAppUsers{}, WithClock(func() time.Time { return now }))
	key := botsfwstore.WebhookUpdateKey{PlatformID: "telegram", BotID: "bot", UpdateID: "42"}
	claim, err := store.ClaimWebhookUpdate(context.Background(), key, now.Add(time.Minute))
	if err != nil || !claim.CanDispatch() || claim.Attempts != 1 {
		t.Fatalf("first claim = %#v, %v", claim, err)
	}
	leased, err := store.ClaimWebhookUpdate(context.Background(), key, now.Add(time.Minute))
	if err != nil || leased.Status != botsfwstore.WebhookUpdateClaimLeased {
		t.Fatalf("concurrent claim = %#v, %v", leased, err)
	}
	now = now.Add(2 * time.Minute)
	retry, err := store.ClaimWebhookUpdate(context.Background(), key, now.Add(time.Minute))
	if err != nil || !retry.CanDispatch() || retry.Attempts != 2 || retry.LeaseID == claim.LeaseID {
		t.Fatalf("expired-lease claim = %#v, %v", retry, err)
	}
	if err = store.CompleteWebhookUpdate(context.Background(), key, retry.LeaseID); err != nil {
		t.Fatal(err)
	}
	done, err := store.ClaimWebhookUpdate(context.Background(), key, now.Add(time.Minute))
	if err != nil || done.Status != botsfwstore.WebhookUpdateClaimCompleted {
		t.Fatalf("completed claim = %#v, %v", done, err)
	}
	if err = store.CompleteWebhookUpdate(context.Background(), key, claim.LeaseID); err == nil {
		t.Fatal("stale lease completed update")
	}
	failedKey := botsfwstore.WebhookUpdateKey{PlatformID: "telegram", BotID: "bot", UpdateID: "43"}
	failed, err := store.ClaimWebhookUpdate(context.Background(), failedKey, now.Add(time.Minute))
	if err != nil || !failed.CanDispatch() {
		t.Fatalf("failed-key claim = %#v, %v", failed, err)
	}
	if err = store.FailWebhookUpdate(context.Background(), failedKey, failed.LeaseID, botsfwstore.WebhookUpdateFailureCode("provider payload: token=secret")); err != nil {
		t.Fatal(err)
	}
	failedData := &webhookUpdateData{}
	if _, err = dal.GetRecordWithIDIntoData(context.Background(), db, NewWebhookUpdateKey(failedKey), failedKey.UpdateID, failedData); err != nil {
		t.Fatal(err)
	}
	if failedData.FailureCode != string(botsfwstore.WebhookUpdateFailureUnknown) {
		t.Fatalf("unsafe failure code was persisted as %q", failedData.FailureCode)
	}
	retried, err := store.ClaimWebhookUpdate(context.Background(), failedKey, now.Add(time.Minute))
	if err != nil || !retried.CanDispatch() || retried.Attempts != 2 {
		t.Fatalf("failed-key retry = %#v, %v", retried, err)
	}
}

func TestWebhookUpdateInbox_ConcurrentClaimHasOneOwner(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := NewStateStore(dalgo2memory.NewDB(), &testAppUsers{}, WithClock(func() time.Time { return now }))
	key := botsfwstore.WebhookUpdateKey{PlatformID: "telegram", BotID: "bot", UpdateID: "concurrent"}
	const callers = 12
	claims := make(chan botsfwstore.WebhookUpdateClaim, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := store.ClaimWebhookUpdate(context.Background(), key, now.Add(time.Minute))
			claims <- claim
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	acquired, leased := 0, 0
	for claim := range claims {
		switch claim.Status {
		case botsfwstore.WebhookUpdateClaimAcquired:
			acquired++
		case botsfwstore.WebhookUpdateClaimLeased:
			leased++
		default:
			t.Fatalf("unexpected concurrent claim %#v", claim)
		}
	}
	if acquired != 1 || leased != callers-1 {
		t.Fatalf("acquired=%d, leased=%d; want 1 and %d", acquired, leased, callers-1)
	}
}

func TestWebhookUpdateInbox_UntrustedUpdateIDsAreOpaqueAndStable(t *testing.T) {
	keys := []botsfwstore.WebhookUpdateKey{
		{PlatformID: "telegram", BotID: "bot", UpdateID: "/provider/path/with/slashes"},
		{PlatformID: "telegram", BotID: "bot", UpdateID: "🧾-更新"},
		{PlatformID: "telegram", BotID: "bot", UpdateID: string(make([]byte, 20_000))},
	}
	for _, input := range keys {
		t.Run("opaque", func(t *testing.T) {
			key1 := NewWebhookUpdateKey(input)
			key2 := NewWebhookUpdateKey(input)
			key1ID, key2ID := fmt.Sprint(key1.ID), fmt.Sprint(key2.ID)
			if key1ID != key2ID || len(key1ID) != 64 {
				t.Fatalf("hashed document IDs = %q and %q", key1.ID, key2.ID)
			}
			if key1ID == input.UpdateID {
				t.Fatal("raw provider update ID was used as a document key")
			}
		})
	}
}

type testAppUsers struct {
	prepareCalls int
	ensureCalls  int
	loadedID     string
}

type persistedAppUser struct {
	Name string
}

type transactionalAppUsers struct {
	tx dal.ReadwriteTransaction
}

func (transactionalAppUsers) PrepareAppUser(context.Context, botsfwstore.Identity) (botsfwstore.AppUser, error) {
	return botsfwstore.AppUser{ID: "app-atomic"}, nil
}

func (s *transactionalAppUsers) EnsureAppUser(ctx context.Context, tx dal.ReadwriteTransaction, _ botsfwstore.Identity, prepared botsfwstore.AppUser) (botsfwstore.AppUser, error) {
	s.tx = tx
	key := record.NewKeyWithID("appUsers", prepared.ID)
	if err := tx.Set(ctx, record.NewRecordWithData(key, &persistedAppUser{Name: "prepared"})); err != nil {
		return botsfwstore.AppUser{}, err
	}
	return prepared, nil
}

func (transactionalAppUsers) AppUser(_ context.Context, _ dal.ReadSession, _ string, id string) (botsfwstore.AppUser, error) {
	return botsfwstore.AppUser{ID: id}, nil
}

type trackingDB struct {
	dal.DB
	tx dal.ReadwriteTransaction
}

func (db *trackingDB) RunReadwriteTransaction(ctx context.Context, f dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		db.tx = tx
		return f(ctx, tx)
	}, options...)
}

func (s *testAppUsers) PrepareAppUser(_ context.Context, _ botsfwstore.Identity) (botsfwstore.AppUser, error) {
	s.prepareCalls++
	return botsfwstore.AppUser{ID: "app-1"}, nil
}

func (s *testAppUsers) EnsureAppUser(_ context.Context, _ dal.ReadwriteTransaction, _ botsfwstore.Identity, prepared botsfwstore.AppUser) (botsfwstore.AppUser, error) {
	s.ensureCalls++
	return prepared, nil
}

func (s *testAppUsers) AppUser(_ context.Context, _ dal.ReadSession, _ string, id string) (botsfwstore.AppUser, error) {
	if s.loadedID != "" {
		id = s.loadedID
	}
	return botsfwstore.AppUser{ID: id}, nil
}

func TestEnsureLinked_PreservesHistoricalKeys(t *testing.T) {
	appUsers := &testAppUsers{}
	store := NewStateStore(dalgo2memory.NewDB(), appUsers)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: func(appUserID string) (botsfwmodels.PlatformUserData, error) {
			return &botsfwmodels.PlatformUserBaseDbo{BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID}}, nil
		},
		NewChatData: func(appUserID string, _ bool) (botsfwmodels.BotChatData, error) {
			// The real factory (SetBotChatFields) always calls SetBotUserID, and
			// ChatBaseData.Validate() requires botUserIDs. Mirror production here:
			// dalgo v0.64 validates on the write path, so an incomplete chat is
			// now rejected at insert instead of being silently stored.
			// SetBotUserID is abstract on ChatBaseData (it panics unless a
			// platform subclass overrides it), so set the field directly.
			return &botsfwmodels.ChatBaseData{
				BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID},
				BotUserIDs:  []string{identity.BotUserID},
			}, nil
		},
	}

	linked, err := store.EnsureLinked(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureLinked() error: %v", err)
	}
	if linked.AppUser.ID != "app-1" || linked.PlatformUser.Data.GetAppUserID() != "app-1" {
		t.Fatalf("unexpected linked identity: %#v", linked)
	}
	if got := NewPlatformUserKey(identity.PlatformID, identity.BotUserID); got.Collection() != botUsersCollection || got.Parent().Collection() != botPlatformsCollection {
		t.Fatalf("platform-user key changed: %v", got)
	}
	if got := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID); got.Collection() != botChatsCollection || got.Parent().Collection() != botsCollection {
		t.Fatalf("bot-chat key changed: %v", got)
	}

	user, err := store.SetPlatformUserAccessGranted(context.Background(), identity, request.ReadPlatformUserData, true)
	if err != nil {
		t.Fatalf("SetPlatformUserAccessGranted() error: %v", err)
	}
	if !user.Data.IsAccessGranted() {
		t.Fatal("access flag was not persisted")
	}

	if _, err = store.EnsureLinked(context.Background(), request); err != nil {
		t.Fatalf("second EnsureLinked() error: %v", err)
	}
	if appUsers.prepareCalls != 1 || appUsers.ensureCalls != 1 {
		t.Fatalf("app-user calls = prepare:%d ensure:%d, want 1 each", appUsers.prepareCalls, appUsers.ensureCalls)
	}

	appUsers.loadedID = "app-2"
	if _, err = store.EnsureLinked(context.Background(), request); !errors.Is(err, botsfwstore.ErrIdentityConflict) {
		t.Fatalf("EnsureLinked() conflict error = %v, want ErrIdentityConflict", err)
	}
}

func TestEnsureLinked_UsesAdapterTransactionForApplicationUser(t *testing.T) {
	db := &trackingDB{DB: dalgo2memory.NewDB()}
	appUsers := &transactionalAppUsers{}
	store := NewStateStore(db, appUsers)
	identity := botsfwstore.Identity{PlatformID: "telegram", BotID: "bot-1", BotUserID: "user-1", ChatID: "chat-1"}
	request := botsfwstore.LinkRequest{
		Identity: identity,
		ReadPlatformUserData: func() botsfwmodels.PlatformUserData {
			return &botsfwmodels.PlatformUserBaseDbo{}
		},
		NewPlatformUserData: func(appUserID string) (botsfwmodels.PlatformUserData, error) {
			return &botsfwmodels.PlatformUserBaseDbo{BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID}}, nil
		},
		NewChatData: func(string, bool) (botsfwmodels.BotChatData, error) {
			return nil, errors.New("force rollback after user and platform writes")
		},
	}

	if _, err := store.EnsureLinked(context.Background(), request); err == nil {
		t.Fatal("EnsureLinked() error = nil, want forced failure after the app-user write")
	}
	if appUsers.tx == nil || appUsers.tx != db.tx {
		t.Fatal("application user was not persisted with the adapter-owned transaction")
	}
}
