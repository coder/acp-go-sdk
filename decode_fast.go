package acp

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The generated types decode JSON in two ways. UnmarshalJSON, the exact
// decoder, decodes with encoding/json and then checks what the schema
// requires. decodeFast, the fast decoder, reads the bytes once with the
// scanner of jsonscan.go and decodes each value straight into its field,
// with the helpers in this file. UnmarshalJSON runs the fast decoder first
// (tryFast) and the exact one when it declines.
//
// Every fast decoder keeps one contract, the whole correctness argument:
//
//	v.decodeFast(b) takes b, a JSON value that encoding/json accepts with no
//	whitespace around it, and v, which holds the zero value of its type. When
//	it returns true, the exact decoder, run on a zero value and b, returns
//	nil and leaves the identical value. When it returns false it has written
//	only to v and to memory that it allocated, and the caller sets v back to
//	zero.
//
// So a fast decoder never makes an error: when in doubt it returns false,
// and the exact decoder runs and makes today's error. It declines a key that
// repeats (the exact decoder rejects a property that repeats, and keeps the
// last value of another member), a key with an escape, a key that names a
// field only in another case, and a value that does not decode.

// fastDecode turns the fast decoders on. Tests turn it off to compare the
// fast decoders with the exact ones.
var fastDecode = true

// fastMissed, when a test sets it, is called with each type whose fast
// decoder declined a value, which the exact decoder then reads.
var fastMissed func(reflect.Type)

// fastDecoder is a generated type with a fast decoder.
type fastDecoder[T any] interface {
	*T
	decodeFast(b []byte) bool
}

// tryFast decodes b into v with its fast decoder, when the fast decoders are
// on, v holds its zero value, and b is valid JSON without whitespace around
// it, as encoding/json passes it to UnmarshalJSON. It reports whether it did;
// when it did not, v holds what it held.
func tryFast[T any, P fastDecoder[T]](v P, b []byte) bool {
	if !fastDecode || len(b) == 0 || isSpace(b[0]) || isSpace(b[len(b)-1]) ||
		!reflect.ValueOf(v).Elem().IsZero() || !validJSON(b) {
		return false
	}
	if v.decodeFast(b) {
		return true
	}
	*v = *new(T)
	noteMissed(v)
	return false
}

// fastOrExact decodes b into *dst, a zero value, as json.Unmarshal does, and
// reports whether it decoded: with dec when dec does, else with
// json.Unmarshal, which also decides a value that dec declined. Its answer is
// exact either way, so a union can try its next variant on false, as its
// exact decoder does. On false, *dst is zero.
func fastOrExact[T any](dst *T, b []byte, dec func(*T, []byte) bool) bool {
	if dec(dst, b) {
		return true
	}
	*dst = *new(T)
	noteMissed(dst)
	if json.Unmarshal(b, dst) == nil {
		return true
	}
	*dst = *new(T)
	return false
}

// fastExact decodes b into a value of a type that has no fast decoder, with
// json.Unmarshal: exact on a zero *dst, though not fast.
func fastExact[T any](dst *T, b []byte) bool {
	return json.Unmarshal(b, dst) == nil
}

func noteMissed[T any](v *T) {
	if fastMissed != nil {
		fastMissed(reflect.TypeOf(v).Elem())
	}
}

// The leaf decoders read a JSON value into a field as encoding/json does:
// null leaves a string, number, or boolean as it is, and sets a pointer,
// slice, or map to nil.

func fastString[S ~string](dst *S, b []byte) bool {
	switch b[0] {
	case 'n':
		return true
	case '"':
		s, ok := unquote(b[1 : len(b)-1])
		if ok {
			*dst = S(s)
		}
		return ok
	}
	return false
}

// fastInt reads an integer as strconv.ParseInt(text, 10, 64) does, and fails
// where the value does not fit in I.
func fastInt[I ~int | ~int64](dst *I, b []byte) bool {
	if b[0] == 'n' {
		return true
	}
	n, ok := parseInt(b)
	if !ok || int64(I(n)) != n {
		return false
	}
	*dst = I(n)
	return true
}

func fastFloat[F ~float64](dst *F, b []byte) bool {
	switch b[0] {
	case 'n':
		return true
	case '"', '{', '[', 't', 'f':
		return false
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return false
	}
	*dst = F(f)
	return true
}

func fastBool[B ~bool](dst *B, b []byte) bool {
	switch b[0] {
	case 'n':
	case 't':
		*dst = true
	case 'f':
		*dst = false
	default:
		return false
	}
	return true
}

