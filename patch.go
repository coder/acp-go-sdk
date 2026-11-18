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
// In ACP v1 a null in a ToolCallUpdate does not clear: the protocol docs say
// that it leaves name, rawInput, and rawOutput unchanged, and the Rust SDK
// reads null in every ToolCallUpdate field as absent. Treat IsNull there like
// IsUnset. A set Patch whose value encodes as JSON null, such as a nil
// json.RawMessage or a nil pointer, is null on the wire and reads back as
// IsNull.
//
// Make one with PatchSet, PatchNull, or PatchFromPtr, and read it with Value,
// Ptr, IsNull, and IsUnset. The structs of this package leave the member out
// when the field is unset. In a struct of your own, tag the field omitzero
// (Go 1.24 or later) to do the same: omitempty never leaves out a struct, so
// it writes an unset Patch as null.
type Patch[T any] struct {
	value T
	state patchState
	// err is why the value of a set Patch cannot be encoded, which
	// MarshalJSON returns (see WithUpdateRawInput).
	err error
}

type patchState uint8

const (
	patchUnset patchState = iota
	patchNull
	patchSet
)

// PatchSet returns the Patch that replaces the stored value with v. A nil
// slice or map is written as an empty one, so that it does not read as null,
// unless its type has a MarshalJSON method: a nil json.RawMessage is null. A
// nil pointer is null too. A []byte is written as encoding/json writes it,
// except that a nil one is []: an empty one is "".
func PatchSet[T any](v T) Patch[T] {
	return Patch[T]{value: v, state: patchSet}
}

// PatchFromPtr returns the Patch that replaces the stored value with *p, or
// an unset Patch, which leaves the stored value as it is, for a nil p.
func PatchFromPtr[T any](p *T) Patch[T] {
	if p == nil {
		return Patch[T]{}
	}
	return PatchSet(*p)
}

// PatchNull returns the Patch that clears the stored value.
func PatchNull[T any]() Patch[T] {
	return Patch[T]{state: patchNull}
}

// Value returns the value of a set Patch and true, or the zero T and false
// when p is unset or null. A Patch from WithUpdateRawInput or
// WithUpdateRawOutput whose value did not encode reads as set with a nil
// value; only MarshalJSON returns its error.
func (p Patch[T]) Value() (T, bool) {
	return p.value, p.state == patchSet
}

// Ptr returns a pointer to a copy of the value of a set Patch, and nil when p
// is unset or null.
func (p Patch[T]) Ptr() *T {
	if p.state != patchSet {
		return nil
	}
	return &p.value
}

// IsNull reports whether p clears the stored value.
func (p Patch[T]) IsNull() bool { return p.state == patchNull }

// IsUnset reports whether p leaves the stored value as it is.
func (p Patch[T]) IsUnset() bool { return p.state == patchUnset }

// MarshalJSON writes the value of a set Patch, and null otherwise. Only the
// struct that holds an unset Patch can leave the member out (see Patch).
func (p Patch[T]) MarshalJSON() ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.state != patchSet {
		return []byte("null"), nil
	}
	if empty := emptyJSON(reflect.ValueOf(&p.value).Elem()); empty != nil {
		return empty, nil
	}
	return json.Marshal(p.value)
}

var marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// emptyJSON returns [] for a nil slice and {} for a nil map, also one that an
// interface holds, and nil for any other value or for a type that writes
// itself.
func emptyJSON(v reflect.Value) []byte {
	for v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	if (v.Kind() != reflect.Slice && v.Kind() != reflect.Map) || !v.IsNil() || v.Type().Implements(marshalerType) {
		return nil
	}
	if v.Kind() == reflect.Slice {
		return []byte("[]")
	}
	return []byte("{}")
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
