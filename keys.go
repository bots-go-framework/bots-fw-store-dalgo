package botsfwstoredalgo

import "github.com/dal-go/record"

// Keys fixes a deployment's platform parent and collection for one adapter.
// Constructors and returned keys copy ancestry so callers cannot mutate it.
type Keys struct {
	parent    *record.Key
	platforms string
}

func NewKeys(parent *record.Key, platforms string) Keys {
	if platforms == "" {
		panic("platform collection is required")
	}
	for key := parent; key != nil; key = key.Parent() {
		if key.ID == nil {
			panic("complete parent key is required")
		}
		if err := key.Validate(); err != nil {
			panic(err)
		}
	}
	return Keys{parent: cloneKey(parent), platforms: platforms}
}
func DefaultKeys() Keys { return NewKeys(nil, botPlatformsCollection) }
func cloneKey(key *record.Key) *record.Key {
	if key == nil {
		return nil
	}
	return record.NewKeyWithParentAndID(cloneKey(key.Parent()), key.Collection(), key.ID)
}

// WithKeys selects exactly one layout; no historical reads or dual writes occur.
func WithKeys(keys Keys) StateStoreOption {
	return func(s *StateStore) {
		if keys.platforms == "" {
			panic("keys are required")
		}
		s.keys = NewKeys(keys.parent, keys.platforms)
	}
}