// fastRaw copies b, as json.RawMessage.UnmarshalJSON does, null included.
func fastRaw(dst *json.RawMessage, b []byte) bool {
	*dst = append(json.RawMessage(nil), b...)
	return true
}

// The container decoders take the decoder of their element.

func fastPtr[T any](dst **T, b []byte, dec func(*T, []byte) bool) bool {
	if b[0] == 'n' {
		*dst = nil
		return true
	}
	p := new(T)
	if !dec(p, b) {
		return false
	}
	*dst = p
	return true
}

// fastSlice reads an array into a fresh slice; [] is an empty slice that is
// not nil.
func fastSlice[S ~[]E, E any](dst *S, b []byte, dec func(*E, []byte) bool) bool {
	switch b[0] {
	case 'n':
		*dst = nil
		return true
	case '[':
	default:
		return false
	}
	var buf [64][]byte
	items, ok := appendItems(buf[:0], b)
	s := make(S, len(items))
	for i, item := range items {
		if !dec(&s[i], item) {
			return false
		}
	}
	*dst = s
	return ok
}

// appendItems appends the items of the array b to items, and reports whether
// b ends as an array does. fastSlice and fastItems collect the items first, in
// a buffer on the stack, so that they allocate their slice once, at its length.
func appendItems(items [][]byte, b []byte) ([][]byte, bool) {
	it, _ := arrayItems(b)
	for it.next() {
		items = append(items, it.item)
	}
	return items, !it.broken
}

// fastMap reads an object into a fresh map. A key that repeats declines.
func fastMap[M ~map[string]E, E any](dst *M, b []byte, dec func(*E, []byte) bool) bool {
	switch b[0] {
	case 'n':
		*dst = nil
		return true
	case '{':
	default:
		return false
	}
	m := make(M)
	// Each value decodes into value[0], which one allocation holds.
	value := make([]E, 1)
	it, _ := objectMembers(b)
	for it.next() {
		key, ok := unquote(it.key)
		if _, repeats := m[key]; !ok || repeats {
			return false
		}
		value[0] = *new(E)
		if !dec(&value[0], it.value) {
			return false
		}
		m[key] = value[0]
	}
	*dst = m
	return !it.broken
}

// fastPatch reads a Patch as Patch.UnmarshalJSON does.
func fastPatch[T any](dst *Patch[T], b []byte, dec func(*T, []byte) bool) bool {
	if b[0] == 'n' {
		*dst = PatchNull[T]()
		return true
	}
	var v T
	if !dec(&v, b) {
		return false
	}
	*dst = PatchSet(v)
	return true
}

// fastEmpty reads a struct without fields: an object, whose members it
// skips, or null.
func fastEmpty(b []byte) bool {
	var w fastObject
	if !w.start(b) {
		return false
	}
	for w.next() {
		if !w.skip() {
			return false
		}
	}
	return w.done()
}

func (m *Meta) decodeFast(b []byte) bool {
	return fastMap(m, b, fastRaw)
}

// The custom-decoded fields of a generated UnmarshalJSON go through the
// helpers that follow its decodeStmt. Each mirrors its exact helper on the
// member's value, raw, which is nil when the member is absent, and on a zero
// *dst.

// fastField mirrors decodeField. ok is false where decodeField returns an
// error, or where dec declines.
func fastField[T any](raw []byte, dst *T, dec func(*T, []byte) bool) (decoded, ok bool) {
	if raw == nil || raw[0] == 'n' {
		return false, true
	}
	if !dec(dst, raw) {
		return false, false
	}
	return true, true
}

// fastLenient mirrors decodeLenient. It never declines: a value that dec
// declines goes to json.Unmarshal, as in decodeLenient. On false, *dst is
// zero.
func fastLenient[T any](raw []byte, dst *T, dec func(*T, []byte) bool, check ...shapeID) bool {
	if raw == nil || raw[0] == 'n' || !fastOrExact(dst, raw, dec) {
		return false
	}
	for _, id := range check {
		if !id.accepts(raw) {
			*dst = *new(T)
			return false
		}
	}
	return true
}

