package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var scanSeeds = append([]string{
	`null`, `true`, `false`, `0`, `-1.5e+3`, `"a"`, `""`, `"\"\\\/\b\f\n\r\t\u00e9"`,
	`[]`, `{}`, ` [ 1 , [2] , {"a" : [ ] } ] `, `{"a":1,"b":{"c":"}"},"d":"]"}`,
	`{"k\"ey":"v","\u0074ype":"text"}`, `{"a":null,"a":2}`, "\t{\n\"a\"\r:\"b\"}\n",
	`{`, `{"a"`, `{"a":`, `{"a":1,`, `{"a":1 "b":2}`, `[1,]`, `"abc`, `{"a":1}}`, `,`, ``,
}, escapeSeeds()...)

// escapeSeeds returns strings with an escape or a run of backslashes after
// each of 0 to 40 bytes, so that the escape falls on every byte of the words
// that skipString reads and past them. Each also comes as a key and an item,
// and without its closing quote.
func escapeSeeds() []string {
	var seeds []string
	for n := 0; n <= 40; n++ {
		pad := strings.Repeat("x", n)
		for _, esc := range []string{`\"`, `\\`, `\\\"`, `\\\\`, `\\\\\"`, `\u0022`} {
			s := `"` + pad + esc + `"`
			seeds = append(seeds, s, s[:len(s)-1], `{`+s+`:[`+s+`,`+s+`]}`)
		}
	}
	return seeds
}

// TestJSONScan_Corpus reads every message of the inbound benchmark corpus
// with the scanner and compares it with encoding/json.
func TestJSONScan_Corpus(t *testing.T) {
	for _, m := range loadInboundCorpus(t) {
		checkSkip(t, m.line)
		if json.Valid(m.line) {
			checkScan(t, m.line)
		}
	}
	for _, s := range scanSeeds {
		checkSkip(t, []byte(s))
		checkScan(t, []byte(s))
	}
}

func FuzzJSONScan(f *testing.F) {
	for _, s := range scanSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		checkSkip(t, b)
		checkScan(t, b)
	})
}

// checkSkip checks that skipString and skipValue return what the scanners
// that read one byte at a time return, from every offset of b, JSON or not.
func checkSkip(t *testing.T, b []byte) {
	t.Helper()
	for i := range b {
		if b[i] == '"' {
			if got, want := skipString(b, i), skipStringBytewise(b, i); got != want {
				t.Fatalf("skipString(%q, %d) = %d, want %d", b, i, got, want)
			}
		}
	}
	for i := 0; i <= len(b); i++ {
		if got, want := skipValue(b, i), skipValueBytewise(b, i); got != want {
			t.Fatalf("skipValue(%q, %d) = %d, want %d", b, i, got, want)
		}
	}
}

// skipStringBytewise and skipValueBytewise are skipString and skipValue as
// they were before they skipped many bytes at once.
func skipStringBytewise(b []byte, i int) int {
	for i++; i < len(b); i++ {
		switch b[i] {
		case '"':
			return i + 1
		case '\\':
			i++
		}
	}
	return -1
}

func skipValueBytewise(b []byte, i int) int {
	i = skipSpace(b, i)
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return skipStringBytewise(b, i)
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				if i = skipStringBytewise(b, i); i < 0 {
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
	start := i
	for i < len(b) && !isSpace(b[i]) && b[i] != ',' && b[i] != '}' && b[i] != ']' && b[i] != ':' {
		i++
	}
	if i == start {
		return -1
	}
	return i
}

// checkScan checks that the scanner reads b, and every object and array in
// it, as encoding/json does. Input that is not JSON must not make it panic.
func checkScan(t *testing.T, b []byte) {
	t.Helper()
	if !json.Valid(b) {
		// Only check that nothing panics or loops.
		skipValue(b, 0)
		if it, ok := objectMembers(b); ok {
			for it.next() {
			}
		}
		if it, ok := arrayItems(b); ok {
			for it.next() {
			}
		}
		return
	}
	end := skipValue(b, 0)
	if end < 0 || skipSpace(b, end) != len(b) {
		t.Fatalf("skipValue(%q) = %d, want the end of the value", b, end)
	}
	if got, want := isNull(b), string(trimValue(b)) == "null"; got != want {
		t.Fatalf("isNull(%q) = %v", b, got)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	switch tok {
	case json.Delim('{'):
		it, ok := objectMembers(b)
		if !ok {
			t.Fatalf("objectMembers(%q) is not ok", b)
		}
		for dec.More() {
			key, _ := dec.Token()
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				t.Fatal(err)
			}
			if !it.next() {
				t.Fatalf("%q: the scanner stops before member %q", b, key)
			}
			var gotKey string
			if err := json.Unmarshal(append(append([]byte{'"'}, it.key...), '"'), &gotKey); err != nil || gotKey != key {
				t.Fatalf("%q: key %q, want %q (%v)", b, it.key, key, err)
			}
			if it.escaped != bytes.ContainsRune(it.key, '\\') {
				t.Fatalf("%q: escaped = %v for key %q", b, it.escaped, it.key)
			}
			if !bytes.Equal(it.value, value) {
				t.Fatalf("%q: member %q is %q, want %q", b, key, it.value, value)
			}
			checkScan(t, it.value)
		}
		if it.next() || it.broken {
			t.Fatalf("%q: the scanner does not end with the object", b)
		}
	case json.Delim('['):
		it, ok := arrayItems(b)
		if !ok {
			t.Fatalf("arrayItems(%q) is not ok", b)
		}
		for dec.More() {
			var item json.RawMessage
			if err := dec.Decode(&item); err != nil {
				t.Fatal(err)
			}
			if !it.next() || !bytes.Equal(it.item, item) {
				t.Fatalf("%q: item %q, want %q", b, it.item, item)
			}
			checkScan(t, it.item)
		}
		if it.next() || it.broken {
			t.Fatalf("%q: the scanner does not end with the array", b)
		}
	default:
		if _, ok := objectMembers(b); ok {
			t.Fatalf("objectMembers(%q) is ok", b)
		}
		if _, ok := arrayItems(b); ok {
			t.Fatalf("arrayItems(%q) is ok", b)
		}
	}
}

// BenchmarkSkipValue skips every corpus message and every value nested in
// one, as the decoders do when they read each object and array level.
func BenchmarkSkipValue(b *testing.B) {
	var values [][]byte
	var walk func(v []byte)
	walk = func(v []byte) {
		values = append(values, v)
		if it, ok := objectMembers(v); ok {
			for it.next() {
				walk(it.value)
			}
		}
		if it, ok := arrayItems(v); ok {
			for it.next() {
				walk(it.item)
			}
		}
	}
	for _, m := range loadInboundCorpus(b) {
		if json.Valid(m.line) {
			walk(m.line)
		}
	}
	var size int64
	for _, v := range values {
		size += int64(len(v))
	}
	b.SetBytes(size)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, v := range values {
			if skipValue(v, 0) != len(v) {
				b.Fatalf("skipValue(%q) does not end with the value", v)
			}
		}
	}
}
