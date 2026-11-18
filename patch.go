package acp

import (
	"encoding/json"
	"reflect"
)

// Patch is a field of an update that changes a value the receiver stores. It
// has three states, one per form of the member on the wire:
//
//   - unset (the zero value): the member is absent, and the stored value stays
//     as it is;
//   - null: the member is null, which clears the stored value, unless the
//     field's documentation says otherwise;
//   - set: the member holds a value, which replaces the stored one.
//
// Make one with PatchSet or PatchNull, and read it with Value, IsNull, and
// IsUnset. A struct that has a Patch field leaves the member out when the
// field is unset.
type Patch[T any] struct {
	value T
	state patchState
}

type patchState uint8

const (
	patchUnset patchState = iota
	patchNull
	patchSet
)

// PatchSet returns the Patch that replaces the stored value with v. A nil
// slice or map is written as an empty one, so that it does not read as null.
func PatchSet[T any](v T) Patch[T] {
	return Patch[T]{value: v, state: patchSet}
}

// PatchNull returns the Patch that clears the stored value.
func PatchNull[T any]() Patch[T] {
	return Patch[T]{state: patchNull}
}

// Value returns the value of a set Patch and true, or the zero T and false
// when p is unset or null.
func (p Patch[T]) Value() (T, bool) {
	return p.value, p.state == patchSet
}

// IsNull reports whether p clears the stored value.
func (p Patch[T]) IsNull() bool { return p.state == patchNull }

// IsUnset reports whether p leaves the stored value as it is.
func (p Patch[T]) IsUnset() bool { return p.state == patchUnset }

// MarshalJSON writes the value of a set Patch, and null otherwise. The struct
// that holds an unset Patch leaves the member out.
func (p Patch[T]) MarshalJSON() ([]byte, error) {
	if p.state != patchSet {
		return []byte("null"), nil
	}
	switch v := reflect.ValueOf(&p.value).Elem(); {
	case v.Kind() == reflect.Slice && v.IsNil():
		return []byte("[]"), nil
	case v.Kind() == reflect.Map && v.IsNil():
		return []byte("{}"), nil
	}
	return json.Marshal(p.value)
}

// UnmarshalJSON reads null as a Patch that clears the stored value, and any
// other JSON as a value of T. encoding/json calls it only for a member that is
// there, so an absent member leaves the Patch unset.
func (p *Patch[T]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*p = PatchNull[T]()
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = PatchSet(v)
	return nil
}

// orNil returns p for the member of a set or null Patch, and nil for an
// unset one, which omitempty leaves out.
func (p Patch[T]) orNil() *Patch[T] {
	if p.state == patchUnset {
		return nil
	}
	return &p
}