// fastItems mirrors decodeItems. ok is false where decodeItems returns an
// error.
func fastItems[T any](raw []byte, dst *[]T, dec func(*T, []byte) bool, check ...shapeID) (decoded, ok bool) {
	if raw == nil {
		return false, true
	}
	if raw[0] != '[' {
		return false, raw[0] == 'n'
	}
	var buf [64][]byte
	items, ok := appendItems(buf[:0], raw)
	kept := make([]T, len(items))
	n := 0
	for _, item := range items {
		// An item decodes in place, and leaves its place zero for the next
		// item when it does not.
		if fastLenient(item, &kept[n], dec, check...) {
			n++
		}
	}
	*dst = kept[:n]
	return true, ok
}

// fastLenientItems mirrors decodeLenientItems.
func fastLenientItems[T any](raw []byte, dst *[]T, dec func(*T, []byte) bool, check ...shapeID) bool {
	decoded, _ := fastItems(raw, dst, dec, check...)
	return decoded
}

// fastPatchLenient mirrors decodePatch.
func fastPatchLenient[T any](raw []byte, dst *Patch[T], dec func(*T, []byte) bool, check ...shapeID) {
	switch {
	case raw == nil:
	case raw[0] == 'n':
		*dst = PatchNull[T]()
	default:
		var v T
		if fastLenient(raw, &v, dec, check...) {
			*dst = PatchSet(v)
		}
	}
}

// fastPatchLenientItems mirrors decodePatchItems.
func fastPatchLenientItems[T any](raw []byte, dst *Patch[[]T], dec func(*T, []byte) bool, check ...shapeID) {
	switch {
	case raw == nil:
	case raw[0] == 'n':
		*dst = PatchNull[[]T]()
	default:
		var v []T
		if fastLenientItems(raw, &v, dec, check...) {
			*dst = PatchSet(v)
		}
	}
}

// hasNullItem reports whether raw is an array with a null item or an object
// with a null member value, where rejectNullItems returns an error.
func hasNullItem(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case '[':
		it, _ := arrayItems(raw)
		for it.next() {
			if it.item[0] == 'n' {
				return true
			}
		}
	case '{':
		it, _ := objectMembers(raw)
		for it.next() {
			if it.value[0] == 'n' {
				return true
			}
		}
	}
	return false
}

// A fastObject reads the members of the JSON object that a generated
// decodeFast decodes. The generated code switches on the folded key, as
// encoding/json matches a key to a field name in any case, and takes a member
// only under its exact name:
//
//	var raw struct{ FieldName []byte }
//	var w fastObject
//	if !w.start(b) { return false }
//	for w.next() {
//		switch string(w.folded()) {
//		case "fieldname":
//			if string(w.key) != "fieldName" || !w.once(&raw.FieldName) || ... { return false }
//		default: if !w.skip() { return false }
//		}
//	}
//	if !w.done() { return false }
//
// next stops at a key with an escape or a byte beyond ASCII, which may fold
// onto a field name too (the Kelvin sign onto k), and done then reports false.
// once and skip decline a key that repeats.
type fastObject struct {
	memberIter
	// lower holds the key in ASCII lower case, n bytes of it, and n is -1 for
	// a key longer than any field name.
	lower [32]byte
	n     int
	// other holds the keys of the members that skip read.
	other  [16][]byte
	others int
}

// start begins to read b, and returns false when b is neither an object nor
// null. encoding/json reads null into a struct as an object without members:
// it leaves the struct as it is.
func (w *fastObject) start(b []byte) bool {
	if b[0] == 'n' {
		return true
	}
	var ok bool
	w.memberIter, ok = objectMembers(b)
	return ok
}

func (w *fastObject) next() bool {
	if !w.memberIter.next() {
		return false
	}
	w.n = len(w.key)
	if w.n > len(w.lower) {
		w.n = -1
	}
	for i, c := range w.key {
		if c >= utf8.RuneSelf || c == '\\' {
			w.broken = true
			return false
		}
		if w.n >= 0 {
			w.lower[i] = lower(c)
		}
	}
	return true
}

// folded returns the key in ASCII lower case, or nothing for a key longer
// than any field name, which folds onto none.
func (w *fastObject) folded() []byte {
	if w.n < 0 {
		return nil
	}
	return w.lower[:w.n]
}

// once keeps the value of a field's member in *raw, and returns false when
// the member repeats.
func (w *fastObject) once(raw *[]byte) bool {
	if *raw != nil {
		return false
	}
	*raw = w.value
	return true
}

// done reports whether the object ended without a member that the fast
// decoder cannot read.
func (w *fastObject) done() bool {
	return !w.broken
}

