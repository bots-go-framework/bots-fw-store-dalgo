package botsfwstoredalgo

import (
	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/dal-go/record/update"
)

// Document field names owned by the platform-user operations below. They are
// the `firestore` (and `json`) tags of botsfwmodels.BotBaseData and WithBotIDs;
// a test pins them to those tags so a rename in bots-fw-store cannot silently
// drift.
const (
	fieldAppUserID     = "appUserID"
	fieldDtCreated     = "dtCreated"
	fieldDtUpdated     = "dtUpdated"
	fieldAccessGranted = "isAccessGranted"
	fieldBotIDs        = "botIDs"
)

// A platform-user document (botPlatforms/{platform}/botUsers/{user}) is shared
// by every bot of the platform, and different services may model it with
// different Go types. Writing it whole with tx.Set would delete every field the
// caller's type does not model, so an existing platform user is changed with
// field updates that name only the fields the operation owns. Updates only set
// values; none deletes a field. tx.Set / Insert is kept for the first insert.
//
// Chat documents live under their bot (see NewBotChatKey), so they are not
// shared between bots and are still written whole.

// timestampUpdates returns the updates for the timestamps EnsureTimestamps
// changed. dtUpdated is always written; dtCreated only when it was zero before
// EnsureTimestamps ran (a legacy record self-healing), so an existing
// dtCreated is never rewritten.
func timestampUpdates(base *botsfwmodels.BotBaseData, createdWasZero bool) []update.Update {
	updates := []update.Update{update.ByFieldName(fieldDtUpdated, base.DtUpdated)}
	if createdWasZero {
		updates = append(updates, update.ByFieldName(fieldDtCreated, base.DtCreated))
	}
	return updates
}

// appUserLinkUpdates returns the updates that link an existing platform user to
// an application user.
func appUserLinkUpdates(appUserID string, base *botsfwmodels.BotBaseData, createdWasZero bool) []update.Update {
	return append([]update.Update{update.ByFieldName(fieldAppUserID, appUserID)}, timestampUpdates(base, createdWasZero)...)
}
