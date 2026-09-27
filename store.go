// Package botsfwstoredalgo is the DALgo implementation of botsfwstore.StateStore.
//
// It deliberately retains the historical botPlatforms/bots/botUsers/botChats
// key layout, so moving to this adapter requires no data migration.
package botsfwstoredalgo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/bots-go-framework/bots-fw-store/botsfwstore"
	"github.com/dal-go/dalgo/dal"
	dalrecord "github.com/dal-go/record"
)

const (
	botPlatformsCollection   = "botPlatforms"
	botsCollection           = "bots"
	botUsersCollection       = "botUsers"
	botChatsCollection       = "botChats"
	webhookUpdatesCollection = "webhookUpdates"
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

type webhookUpdateData struct {
	Status      string    `firestore:"status"`
	LeaseID     string    `firestore:"leaseID,omitempty"`
	LeaseUntil  time.Time `firestore:"leaseUntil,omitempty"`
	Attempts    int       `firestore:"attempts"`
	Completed   time.Time `firestore:"completed,omitempty"`
	Failed      time.Time `firestore:"failed,omitempty"`
	FailureCode string    `firestore:"failureCode,omitempty"`
}

// StateStore is the default DALgo-backed implementation of the framework store.
type StateStore struct {
	getDB    DBProvider
	appUsers AppUserStore
	now      func() time.Time
}

var _ botsfwstore.StateStore = (*StateStore)(nil)

// StateStoreOption configures optional StateStore behaviour.
type StateStoreOption func(*StateStore)

// WithClock overrides the clock StateStore uses to stamp DtCreated/DtUpdated
// on platform-user and bot-chat records (via botsfwmodels.BotBaseData.
// EnsureTimestamps). It defaults to time.Now().UTC(). Tests should supply a
// fixed or stepped clock instead of relying on wall-clock time, so assertions
// about DtCreated/DtUpdated are deterministic.
func WithClock(now func() time.Time) StateStoreOption {
	return func(s *StateStore) {
		if now != nil {
			s.now = now
		}
	}
}

// NewStateStore creates a DALgo state-store adapter.
func NewStateStore(db dal.DB, appUsers AppUserStore, opts ...StateStoreOption) *StateStore {
	if db == nil {
		panic("db is required")
	}
	return NewStateStoreWithProvider(func(context.Context) (dal.DB, error) { return db, nil }, appUsers, opts...)
}

// NewStateStoreWithProvider creates a DALgo state-store adapter whose database
// is resolved for each operation.
func NewStateStoreWithProvider(getDB DBProvider, appUsers AppUserStore, opts ...StateStoreOption) *StateStore {
	if getDB == nil {
		panic("getDB is required")
	}
	if appUsers == nil {
		panic("appUsers is required")
	}
	s := &StateStore{
		getDB:    getDB,
		appUsers: appUsers,
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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

// NewWebhookUpdateKey stores the inbox under its bot to isolate equal provider
// update IDs from different Telegram bots.
func NewWebhookUpdateKey(key botsfwstore.WebhookUpdateKey) *dalrecord.Key {
	if err := key.Validate(); err != nil {
		panic(err)
	}
	// Provider update IDs are untrusted external input. A document key must not
	// retain a payload-derived value or depend on provider path/length rules.
	// SHA-256 makes a bounded opaque, deterministic key while preserving exact
	// deduplication semantics.
	digest := sha256.Sum256([]byte(key.UpdateID))
	return dalrecord.NewKeyWithParentAndID(NewBotKey(key.PlatformID, key.BotID), webhookUpdatesCollection, hex.EncodeToString(digest[:]))
}

func newLeaseID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *StateStore) ClaimWebhookUpdate(ctx context.Context, key botsfwstore.WebhookUpdateKey, leaseUntil time.Time) (claim botsfwstore.WebhookUpdateClaim, err error) {
	if err = key.Validate(); err != nil {
		return claim, err
	}
	if leaseUntil.IsZero() || !leaseUntil.After(s.now()) {
		return claim, errors.New("webhook update lease must end in the future")
	}
	db, err := s.db(ctx)
	if err != nil {
		return claim, err
	}
	err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		data := &webhookUpdateData{}
		_, getErr := dal.GetRecordWithIDIntoData(ctx, tx, NewWebhookUpdateKey(key), key.UpdateID, data)
		if getErr != nil && !dalrecord.IsNotFound(getErr) {
			return getErr
		}
		if getErr == nil && data.Status == string(botsfwstore.WebhookUpdateClaimCompleted) {
			claim = botsfwstore.WebhookUpdateClaim{Status: botsfwstore.WebhookUpdateClaimCompleted, Attempts: data.Attempts}
			return nil
		}
		now := s.now()
		if getErr == nil && data.Status == "processing" && data.LeaseUntil.After(now) {
			claim = botsfwstore.WebhookUpdateClaim{Status: botsfwstore.WebhookUpdateClaimLeased, Attempts: data.Attempts}
			return nil
		}
		leaseID := newLeaseID()
		data.Status, data.LeaseID, data.LeaseUntil, data.Attempts, data.FailureCode = "processing", leaseID, leaseUntil, data.Attempts+1, ""
		if e := tx.Set(ctx, dalrecord.NewRecordWithData(NewWebhookUpdateKey(key), data)); e != nil {
			return e
		}
		claim = botsfwstore.WebhookUpdateClaim{Status: botsfwstore.WebhookUpdateClaimAcquired, LeaseID: leaseID, Attempts: data.Attempts}
		return nil
	})
	return claim, err
}

