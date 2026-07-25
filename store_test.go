package botsfwstoredalgo

import (
	"context"
	"errors"
	"testing"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

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
			return &botsfwmodels.ChatBaseData{BotBaseData: botsfwmodels.BotBaseData{AppUserID: appUserID}}, nil
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
