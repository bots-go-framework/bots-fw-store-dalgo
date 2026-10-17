package botsfwstoredalgo

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/dal-go/record/update"
)

// Document field names owned by the store operations below. They are the
// `firestore` tags of botsfwmodels.BotBaseData and WithBotIDs; a test pins
// them to those tags so a rename in bots-fw-store cannot silently drift.
const (
	fieldAppUserID     = "appUserID"
	fieldDtCreated     = "dtCreated"
	fieldDtUpdated     = "dtUpdated"
	fieldAccessGranted = "isAccessGranted"
	fieldBotIDs        = "botIDs"
)

// Writing a whole document with tx.Set replaces it, which deletes every field
// the caller's Go type does not model - for example fields written by another
// service whose profile uses a different type for the same collection. Existing
// documents are therefore changed with field updates that name only the fields
// the operation owns. tx.Set is kept for the first insert only.

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

// appUserLinkUpdates returns the updates that link an existing record to an
// application user.
func appUserLinkUpdates(appUserID string, base *botsfwmodels.BotBaseData, createdWasZero bool) []update.Update {
	return append([]update.Update{update.ByFieldName(fieldAppUserID, appUserID)}, timestampUpdates(base, createdWasZero)...)
}

// modelledFieldUpdates converts a chat-data value into one top-level field
// update per field its Go type models, using the same field naming rules as
// the Firestore struct mapping: the `firestore` tag name (default: the Go field
// name), "-" to skip, "omitempty" to omit an empty value, and untagged
// embedded structs flattened into their parent. A field that is omitted for
// being empty is deleted, so clearing a modelled field still clears it in the
// stored document. Fields the type does not model are not mentioned and
// therefore survive.
func modelledFieldUpdates(data any) ([]update.Update, error) {
	v := reflect.ValueOf(data)
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("chat data must be a struct or a pointer to one, got %T", data)
	}
	var fields []modelledField
	fields = collectModelledFields(fields, v, 0)
	if len(fields) == 0 {
		return nil, errors.New("chat data models no persistable fields")
	}
	updates := make([]update.Update, len(fields))
	for i, f := range fields {
		if f.omit {
			updates[i] = update.DeleteByFieldName(f.name)
		} else {
			updates[i] = update.ByFieldName(f.name, f.value)
		}
	}
	return updates, nil
}

type modelledField struct {
	name  string
	value any
	omit  bool
	depth int
}

func collectModelledFields(fields []modelledField, v reflect.Value, depth int) []modelledField {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := sf.Tag.Get("firestore")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fv := v.Field(i)
		if sf.Anonymous && name == "" {
			ev := fv
			if ev.Kind() == reflect.Pointer {
				if ev.IsNil() {
					continue
				}
				ev = ev.Elem()
			}
			if ev.Kind() == reflect.Struct {
				fields = collectModelledFields(fields, ev, depth+1)
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		field := modelledField{name: name, depth: depth}
		if strings.Contains(","+opts+",", ",omitempty,") && isEmptyValue(fv) {
			field.omit = true
		} else {
			field.value = fv.Interface()
		}
		fields = addModelledField(fields, field)
	}
	return fields
}

// addModelledField adds a field unless a field of the same name at a shallower
// depth already shadows it, matching Go's promotion rules for embedded structs.
func addModelledField(fields []modelledField, field modelledField) []modelledField {
	for i, existing := range fields {
		if existing.name != field.name {
			continue
		}
		if field.depth < existing.depth {
			fields[i] = field
		}
		return fields
	}
	return append(fields, field)
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}
