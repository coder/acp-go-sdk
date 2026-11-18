package acp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrMetaKeyNotFound is the error that Meta.Get wraps when the Meta has no
// value under the key. Test for it with errors.Is.
var ErrMetaKeyNotFound = errors.New("acp: no such _meta key")

// Meta is the _meta member of an ACP type: data that a client or an agent
// attaches for its own use. ACP gives its keys no meaning. Each value holds
// the JSON as it was read, so a number keeps every digit.
//
// Read a value with Get and write one with Set, or use the map directly. An
// empty Meta is left out of the encoded message.
type Meta map[string]json.RawMessage

// NewMeta returns a Meta with each value of values encoded as JSON. It returns
// nil for no values.
func NewMeta(values map[string]any) (Meta, error) {
	var m Meta
	for key, value := range values {
		if err := m.Set(key, value); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Get decodes the value of key into v, as json.Unmarshal does. It returns an
// error that wraps ErrMetaKeyNotFound when m has no key, and the decode error
// when the value does not decode into v.
func (m Meta) Get(key string, v any) error {
	raw, ok := m[key]
	if !ok {
		return fmt.Errorf("%w: %q", ErrMetaKeyNotFound, key)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("acp: _meta key %q: %w", key, err)
	}
	return nil
}

// Set encodes v as JSON and stores it under key. It makes the map when m is
// nil. A json.RawMessage is stored as its JSON, without the spaces between
// tokens.
func (m *Meta) Set(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("acp: _meta key %q: %w", key, err)
	}
	if *m == nil {
		*m = Meta{}
	}
	(*m)[key] = raw
	return nil
}
