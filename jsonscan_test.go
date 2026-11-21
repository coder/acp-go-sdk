package acp

import (
	"bytes"
	"encoding/json"
	"testing"
)

var scanSeeds = []string{
	`null`, `true`, `false`, `0`, `-1.5e+3`, `"a"`, `""`, `"\"\\\/\b\f\n\r\t\u00e9"`,
	`[]`, `{}`, ` [ 1 , [2] , {"a" : [ ] } ] `, `{"a":1,"b":{"c":"}"},"d":"]"}`,
	`{"k\"ey":"v","\u0074ype":"text"}`, `{"a":null,"a":2}`, "\t{\n\"a\"\r:\"b\"}\n",
	`{`, `{"a"`, `{"a":`, `{"a":1,`, `{"a":1 "b":2}`, `[1,]`, `"abc`, `{"a":1}}`, `,`, ``,
}

// TestJSONScan_Corpus reads every message of the inbound benchmark corpus
// with the scanner and compares it with encoding/json.
func TestJSONScan_Corpus(t *testing.T) {
	for _, m := range loadInboundCorpus(t) {
		if json.Valid(m.line) {
			checkScan(t, m.line)
		}
	}
	for _, s := range scanSeeds {
		checkScan(t, []byte(s))
	}
}

func FuzzJSONScan(f *testing.F) {
	for _, s := range scanSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		checkScan(t, b)
	})
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
