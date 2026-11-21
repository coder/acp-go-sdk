package acp

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// shapeScanSeeds are JSON values on the edges of what scan reads itself:
// duplicate and escaped keys, escaped and non-UTF-8 strings, numbers on the
// edges of a range, and values nested past maxScanDepth.
var shapeScanSeeds = []string{
	`null`, `true`, `false`, `""`, `[]`, `{}`, `0`, `-0`, `1.0`, `1e3`, `-1`,
	`4294967295`, `4294967296`, `2147483648`, `-2147483649`,
	`9223372036854775807`, `9223372036854775808`, `-9223372036854775809`,
	`-32800`, `-32800.0`, `-3.28e4`, `"high"`, `"h\u0069gh"`, "\"hi\xffgh\"", `"héé"`, "\"h\xc3\"",
	`"end_turn"`, `"assistant"`, `["assistant","user"]`,
	`{"stopReason":"end_turn"}`, `{"stopReason":"end\u005fturn"}`, `{"stopReason":"cancelled","stopReason":"oops"}`,
	`{"stopReason":"oops","stopReason":"refusal"}`,
	`{"type":"terminal","id":"a","name":"b","args":["--cli"],"env":{"A":"1"}}`,
	"{\"type\":\"terminal\",\"id\":\"a\",\"name\":\"b\",\"env\":{\"\xff\":1,\"\xfe\":\"s\"}}",
	`{"type":"terminal","id":"a","name":"b","env":{"A":1,"A":"s"}}`,
	`{"type":"terminal","id":"a","name":"b","env":{"A":"s","A":1}}`,
	`{"\u0069d":"a","name":"b"}`, `{"id":"a","name":1,"name":"b"}`, `{"id":"a","name":"b","name":1}`,
	`{"id":"a","name":"b","id":"c"}`, ` { "id" : "a" , "name" : "b" } `,
	`{"content":"a","priority":"high","status":"pending"}`, `{"content":"a","priority":"urgent","status":"pending"}`,
	`{"type":"diff","path":"/a","newText":"y"}`, `{"type":"diff","newText":"x"}`,
	`{"type":"content","content":{"type":"text","text":"hi"}}`,
	`{"path":"/a","line":1}`, `{"path":"/a","line":-1}`, `{"path":"/a","line":1.5}`,
	`{"used":1,"size":2,"cachedReadTokens":3}`, `{"exitCode":4294967296,"signal":null}`,
	`{"code":-32800,"message":"m"}`, `{"code":-32800.0,"message":"m"}`,
	`{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8,"i":9,"j":10,"k":11,"l":12,"m":13,"n":14,"o":15,"p":16,"id":"x"}`,
	strings.Repeat(`[`, 1500) + strings.Repeat(`]`, 1500),
	strings.Repeat(`{"a":`, 1500) + `1` + strings.Repeat(`}`, 1500),
}

// testShapes are shapes with rules that the generated table has no use of
// yet: values of every type that a shape may list, and shapes that follow a
// value as deep as it goes.
var testShapes = func() []*shape {
	deepArray := &shape{kinds: kindArray | kindInteger}
	deepArray.items = deepArray
	deepObject := &shape{kinds: kindObject | kindInteger, required: []string{"a"}}
	deepObject.props = map[string]*shape{"a": deepObject}
	return []*shape{
		{values: []any{nil, true, json.Number("1"), "a"}},
		{values: []any{false, json.Number("-0"), "hi\uFFFDgh", "h\u00e9\u00e9"}},
		{values: []any{}},
		{values: []any{1.0}},
		{ints: &intRange{math.MinInt64, math.MaxInt64}},
		{ints: &intRange{-1, 0}, not: &shape{kinds: kindNull}},
		{kinds: kindObject, rest: &shape{kinds: kindString}},
		{kinds: kindObject, props: map[string]*shape{"\uFFFD": {kinds: kindString}, "a": {kinds: kindInteger}}},
		{required: []string{"a", "id"}, props: map[string]*shape{"id": {values: []any{"c"}}}},
		{anyOf: []*shape{}},
		{anyOf: []*shape{{kinds: kindNull}, {items: &shape{values: []any{"assistant"}}}}},
		deepArray,
		deepObject,
	}
}()

// TestShapeScan_Corpus checks scan against decoding for every value in every
// message of the inbound benchmark corpus, which holds every value that a
// shape checks there, and every shape. scan must tell nearly all of them:
// it cannot tell only for an escaped string that a values rule compares.
func TestShapeScan_Corpus(t *testing.T) {
	var pairs, unknown int
	for _, m := range loadInboundCorpus(t) {
		if !json.Valid(m.line) {
			continue
		}
		eachValue(m.line, func(b []byte) {
			pairs += len(shapes)
			unknown += checkShapeScan(t, b)
		})
	}
	t.Logf("scan could not tell %d of %d values and shapes", unknown, pairs)
	if unknown*100 > pairs {
		t.Fatalf("scan could not tell %d of %d values and shapes", unknown, pairs)
	}
}

func TestShapeScan_Seeds(t *testing.T) {
	for _, s := range shapeScanSeeds {
		checkShapeScan(t, []byte(s))
	}
}

func FuzzShapeScan(f *testing.F) {
	for _, s := range shapeScanSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		checkShapeScan(t, b)
	})
}

// eachValue calls f for b, a JSON value, and for every value in it.
func eachValue(b []byte, f func([]byte)) {
	f(b)
	if it, ok := objectMembers(b); ok {
		for it.next() {
			eachValue(it.value, f)
		}
	}
	if it, ok := arrayItems(b); ok {
		for it.next() {
			eachValue(it.item, f)
		}
	}
}

// checkShapeScan checks that accepts, and scan when it can tell, report for b
// what fits reports for the value that b decodes to, for every shape in the
// table and in testShapes. It returns the number of shapes in the table for
// which scan could not tell. On input that is not JSON, it only checks that
// scan does not panic or loop.
func checkShapeScan(t *testing.T, b []byte) (unknown int) {
	t.Helper()
	if !json.Valid(b) {
		for id := range shapes {
			shapes[id].scan(trimValue(b), 0)
		}
		return 0
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	for id := range shapes {
		want := shapes[id].fits(v)
		if got := shapeID(id).accepts(b); got != want {
			t.Fatalf("shapes[%d]: accepts(%q) = %v, want %v", id, b, got, want)
		}
		switch got := shapes[id].scan(trimValue(b), 0); {
		case got == fitUnknown:
			unknown++
		case (got == fitYes) != want:
			t.Fatalf("shapes[%d]: scan(%q) = %d, want fits = %v", id, b, got, want)
		}
	}
	for i, s := range testShapes {
		if got, want := s.scan(trimValue(b), 0), s.fits(v); got != fitUnknown && (got == fitYes) != want {
			t.Fatalf("testShapes[%d]: scan(%q) = %d, want fits = %v", i, b, got, want)
		}
	}
	return unknown
}
