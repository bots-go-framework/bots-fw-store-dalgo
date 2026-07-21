// Package botsfwstoredalgo is the DALgo implementation of botsfwstore.StateStore.
//
// It deliberately retains the historical botPlatforms/bots/botUsers/botChats
// key layout, so moving to this adapter requires no data migration.
package botsfwstoredalgo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/dal"
	dalrecord "github.com/dal-go/record"
)

const (
	botPlatformsCollection = "botPlatforms"
	botsCollection         = "bots"
	botUsersCollection     = "botUsers"
	botChatsCollection     = "botChats"
)

// DBProvider returns the DALgo database for a request context. It supports
// hosts that select a database from tenant or request metadata.
type DBProvider func(ctx context.Context) (dal.DB, error)

// PlatformUserRecord is the DALgo representation of a platform user. It is
// exported for application-owned identity flows that intentionally operate on
// the historical schema without importing DALgo into bots-fw itself.
type PlatformUserRecord = dalrecord.DataWithID[string, botsfwmodels.PlatformUserData]

// BotUser is a deprecated schema-level alias retained only to make application
// DALgo login tests readable during migration. New code should use
// PlatformUserRecord; public facades should return botsfwstore.PlatformUser.
type BotUser = PlatformUserRecord

// AppUserStore is the application-owned half of DALgo identity persistence.
// PrepareAppUser runs before the adapter transaction and may idempotently
// provision an external identity such as Firebase Auth. It must not persist the
// application user in DALgo. EnsureAppUser then persists that prepared user
// using only the supplied retryable transaction. This lets the adapter commit
// the application user, platform link, and chat atomically without repeating
// external side effects.
type AppUserStore interface {
	PrepareAppUser(ctx context.Context, identity botsfwstore.Identity) (botsfwstore.AppUser, error)
	EnsureAppUser(ctx context.Context, tx dal.ReadwriteTransaction, identity botsfwstore.Identity, prepared botsfwstore.AppUser) (botsfwstore.AppUser, error)
	AppUser(ctx context.Context, tx dal.ReadSession, botID, appUserID string) (botsfwstore.AppUser, error)
}

// StateStore is the default DALgo-backed implementation of the framework store.
type StateStore struct {
	getDB    DBProvider
	appUsers AppUserStore
}

var _ botsfwstore.StateStore = (*StateStore)(nil)

// NewStateStore creates a DALgo state-store adapter.
func NewStateStore(db dal.DB, appUsers AppUserStore) *StateStore {
	if db == nil {
		panic("db is required")
	}
	return NewStateStoreWithProvider(func(context.Context) (dal.DB, error) { return db, nil }, appUsers)
}

// NewStateStoreWithProvider creates a DALgo state-store adapter whose database
// is resolved for each operation.
func NewStateStoreWithProvider(getDB DBProvider, appUsers AppUserStore) *StateStore {
	if getDB == nil {
		panic("getDB is required")
	}
	if appUsers == nil {
		panic("appUsers is required")
	}
	return &StateStore{getDB: getDB, appUsers: appUsers}
}

// NewPlatformKey creates the historical top-level platform key.
func NewPlatformKey[PlatformID ~string](platformID PlatformID) *dalrecord.Key {
	if platformID == "" {
		panic("platform ID is required")
	}
	return dalrecord.NewKeyWithID(botPlatformsCollection, string(platformID))
}

// NewBotKey creates the historical platform bot key.
func NewBotKey[PlatformID ~string](platformID PlatformID, botID string) *dalrecord.Key {
	if botID == "" {
		panic("bot ID is required")
	}
	return dalrecord.NewKeyWithParentAndID(NewPlatformKey(platformID), botsCollection, botID)
}

// NewPlatformUserKey creates the historical platform-user key.
func NewPlatformUserKey[PlatformID ~string](platformID PlatformID, userID string) *dalrecord.Key {
	if userID == "" {
		panic("platform-user ID is required")
	}
	return dalrecord.NewKeyWithParentAndID(NewPlatformKey(platformID), botUsersCollection, userID)
}

// NewBotChatKey creates the historical bot-chat key.
func NewBotChatKey[PlatformID ~string](platformID PlatformID, botID, chatID string) *dalrecord.Key {
	if chatID == "" {
		panic("chat ID is required")
	}
	return dalrecord.NewKeyWithParentAndID(NewBotKey(platformID, botID), botChatsCollection, chatID)
}

// GetPlatformUser loads a platform user from the historical schema.
func GetPlatformUser(ctx context.Context, reader dal.ReadSession, platformID, userID string, data botsfwmodels.PlatformUserData) (PlatformUserRecord, error) {
	result, err := dal.GetRecordWithIDIntoData(ctx, reader, NewPlatformUserKey(platformID, userID), userID, data)
	return PlatformUserRecord(result), err
}