// skip passes over a member whose key names no field, and returns false when
// the key repeats.
func (w *fastObject) skip() bool {
	if w.others == len(w.other) {
		return false
	}
	for _, key := range w.other[:w.others] {
		if bytes.Equal(key, w.key) {
			return false
		}
	}
	w.other[w.others] = w.key
	w.others++
	return true
}

// keep adds a member whose key names no field to *dst, as
// additionalProperties does for a struct with additionalProperties, and
// returns false when the key repeats.
func (w *fastObject) keep(dst *map[string]json.RawMessage) bool {
	// next took the keys with an escape or a byte beyond ASCII.
	key := string(w.key)
	if _, repeats := (*dst)[key]; repeats {
		return false
	}
	if *dst == nil {
		*dst = map[string]json.RawMessage{}
	}
	(*dst)[key] = append(json.RawMessage(nil), w.value...)
	return true
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// itemsAreObjects reports whether every item of the array b is an object or
// null, where encoding/json decodes b into a []map[string]json.RawMessage.
func itemsAreObjects(b []byte) bool {
	it, ok := arrayItems(b)
	for ok && it.next() {
		if c := it.item[0]; c != '{' && c != 'n' {
			return false
		}
	}
	return ok && !it.broken
}

// absentOrNull reports whether a member value is absent (nil) or null.
func absentOrNull(raw []byte) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// isJSONString reports whether a member value is a string.
func isJSONString(raw []byte) bool {
	return len(raw) > 0 && raw[0] == '"'
}

// isText reports whether json.Unmarshal reads raw into a string without an
// error and gives text, as the exact decoder checks a const property.
func isText(raw []byte, text string) bool {
	if string(raw) == "null" {
		return text == ""
	}
	if !isJSONString(raw) {
		return false
	}
	t, _ := unquoteBytes(raw[1 : len(raw)-1])
	return string(t) == text
}

// parseInt reads b, the text of a JSON number, as strconv.ParseInt(b, 10, 64)
// does: it returns false for a number with a fraction or an exponent and for
// one beyond the range of an int64. It does not allocate, as ParseInt's
// error would.
func parseInt(b []byte) (int64, bool) {
	digits := b
	neg := b[0] == '-'
	if neg {
		digits = b[1:]
	}
	if len(digits) == 0 || len(digits) > 19 {
		return 0, false
	}
	var u uint64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		u = u*10 + uint64(c-'0')
	}
	switch {
	case neg && u <= 1<<63:
		return -int64(u), true
	case !neg && u < 1<<63:
		return int64(u), true
	}
	return 0, false
}

// unquote returns the text of the content s of a JSON string, without its
// quotes, as encoding/json reads it.
func unquote(s []byte) (string, bool) {
	t, ok := unquoteBytes(s)
	return string(t), ok
}

// unquoteBytes follows unquoteBytes of encoding/json: it returns s itself
// when s holds no escape and is valid UTF-8, and otherwise decodes the
// escapes and reads each byte of invalid UTF-8, and each lone surrogate, as
// U+FFFD.
func unquoteBytes(s []byte) ([]byte, bool) {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\\' || c == '"' || c < ' ' {
			break
		}
		if c < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		i += size
	}
	if i == len(s) {
		return s, true
	}
	buf := make([]byte, i, len(s)+utf8.UTFMax)
	copy(buf, s[:i])
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\':
			if i+1 == len(s) {
				return nil, false
			}
			switch e := s[i+1]; e {
			case '"', '\\', '/', '\'':
				buf = append(buf, e)
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				r := getu4(s[i:])
				if r < 0 {
					return nil, false
				}
				i += 6
				if utf16.IsSurrogate(r) {
					if dec := utf16.DecodeRune(r, getu4(s[i:])); dec != unicode.ReplacementChar {
						// A valid pair.
						i += 6
						buf = utf8.AppendRune(buf, dec)
						continue
					}
					r = unicode.ReplacementChar
				}
				buf = utf8.AppendRune(buf, r)
				continue
			default:
				return nil, false
			}
			i += 2
		case c == '"', c < ' ':
			return nil, false
		case c < utf8.RuneSelf:
			buf = append(buf, c)
			i++
		default:
			r, size := utf8.DecodeRune(s[i:])
			i += size
			buf = utf8.AppendRune(buf, r)
		}
	}
	return buf, true
}

// getu4 decodes \uXXXX from the start of s, or returns -1.
func getu4(s []byte) rune {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return -1
	}
	var r rune
	for _, c := range s[2:6] {
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}
