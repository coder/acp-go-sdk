package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// unionTypes makes a value of each union whose UnmarshalJSON calls
// readUnionObject, by name: the unions of generatedUnions, which
// TestUnionObject_ListsEveryUnion checks.
var unionTypes = func() map[string]func() json.Unmarshaler {
	makers := map[string]func() json.Unmarshaler{}
	for _, typ := range generatedUnions {
		typ := typ
		makers[typ.Name()] = func() json.Unmarshaler { return reflect.New(typ).Interface().(json.Unmarshaler) }
	}
	return makers
}()

// unionNames lists the keys of unionTypes in order.
func unionNames() []string {
	var names []string
	for name := range unionTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestUnionObject_ListsEveryUnion(t *testing.T) {
	src, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	decoder := regexp.MustCompile(`(?s)\nfunc \(u \*(\w+)\) UnmarshalJSON\(b \[\]byte\) error \{\n(.*?)\n\}\n`)
	found := map[string]bool{}
	for _, m := range decoder.FindAllSubmatch(src, -1) {
		if bytes.Contains(m[2], []byte("readUnionObject(b)")) {
			found[string(m[1])] = true
		}
	}
	for name := range found {
		if unionTypes[name] == nil {
			t.Errorf("unionTypes lacks %s", name)
		}
	}
	for name := range unionTypes {
		if !found[name] {
			t.Errorf("%s does not call readUnionObject", name)
		}
	}
}

// unionSeeds are values that take every path of readUnionObject and of the
// union decoders: values that are not JSON, null, other values than objects,
// and objects with escaped or repeated keys or tags that need decoding.
var unionSeeds = []string{
	``, ` `, `null`, ` null `, `nul`, `0`, `-1`, `1.5`, `"x"`, `true`, `[]`, `[null]`, `[{}]`, `[1]`, `{`, `}`, `{}`, ` {} `,
	`{"type":"text","text":"a"}`, ` {"type" : "text", "text" : "a"} `, `{"type":"text","text":"a"} x`,
	`{"type":"text","text":tru}`, `{"type":"text","text":"a",}`, `{"type":"text""text":"a"}`,
	`{"typ\u0065":"text","text":"a"}`, `{"type":"t\u0065xt","text":"a"}`, `{"Type":"text","text":"a"}`,
	`{"type":"image","type":"text","text":"a"}`, `{"type":"text","text":"a","type":"image"}`,
	`{"type":1,"text":"a"}`, `{"type":null,"text":"a"}`, "{\"type\":\"te\xffxt\",\"text\":\"a\"}",
	`{"type":"text","text":null}`, `{"type":"content","content":{"type":"text","text":"a"}}`,
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"}}`,
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"},"sessionUpdate":"plan"}`,
	`{"outcome":"selected","optionId":"a"}`, `{"outcome":"cancelled"}`, `{"stopReason":"end_turn"}`, `{"stopReason":7}`,
	`{"stopReason":null}`, `{"stopReason":"other"}`, `{"state":"idle","stopReason":{}}`,
	`{"id":1,"result":{}}`, `{"id":1,"error":{"code":-32600,"message":"m"}}`, `{"id":1,"error":null}`,
	`{"uri":"file:///a","text":"t"}`, `{"uri":"file:///a","blob":"YQ=="}`,
	`{"name":"a","command":"c","args":[],"env":[]}`, `{"type":"http","name":"a","url":"u","headers":[]}`,
	`[{"group":"g","name":"n","options":[]}]`, `[{"value":"v","name":"n"}]`,
	`{"mode":"form","message":"m","requestedSchema":{"type":"object","properties":{}}}`,
	`{"mode":"url","message":"m","url":"u","elicitationId":"e"}`, `{"mode":"other","x":1}`,
	`{"action":"accept","content":{}}`, `{"type":"string","enum":["a"]}`, `{"type":"array","items":{"type":"string","enum":["a"]}}`,
}

// unionInputs returns every distinct value in the inbound benchmark corpus,
// the members and items of every object and array in it included, and the
// unionSeeds.
func unionInputs(tb testing.TB) [][]byte {
	seen := map[string]bool{}
	var inputs [][]byte
	var add func(b []byte)
	add = func(b []byte) {
		if seen[string(b)] {
			return
		}
		seen[string(b)] = true
		inputs = append(inputs, b)
		if it, ok := objectMembers(b); ok {
			for it.next() {
				add(it.value)
			}
		}
		if it, ok := arrayItems(b); ok {
			for it.next() {
				add(it.item)
			}
		}
	}
	for _, m := range loadInboundCorpus(tb) {
		if json.Valid(m.line) {
			add(m.line)
		}
	}
	for _, s := range unionSeeds {
		add([]byte(s))
	}
	return inputs
}

// variantsOf returns copies of the object b, changed in ways that the
// decoders must read as encoding/json does: not valid JSON; and for each
// member, its key escaped or capitalized, its value a number or null or a
// string with an escape, and the member repeated with another value.
func variantsOf(b []byte) [][]byte {
	it, ok := objectMembers(b)
	if !ok {
		return nil
	}
	// at returns the offset in b of part, a slice of b.
	at := func(part []byte) int { return cap(b) - cap(part) }
	edit := func(start, end int, s string) []byte {
		return append(append(append([]byte{}, b[:start]...), s...), b[end:]...)
	}
	body := bytes.TrimRight(b, " \t\r\n")
	body = body[:len(body)-1] // without the closing brace
	out := [][]byte{append([]byte{}, body...), append(append([]byte{}, b...), '}')}
	for it.next() {
		key, value := it.key, it.value
		k, v := at(key), at(value)
		if len(key) > 0 {
			out = append(out, edit(k, k+1, fmt.Sprintf(`\u%04x`, key[0])))
			if 'a' <= key[0] && key[0] <= 'z' {
				out = append(out, edit(k, k+1, string(key[0]-'a'+'A')))
			}
		}
		out = append(out,
			edit(v, v+len(value), `1`),
			edit(v, v+len(value), `null`),
			append(append([]byte{}, body...), `,"`+string(key)+`":"other"}`...),
			append(append([]byte{}, body...), `,"`+string(key)+`":null}`...),
		)
		if s, plain := plainString(value); plain && len(s) > 0 {
			out = append(out, edit(v+1, v+2, fmt.Sprintf(`\u%04x`, s[0])))
		}
	}
	return out
}

// checkUnion decodes b into the union name both ways, scanning objects and
// decoding them into maps, and fails unless both choose the same variant,
// decode the same value, and return the same error.
func checkUnion(t *testing.T, name string, b []byte) {
	t.Helper()
	decode := func(byMap bool) (json.Unmarshaler, error) {
		unionsByMap = byMap
		defer func() { unionsByMap = false }()
		v := unionTypes[name]()
		return v, v.UnmarshalJSON(b)
	}
	got, gotErr := decode(false)
	want, wantErr := decode(true)
	if fmt.Sprintf("%T %v", gotErr, gotErr) != fmt.Sprintf("%T %v", wantErr, wantErr) {
		t.Fatalf("%s %q: error %T %v, want %T %v", name, b, gotErr, gotErr, wantErr, wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s %q: decodes into %+v, want %+v", name, b, got, want)
	}
	gotJSON, gotErr := json.Marshal(got)
	wantJSON, wantErr := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
		t.Fatalf("%s %q: encodes as %s (%v), want %s (%v)", name, b, gotJSON, gotErr, wantJSON, wantErr)
	}
}

// unionSamples returns the inputs that decode into each union, one of each
// shape: the keys of an object, or the value.
func unionSamples(inputs [][]byte, names []string) map[string][][]byte {
	samples := map[string][][]byte{}
	shapes := map[string]bool{}
	for _, b := range inputs {
		for _, name := range names {
			if unionTypes[name]().UnmarshalJSON(b) != nil {
				continue
			}
			shape := name + " " + string(b)
			if it, ok := objectMembers(b); ok {
				shape = name + " {"
				for it.next() {
					shape += string(it.key) + ","
				}
			}
			if !shapes[shape] {
				shapes[shape] = true
				samples[name] = append(samples[name], b)
			}
		}
	}
	return samples
}

// TestUnionObject_DecodesLikeAMap decodes every value of the corpus into
// every union, and changed copies of the objects that decode into one.
func TestUnionObject_DecodesLikeAMap(t *testing.T) {
	if obj, err := readUnionObject([]byte(`{"type":"text"}`)); err != nil || obj.b == nil {
		t.Fatalf("readUnionObject does not scan a plain object: %+v %v", obj, err)
	}
	names := unionNames()
	inputs := unionInputs(t)
	for _, b := range inputs {
		for _, name := range names {
			checkUnion(t, name, b)
		}
	}
	for name, samples := range unionSamples(inputs, names) {
		for _, b := range samples {
			for _, changed := range variantsOf(b) {
				checkUnion(t, name, changed)
			}
		}
	}
}

func FuzzUnionObject(f *testing.F) {
	names := unionNames()
	for i := range names {
		for _, s := range unionSeeds {
			f.Add(uint8(i), []byte(s))
		}
	}
	samples := unionSamples(unionInputs(f), names)
	for i, name := range names {
		for _, b := range samples[name] {
			f.Add(uint8(i), b)
		}
	}
	f.Fuzz(func(t *testing.T, union uint8, b []byte) {
		checkUnion(t, names[int(union)%len(names)], b)
	})
}