// CreatePlatformUserRecord inserts a platform user in the historical schema.
func CreatePlatformUserRecord(ctx context.Context, tx dal.ReadwriteTransaction, platformID, userID string, data botsfwmodels.PlatformUserData) error {
	if validatable, ok := data.(interface{ Validate() error }); ok {
		if err := validatable.Validate(); err != nil {
			return err
		}
	}
	_, err := dal.InsertRecordWithDataAndID(ctx, tx, NewPlatformUserKey(platformID, userID), userID, data)
	return err
}

func (s *StateStore) db(ctx context.Context) (dal.DB, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("get DALgo database: %w", err)
	}
	if db == nil {
		return nil, errors.New("DALgo database provider returned nil")
	}
	return db, nil
}

func notFound(err error) error {
	if dalrecord.IsNotFound(err) {
		return fmt.Errorf("%w: %v", botsfwstore.ErrNotFound, err)
	}
	return err
}

func (s *StateStore) EnsureLinked(ctx context.Context, request botsfwstore.LinkRequest) (linked botsfwstore.LinkedIdentity, err error) {
	if err = request.Validate(); err != nil {
		return linked, err
	}
	db, err := s.db(ctx)
	if err != nil {
		return linked, err
	}

	identity := request.Identity
	platformData, found, err := readPlatformUser(ctx, db, identity, request.ReadPlatformUserData)
	if err != nil {
		return linked, err
	}

	var prepared botsfwstore.AppUser
	preparedAppUser := !found || platformData.GetAppUserID() == ""
	if preparedAppUser {
		prepared, err = s.appUsers.PrepareAppUser(ctx, identity)
		if err != nil {
			return linked, err
		}
		if err = botsfwstore.RequireAppUserID(prepared.ID); err != nil {
			return linked, err
		}
	}

	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		platformData := request.ReadPlatformUserData()
		if platformData == nil {
			return errors.New("platform-user reader factory returned nil")
		}
		platformUserKey := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
		_, getErr := dal.GetRecordWithIDIntoData(ctx, tx, platformUserKey, identity.BotUserID, platformData)
		platformUserExists := true
		if dalrecord.IsNotFound(getErr) {
			platformUserExists = false
		} else if getErr != nil {
			return getErr
		}

		platformAppUserID := ""
		if platformUserExists {
			platformAppUserID = platformData.GetAppUserID()
		}

		if platformAppUserID == "" {
			if !preparedAppUser {
				return errors.New("application user was not prepared for an unlinked platform identity")
			}
			linked.AppUser, getErr = s.appUsers.EnsureAppUser(ctx, tx, identity, prepared)
		} else {
			if preparedAppUser && platformAppUserID != prepared.ID {
				return fmt.Errorf("%w: %s/%s was prepared as %s but resolves to %s", botsfwstore.ErrIdentityConflict, identity.PlatformID, identity.BotUserID, prepared.ID, platformAppUserID)
			}
			linked.AppUser, getErr = s.appUsers.AppUser(ctx, tx, identity.BotID, platformAppUserID)
		}
		if getErr != nil {
			return getErr
		}
		if getErr = botsfwstore.RequireAppUserID(linked.AppUser.ID); getErr != nil {
			return getErr
		}
		if platformAppUserID != "" && linked.AppUser.ID != platformAppUserID {
			return fmt.Errorf("%w: %s/%s resolves to app user %s but the application store loaded %s", botsfwstore.ErrIdentityConflict, identity.PlatformID, identity.BotUserID, platformAppUserID, linked.AppUser.ID)
		}
		if preparedAppUser && linked.AppUser.ID != prepared.ID {
			return fmt.Errorf("%w: prepared app user %s but persisted %s", botsfwstore.ErrIdentityConflict, prepared.ID, linked.AppUser.ID)
		}

		if !platformUserExists {
			platformData, getErr = request.NewPlatformUserData(linked.AppUser.ID)
			if getErr != nil {
				return getErr
			}
			if platformData == nil {
				return errors.New("platform-user factory returned nil")
			}
			if _, getErr = dal.InsertRecordWithDataAndID(ctx, tx, platformUserKey, identity.BotUserID, platformData); getErr != nil {
				return getErr
			}
		} else {
			if platformAppUserID == "" {
				platformData.SetAppUserID(linked.AppUser.ID)
				platformData.SetUpdatedTime(identityTime(ctx))
				if getErr = tx.Set(ctx, dalrecord.NewRecordWithData(platformUserKey, platformData)); getErr != nil {
					return getErr
				}
			}
		}
		linked.PlatformUser = botsfwstore.PlatformUser{ID: identity.BotUserID, Data: platformData}

		if identity.ChatID == "" {
			return nil
		}
		chatData, getErr := request.NewChatData(linked.AppUser.ID, platformData.IsAccessGranted())
		if getErr != nil {
			return getErr
		}
		if chatData == nil {
			return errors.New("chat-data factory returned nil")
		}
		chatKey := NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
		_, getErr = dal.GetRecordWithIDIntoData(ctx, tx, chatKey, identity.ChatID, chatData)
		if dalrecord.IsNotFound(getErr) {
			if _, getErr = dal.InsertRecordWithDataAndID(ctx, tx, chatKey, identity.ChatID, chatData); getErr != nil {
				return getErr
			}
		} else if getErr != nil {
			return getErr
		} else if chatData.GetAppUserID() == "" {
			chatData.SetAppUserID(linked.AppUser.ID)
			if getErr = tx.Set(ctx, dalrecord.NewRecordWithData(chatKey, chatData)); getErr != nil {
				return getErr
			}
		} else if chatData.GetAppUserID() != linked.AppUser.ID {
			return fmt.Errorf("%w: chat %s resolves to app user %s, platform identity resolves to %s", botsfwstore.ErrIdentityConflict, identity.ChatID, chatData.GetAppUserID(), linked.AppUser.ID)
		}
		linked.ChatData = chatData
		return nil
	})
	return linked, notFound(err)
}