func (s *StateStore) CompleteWebhookUpdate(ctx context.Context, key botsfwstore.WebhookUpdateKey, leaseID string) error {
	return s.finishWebhookUpdate(ctx, key, leaseID, true, botsfwstore.WebhookUpdateFailureUnknown)
}
func (s *StateStore) FailWebhookUpdate(ctx context.Context, key botsfwstore.WebhookUpdateKey, leaseID string, failureCode botsfwstore.WebhookUpdateFailureCode) error {
	return s.finishWebhookUpdate(ctx, key, leaseID, false, failureCode)
}
func (s *StateStore) finishWebhookUpdate(ctx context.Context, key botsfwstore.WebhookUpdateKey, leaseID string, completed bool, failureCode botsfwstore.WebhookUpdateFailureCode) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if leaseID == "" {
		return errors.New("webhook update lease ID is required")
	}
	db, err := s.db(ctx)
	if err != nil {
		return err
	}
	return db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		data := &webhookUpdateData{}
		k := NewWebhookUpdateKey(key)
		if _, e := dal.GetRecordWithIDIntoData(ctx, tx, k, key.UpdateID, data); e != nil {
			return e
		}
		if data.LeaseID != leaseID {
			return errors.New("webhook update lease is no longer owned")
		}
		data.LeaseID, data.LeaseUntil = "", time.Time{}
		if completed {
			data.Status, data.Completed, data.FailureCode = string(botsfwstore.WebhookUpdateClaimCompleted), s.now(), ""
		} else {
			data.Status, data.Failed = "failed", s.now()
			data.FailureCode = string(botsfwstore.NormalizeWebhookUpdateFailureCode(failureCode))
		}
		return tx.Set(ctx, dalrecord.NewRecordWithData(k, data))
	})
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
		// dalgo v0.68 rejects any read that follows a write inside a
		// read-write transaction (matching real Firestore) and makes the
		// transaction atomic, so this closure is split into a read phase
		// that issues every dal/tx read up front, and a write phase below
		// that only issues dal/tx writes. Values a write phase needs that
		// would otherwise require a read after another write (the resolved
		// app-user ID, in particular) are instead derived from data already
		// available before this transaction started, or resolved here in
		// the read phase and merely applied afterwards.

		// ---- Read phase ----

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

		// resolvedAppUser is the app user this identity links to. When the
		// platform identity is not yet linked, it is `prepared`, minted by
		// PrepareAppUser before this transaction started - EnsureAppUser
		// still has to persist it, but that persisting write is deferred to
		// the write phase below; it does not need to run here to know the
		// ID. When the platform identity already resolves to an app user,
		// AppUser is a read (it takes a dal.ReadSession, not a
		// dal.ReadwriteTransaction) and can safely run in this phase.
		var resolvedAppUser botsfwstore.AppUser
		if platformAppUserID == "" {
			if !preparedAppUser {
				return errors.New("application user was not prepared for an unlinked platform identity")
			}
			resolvedAppUser = prepared
		} else {
			if preparedAppUser && platformAppUserID != prepared.ID {
				return fmt.Errorf("%w: %s/%s was prepared as %s but resolves to %s", botsfwstore.ErrIdentityConflict, identity.PlatformID, identity.BotUserID, prepared.ID, platformAppUserID)
			}
			if resolvedAppUser, getErr = s.appUsers.AppUser(ctx, tx, identity.BotID, platformAppUserID); getErr != nil {
				return getErr
			}
		}
		if getErr = botsfwstore.RequireAppUserID(resolvedAppUser.ID); getErr != nil {
			return getErr
		}
		if platformAppUserID != "" && resolvedAppUser.ID != platformAppUserID {
			return fmt.Errorf("%w: %s/%s resolves to app user %s but the application store loaded %s", botsfwstore.ErrIdentityConflict, identity.PlatformID, identity.BotUserID, platformAppUserID, resolvedAppUser.ID)
		}

		// Compute the final platform-user payload now (pure in-memory work:
		// factories and field setters, no dal/tx calls) so its
		// IsAccessGranted value is available for the chat-data factory
		// below exactly as before; only the actual write is deferred.
		isNewPlatformUser := !platformUserExists
		if isNewPlatformUser {
			platformData, getErr = request.NewPlatformUserData(resolvedAppUser.ID)
			if getErr != nil {
				return getErr
			}
			if platformData == nil {
				return errors.New("platform-user factory returned nil")
			}
			// The factory only populates platform-specific fields (see
			// bots-fw-telegram's SetBotUserFields); without this, DtCreated/
			// DtUpdated stay zero and Validate rejects the very first write
			// for a brand-new platform user.
			platformData.BaseData().EnsureTimestamps(s.now())
			// Same reasoning for BotIDs: WithRequiredBotIDs.Validate requires
			// a non-empty BotIDs, but no platform adapter's field setter
			// populates it either (SetBotUserFields receives a botID
			// parameter and never uses it). EnsureBotID is the seam that
			// finally sets it, using the identity already in scope here.
			platformData.BaseData().EnsureBotID(identity.BotID)
		} else if platformAppUserID == "" {
			platformData.SetAppUserID(resolvedAppUser.ID)
			// EnsureTimestamps (not SetUpdatedTime) so a legacy record
			// that somehow lacks DtCreated self-heals instead of staying
			// permanently invalid.
			platformData.BaseData().EnsureTimestamps(s.now())
			// Same self-heal reasoning for BotIDs: a platform-user record
			// that predates this field, or was seeded by an application
			// flow that never set it, gains the current bot id here
			// instead of staying permanently invalid. Never clobbers a
			// different bot's id already recorded.
			platformData.BaseData().EnsureBotID(identity.BotID)
		}

		var chatData botsfwmodels.BotChatData
		var chatKey *dalrecord.Key
		chatExists := false
		if identity.ChatID != "" {
			chatData, getErr = request.NewChatData(resolvedAppUser.ID, platformData.IsAccessGranted())
			if getErr != nil {
				return getErr
			}
			if chatData == nil {
				return errors.New("chat-data factory returned nil")
			}
			chatKey = NewBotChatKey(identity.PlatformID, identity.BotID, identity.ChatID)
			_, getErr = dal.GetRecordWithIDIntoData(ctx, tx, chatKey, identity.ChatID, chatData)
			if dalrecord.IsNotFound(getErr) {
				chatExists = false
			} else if getErr != nil {
				return getErr
			} else {
				chatExists = true
			}
		}

		// ---- Write phase ----

		if platformAppUserID == "" {
			if linked.AppUser, getErr = s.appUsers.EnsureAppUser(ctx, tx, identity, prepared); getErr != nil {
				return getErr
			}
			if getErr = botsfwstore.RequireAppUserID(linked.AppUser.ID); getErr != nil {
				return getErr
			}
			if linked.AppUser.ID != prepared.ID {
				return fmt.Errorf("%w: prepared app user %s but persisted %s", botsfwstore.ErrIdentityConflict, prepared.ID, linked.AppUser.ID)
			}
		} else {
			linked.AppUser = resolvedAppUser
		}

		if isNewPlatformUser {
			if _, getErr = dal.InsertRecordWithDataAndID(ctx, tx, platformUserKey, identity.BotUserID, platformData); getErr != nil {
				return getErr
			}
		} else if platformAppUserID == "" {
			if getErr = tx.Set(ctx, dalrecord.NewRecordWithData(platformUserKey, platformData)); getErr != nil {
				return getErr
			}
		}
		linked.PlatformUser = botsfwstore.PlatformUser{ID: identity.BotUserID, Data: platformData}

		if identity.ChatID == "" {
			return nil
		}
		if !chatExists {
			// Same reasoning as the new-platform-user branch above: the
			// factory (SetBotChatFields) only sets ids and access, never
			// timestamps.
			chatData.Base().EnsureTimestamps(s.now())
			if _, getErr = dal.InsertRecordWithDataAndID(ctx, tx, chatKey, identity.ChatID, chatData); getErr != nil {
				return getErr
			}
		} else if chatData.GetAppUserID() == "" {
			chatData.SetAppUserID(linked.AppUser.ID)
			chatData.Base().EnsureTimestamps(s.now())
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
		// Every other write site in this file (new/relinked platform user,
		// new/relinked chat inside EnsureLinked, SetPlatformUserAccessGranted)
		// stamps timestamps before persisting; this one did not, so a caller
		// that passes a BotChatData whose DtCreated/DtUpdated are (still, or
		// again) zero - a legacy record, or simply one it built itself - would
		// write a record BotChatData.Validate would reject, with no self-heal
		// seam to fix it. EnsureTimestamps closes that gap the same way.
		data.Base().EnsureTimestamps(s.now())
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
		data.BaseData().EnsureTimestamps(s.now())
		// Same self-heal seam as EnsureLinked: this loads and rewrites an
		// existing platform-user record too, so a legacy record missing
		// BotIDs gets the same chance to repair itself here.
		data.BaseData().EnsureBotID(identity.BotID)
		if err := tx.Set(ctx, dalrecord.NewRecordWithData(key, data)); err != nil {
			return err
		}
		result = botsfwstore.PlatformUser{ID: identity.BotUserID, Data: data}
		return nil
	})
	return result, err
}
