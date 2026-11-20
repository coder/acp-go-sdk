package acp

import (
	"encoding/binary"
	"math/bits"
)

// validJSON reports whether b is one JSON value with only whitespace around
// it. It must return exactly what json.Valid returns for every input, so
// that decodeJSON matches json.Unmarshal; FuzzValidJSON compares the two. It
// follows encoding/json's scanner: a control character in a string is
// invalid, invalid UTF-8 is not, and values nest at most maxNestingDepth
// deep. Where encoding/json makes a call per byte, validJSON reads the bytes
// of a string eight at a time.
func validJSON(b []byte) bool {
	i := validValue(b, skipSpace(b, 0), 0)
	return i >= 0 && skipSpace(b, i) == len(b)
}

// maxNestingDepth is encoding/json's limit on how deep arrays and objects
// nest.
const maxNestingDepth = 10000

// validValue returns the index just past the valid JSON value at b[i], or -1.
// depth counts the arrays and objects that hold the value.
func validValue(b []byte, i, depth int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return validString(b, i)
	case '{':
		return validObject(b, i, depth+1)
	case '[':
		return validArray(b, i, depth+1)
	case 't':
		return validLiteral(b, i, "true")
	case 'f':
		return validLiteral(b, i, "false")
	case 'n':
		return validLiteral(b, i, "null")
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return validNumber(b, i)
	}
	return -1
}

// validObject returns the index just past the valid object that starts at
// b[i], or -1. depth counts the object and the values that hold it.
func validObject(b []byte, i, depth int) int {
	if depth > maxNestingDepth {
		return -1
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		return i + 1
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return -1
		}
		if i = validString(b, i); i < 0 {
			return -1
		}
		if i = skipSpace(b, i); i >= len(b) || b[i] != ':' {
			return -1
		}
		if i = validValue(b, skipSpace(b, i+1), depth); i < 0 {
			return -1
		}
		if i = skipSpace(b, i); i >= len(b) {
			return -1
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case '}':
			return i + 1
		default:
			return -1
		}
	}
}

// validArray returns the index just past the valid array that starts at
// b[i], or -1. depth counts the array and the values that hold it.
func validArray(b []byte, i, depth int) int {
	if depth > maxNestingDepth {
		return -1
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == ']' {
		return i + 1
	}
	for {
		if i = validValue(b, i, depth); i < 0 {
			return -1
		}
		if i = skipSpace(b, i); i >= len(b) {
			return -1
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case ']':
			return i + 1
		default:
			return -1
		}
	}
}

// validString returns the index just past the valid string whose opening
// quote is at b[i], or -1.
func validString(b []byte, i int) int {
	for i++; ; {
		// Find the next quote, backslash or control character, eight bytes
		// at a time while eight remain.
		if i+8 <= len(b) {
			m := specialBytes(binary.LittleEndian.Uint64(b[i:]))
			if m == 0 {
				i += 8
				continue
			}
			i += bits.TrailingZeros64(m) / 8
		} else {
			for i < len(b) && b[i] >= 0x20 && b[i] != '"' && b[i] != '\\' {
				i++
			}
			if i >= len(b) {
				return -1
			}
		}
		switch b[i] {
		case '"':
			return i + 1
		case '\\':
			if i+1 >= len(b) {
				return -1
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+5 >= len(b) || !isHex(b[i+2]) || !isHex(b[i+3]) || !isHex(b[i+4]) || !isHex(b[i+5]) {
					return -1
				}
				i += 6
			default:
				return -1
			}
		default:
			// A control character.
			return -1
		}
	}
}

// specialBytes returns a mask whose lowest set bit is the high bit of the
// first byte of x, in little-endian order, that is a quote, a backslash or a
// control character; it is 0 when x has none. Bits above that one may be
// set for bytes that are not special. The tests for a zero byte and for a
// byte less than 0x20 come from Bit Twiddling Hacks:
// https://graphics.stanford.edu/~seander/bithacks.html#ZeroInWord.
func specialBytes(x uint64) uint64 {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	quote, backslash := x^('"'*ones), x^('\\'*ones)
	control := (x - 0x20*ones) & ^x
	quote = (quote - ones) & ^quote
	backslash = (backslash - ones) & ^backslash
	return (control | quote | backslash) & highs
}

// isHex reports whether c is a hexadecimal digit.
func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// validLiteral returns the index just past lit at b[i], or -1.
func validLiteral(b []byte, i int, lit string) int {
	if len(b)-i < len(lit) || string(b[i:i+len(lit)]) != lit {
		return -1
	}
	return i + len(lit)
}

// validNumber returns the index just past the valid number that starts at
// b[i], or -1.
func validNumber(b []byte, i int) int {
	if b[i] == '-' {
		i++
	}
	if i < len(b) && b[i] == '0' {
		i++
	} else if i = digits(b, i); i < 0 {
		return -1
	}
	if i < len(b) && b[i] == '.' {
		if i = digits(b, i+1); i < 0 {
			return -1
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i = digits(b, i); i < 0 {
			return -1
		}
	}
	return i
}

// digits returns the index just past the decimal digits at b[i], or -1 when
// b[i] is not one.
func digits(b []byte, i int) int {
	j := i
	for j < len(b) && '0' <= b[j] && b[j] <= '9' {
		j++
	}
	if j == i {
		return -1
	}
	return j
}
