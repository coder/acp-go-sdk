package acp

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

// The generated decoders read the structure of a JSON value with the scanner
// in this file instead of decoding it again with encoding/json. It expects
// JSON that encoding/json accepts; on other input it never panics or loops,
// and it reports that it could not read the value.

// isSpace reports whether c is JSON whitespace.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// skipSpace returns the index of the first byte at or after i that is not
// whitespace.
func skipSpace(b []byte, i int) int {
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	return i
}

// skipString returns the index just past the JSON string whose opening quote
// is at b[i], or -1 when the string does not end.
func skipString(b []byte, i int) int {
	start := i
	for i++; ; i++ {
		// Find the next quote. Most strings are short, so read the next two
		// words here before calling bytes.IndexByte.
		for n := 0; n < 2 && i+8 <= len(b); n++ {
			w := binary.LittleEndian.Uint64(b[i:]) ^ (0x0101010101010101 * '"')
			// The lowest byte of w that is zero, if any, has its high bit set
			// in m, and no byte below it does.
			if m := (w - 0x0101010101010101) &^ w & 0x8080808080808080; m != 0 {
				i += bits.TrailingZeros64(m) / 8
				goto quote
			}
			i += 8
		}
		if q := bytes.IndexByte(b[i:], '"'); q >= 0 {
			i += q
		} else {
			return -1
		}
	quote:
		if b[i-1] != '\\' {
			return i + 1
		}
		// The quote ends the string unless an odd run of backslashes before
		// it escapes it.
		j := i
		for j > start+1 && b[j-1] == '\\' {
			j--
		}
		if (i-j)%2 == 0 {
			return i + 1
		}
	}
}

// skipValue returns the index just past the JSON value that starts at b[i],
// after any whitespace, or -1 when b has no complete value there.
func skipValue(b []byte, i int) int {
	i = skipSpace(b, i)
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				if i = skipString(b, i); i < 0 {
					return -1
				}
				i--
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
		}
		return -1
	case ',', ':', '}', ']':
		return -1
	}
	// A number or a literal ends at the next delimiter.
	start := i
	for i < len(b) && !isSpace(b[i]) && b[i] != ',' && b[i] != '}' && b[i] != ']' && b[i] != ':' {
		i++
	}
	if i == start {
		return -1
	}
	return i
}

// trimValue returns b without the whitespace around it.
func trimValue(b []byte) []byte {
	i, j := skipSpace(b, 0), len(b)
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}

// isNull reports whether the JSON value b is null.
func isNull(b []byte) bool {
	b = trimValue(b)
	return len(b) == 4 && string(b) == "null"
}

// A memberIter reads the members of a JSON object in order:
//
//	it, ok := objectMembers(b)
//	for it.next() {
//		// it.key, it.value
//	}
//	if it.broken { ... }
type memberIter struct {
	b []byte
	i int
	// key is the raw text of the member's name, without the quotes, and
	// escaped tells whether it holds a backslash escape.
	key     []byte
	escaped bool
	// value is the member's value, without whitespace around it.
	value []byte
	seen  bool
	// broken is set when the object does not continue as JSON does.
	broken bool
}

// objectMembers returns an iterator over the members of b, and false when b
// is not a JSON object.
func objectMembers(b []byte) (memberIter, bool) {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '{' {
		return memberIter{}, false
	}
	return memberIter{b: b, i: i + 1}, true
}

// next reads the next member, and returns false at the end of the object or
// when it breaks off.
func (it *memberIter) next() bool {
	if it.broken || it.b == nil {
		return false
	}
	b := it.b
	i := skipSpace(b, it.i)
	if i < len(b) && b[i] == '}' {
		it.b = nil
		return false
	}
	if it.seen {
		// A member before this one: a comma must follow it.
		if i >= len(b) || b[i] != ',' {
			return it.fail()
		}
		i = skipSpace(b, i+1)
	}
	if i >= len(b) || b[i] != '"' {
		return it.fail()
	}
	end := skipString(b, i)
	if end < 0 {
		return it.fail()
	}
	it.key = b[i+1 : end-1]
	it.escaped = false
	for _, c := range it.key {
		if c == '\\' {
			it.escaped = true
			break
		}
	}
	i = skipSpace(b, end)
	if i >= len(b) || b[i] != ':' {
		return it.fail()
	}
	start := skipSpace(b, i+1)
	end = skipValue(b, start)
	if end < 0 {
		return it.fail()
	}
	it.value = b[start:end]
	it.i = end
	it.seen = true
	return true
}

func (it *memberIter) fail() bool {
	it.broken = true
	return false
}

// An itemIter reads the items of a JSON array in order, as memberIter reads
// the members of an object.
type itemIter struct {
	b      []byte
	i      int
	item   []byte
	seen   bool
	broken bool
}

// arrayItems returns an iterator over the items of b, and false when b is not
// a JSON array.
func arrayItems(b []byte) (itemIter, bool) {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '[' {
		return itemIter{}, false
	}
	return itemIter{b: b, i: i + 1}, true
}

func (it *itemIter) next() bool {
	if it.broken || it.b == nil {
		return false
	}
	b := it.b
	i := skipSpace(b, it.i)
	if i < len(b) && b[i] == ']' {
		it.b = nil
		return false
	}
	if it.seen {
		if i >= len(b) || b[i] != ',' {
			it.broken = true
			return false
		}
		i = skipSpace(b, i+1)
	}
	end := skipValue(b, i)
	if end < 0 {
		it.broken = true
		return false
	}
	it.item = b[i:end]
	it.i = end
	it.seen = true
	return true
}
