package botsfwstoredalgo

import (
	"reflect"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
	"github.com/dal-go/record/update"
)

type updateSummary struct {
	value  any
	delete bool
}

func summarize(t *testing.T, updates []update.Update) map[string]updateSummary {
	t.Helper()
	got := map[string]updateSummary{}
	for _, u := range updates {
		if _, dup := got[u.FieldName()]; dup {
			t.Fatalf("field %q is updated twice", u.FieldName())
		}
		got[u.FieldName()] = updateSummary{value: u.Value(), delete: u.Value() == update.DeleteField}
	}
	return got
}

func TestModelledFieldUpdates_FrameworkChat(t *testing.T) {
	last := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	chat := &botsfwmodels.ChatBaseData{
		BotBaseData:       botsfwmodels.BotBaseData{AppUserID: "app-1", DtCreated: seededAt, DtUpdated: changeAt},
		BotUserIDs:        []string{"u1"},
		Title:             "Team",
		DtLastInteraction: last,
	}
	chat.SetPreferredLanguage("") // exercises promoted fields of the unexported embedded structs

	got := summarize(t, mustUpdates(t, chat))

	want := map[string]updateSummary{
		"botUserIDs":         {value: []string{"u1"}},
		"appUserID":          {value: "app-1"},
		"dtCreated":          {value: seededAt},
		"dtUpdated":          {value: changeAt},
		"isAccessGranted":    {value: false},
		"awaitingReplyTo":    {delete: true},
		"preferredLanguage":  {delete: true},
		"languageCodes":      {delete: true},
		"vars":               {delete: true},
		"isGroup":            {delete: true},
		"type":               {delete: true},
		"title":              {value: "Team"},
		"analyticsClientIDs": {delete: true},
		"dtLastInteraction":  {value: last},
		"interactionsCount":  {delete: true},
		"dtForbidden":        {delete: true},
		"DtForbiddenLast":    {delete: true},
	}
	// update.DeleteField is not comparable with DeepEqual-able zero values, so
	// compare delete markers and values separately.
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("no update for %q", name)
			continue
		}
		if g.delete != w.delete || (!w.delete && !reflect.DeepEqual(g.value, w.value)) {
			t.Errorf("update %q = %#v, want %#v", name, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d updates, want %d: %v", len(got), len(want), got)
	}
}

type nestedProfile struct {
	Inner string `firestore:"inner"`
}

type Label string

type profileChat struct {
	botsfwmodels.ChatBaseData
	*nestedProfile
	*absentProfile
	Label
	Skipped    string `firestore:"-"`
	Renamed    string `firestore:"renamed_as"`
	Untagged   int
	Flagged    []string `firestore:",omitempty"`
	hidden     string
	Title      string `firestore:"title"` // shadows the embedded ChatBaseData.Title
	NotOmitted string `firestore:"not_omitted,omitempty,"`
}

type absentProfile struct {
	Never string `firestore:"never"`
}

func TestModelledFieldUpdates_TagRules(t *testing.T) {
	data := &profileChat{
		nestedProfile: &nestedProfile{Inner: "in"},
		Label:         "lbl",
		Skipped:       "skip",
		Renamed:       "r",
		Untagged:      7,
		hidden:        "h",
		Title:         "outer",
		NotOmitted:    "n",
	}
	data.ChatBaseData.Title = "inner-title"

	got := summarize(t, mustUpdates(t, data))

	for name, want := range map[string]any{
		"inner":           "in",
		"Label":           Label("lbl"),
		"renamed_as":      "r",
		"Untagged":        7,
		"title":           "outer",
		"not_omitted":     "n",
		"appUserID":       "",
		"isAccessGranted": false,
	} {
		g, ok := got[name]
		if !ok || g.delete || !reflect.DeepEqual(g.value, want) {
			t.Errorf("update %q = %#v (present=%v), want value %#v", name, g, ok, want)
		}
	}
	for _, name := range []string{"Flagged", "botUserIDs"} {
		if !got[name].delete {
			t.Errorf("empty omitempty field %q must be deleted, got %#v", name, got[name])
		}
	}
	for _, name := range []string{"Skipped", "hidden", "never", "nestedProfile", "absentProfile"} {
		if _, ok := got[name]; ok {
			t.Errorf("field %q must not be updated", name)
		}
	}
}

type shallowFirst struct {
	Name string `firestore:"name"`
	deep
}

type deep struct {
	Name string `firestore:"name"`
}

type deepFirst struct {
	deep
	Name string `firestore:"name"`
}

func TestModelledFieldUpdates_ShallowestFieldWins(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
	}{
		{"shallow declared first", &shallowFirst{Name: "outer", deep: deep{Name: "inner"}}},
		{"shallow declared last", &deepFirst{Name: "outer", deep: deep{Name: "inner"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summarize(t, mustUpdates(t, tc.data))
			if len(got) != 1 || got["name"].value != "outer" {
				t.Fatalf("updates = %v, want only name=outer", got)
			}
		})
	}
}

func TestModelledFieldUpdates_Errors(t *testing.T) {
	type noFields struct {
		hidden string
		Skip   string `firestore:"-"`
	}
	_ = noFields{}.hidden // the field exists only to prove unexported fields are ignored
	for name, data := range map[string]any{
		"nil":            nil,
		"int":            42,
		"nil pointer":    (*profileChat)(nil),
		"map":            map[string]any{"a": 1},
		"no fields":      noFields{},
		"pointer no fld": &noFields{},
	} {
		t.Run(name, func(t *testing.T) {
			if updates, err := modelledFieldUpdates(data); err == nil {
				t.Fatalf("modelledFieldUpdates(%v) = %v, want an error", data, updates)
			}
		})
	}
}

func mustUpdates(t *testing.T, data any) []update.Update {
	t.Helper()
	updates, err := modelledFieldUpdates(data)
	if err != nil {
		t.Fatal(err)
	}
	return updates
}

func TestTimestampUpdates(t *testing.T) {
	base := &botsfwmodels.BotBaseData{DtCreated: seededAt, DtUpdated: changeAt}
	if got := summarize(t, timestampUpdates(base, false)); len(got) != 1 || got["dtUpdated"].value != changeAt {
		t.Errorf("timestampUpdates(createdWasZero=false) = %v, want only dtUpdated", got)
	}
	got := summarize(t, timestampUpdates(base, true))
	if len(got) != 2 || got["dtUpdated"].value != changeAt || got["dtCreated"].value != seededAt {
		t.Errorf("timestampUpdates(createdWasZero=true) = %v, want dtUpdated and dtCreated", got)
	}
}