func readPlatformUser(
	ctx context.Context,
	db dal.DB,
	identity botsfwstore.Identity,
	newData func() botsfwmodels.PlatformUserData,
) (data botsfwmodels.PlatformUserData, found bool, err error) {
	err = db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		data = newData()
		if data == nil {
			return errors.New("platform-user reader factory returned nil")
		}
		_, getErr := dal.GetRecordWithIDIntoData(ctx, tx, NewPlatformUserKey(identity.PlatformID, identity.BotUserID), identity.BotUserID, data)
		if dalrecord.IsNotFound(getErr) {
			data = nil
			return nil
		}
		if getErr != nil {
			return getErr
		}
		found = true
		return nil
	})
	return data, found, err
}

// identityTime exists to make the write timestamp explicit at the adapter
// boundary. DALgo transaction retries never encompass router work.
func identityTime(context.Context) time.Time { return time.Now().UTC() }

func (s *StateStore) PlatformUser(ctx context.Context, identity botsfwstore.Identity, newData func() botsfwmodels.PlatformUserData) (result botsfwstore.PlatformUser, err error) {
	if err = identity.Validate(); err != nil {
		return result, err
	}
	if newData == nil {
		return result, errors.New("platform-user reader factory is required")
	}
	db, err := s.db(ctx)
	if err != nil {
		return result, err
	}
	err = db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		data := newData()
		if data == nil {
			return errors.New("platform-user reader factory returned nil")
		}
		_, err := dal.GetRecordWithIDIntoData(ctx, tx, NewPlatformUserKey(identity.PlatformID, identity.BotUserID), identity.BotUserID, data)
		if err == nil {
			result = botsfwstore.PlatformUser{ID: identity.BotUserID, Data: data}
		}
		return notFound(err)
	})
	return result, err
}

func (s *StateStore) AppUser(ctx context.Context, botID, appUserID string) (result botsfwstore.AppUser, err error) {
	if botID == "" || appUserID == "" {
		return result, fmt.Errorf("%w: bot and app-user IDs are required", botsfwstore.ErrNotFound)
	}
	db, err := s.db(ctx)
	if err != nil {
		return result, err
	}
	err = db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		var getErr error
		result, getErr = s.appUsers.AppUser(ctx, tx, botID, appUserID)
		return getErr
	})
	err = notFound(err)
	return result, err
}

func (s *StateStore) SaveChat(ctx context.Context, identity botsfwstore.Identity, data botsfwmodels.BotChatData) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if identity.ChatID == "" || data == nil {
		return errors.New("chat ID and data are required")
	}
	db, err := s.db(ctx)
	if err != nil {
		return err
	}
	return db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, dalrecord.NewRecordWithData(NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID), data))
	})
}

func (s *StateStore) SetPlatformUserAccessGranted(ctx context.Context, identity botsfwstore.Identity, newData func() botsfwmodels.PlatformUserData, value bool) (result botsfwstore.PlatformUser, err error) {
	if err = identity.Validate(); err != nil {
		return result, err
	}
	if newData == nil {
		return result, errors.New("platform-user reader factory is required")
	}
	db, err := s.db(ctx)
	if err != nil {
		return result, err
	}
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		data := newData()
		if data == nil {
			return errors.New("platform-user reader factory returned nil")
		}
		key := NewPlatformUserKey(identity.PlatformID, identity.BotUserID)
		if _, err := dal.GetRecordWithIDIntoData(ctx, tx, key, identity.BotUserID, data); err != nil {
			return notFound(err)
		}
		data.SetAccessGranted(value)
		data.SetUpdatedTime(identityTime(ctx))
		if err := tx.Set(ctx, dalrecord.NewRecordWithData(key, data)); err != nil {
			return err
		}
		result = botsfwstore.PlatformUser{ID: identity.BotUserID, Data: data}
		return nil
	})
	return result, err
}
