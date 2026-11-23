package acp

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file compare the fast decoders (decode_fast.go) with the
// exact ones, which run alone when fastDecode is off: on the messages of the
// inbound benchmark corpus, on mutations of them, and on every generated type
// with a fast decoder.

// withFast runs f with the fast decoders on or off.
func withFast(on bool, f func()) {
	saved := fastDecode
	fastDecode = on
	defer func() { fastDecode = saved }()
	f()
}

// corpusOutcome decodes m as TestInboundCorpus does, and returns the outcome
// there, with the encoded value in place of its hash, and the value itself.
func corpusOutcome(p *benchPeers, m *inboundMessage) (string, any) {
	r := recorder{on: true}
	p.agentImpl.recorder, p.clientImpl.recorder = recorder{on: true}, recorder{on: true}
	err := p.decode(context.Background(), m, &r)
	for _, side := range []*recorder{&p.agentImpl.recorder, &p.clientImpl.recorder} {
		if side.got != nil {
			r.got = side.got
		}
	}
	switch {
	case err != nil:
		return "error " + err.Error(), nil
	case r.got == nil:
		return "ok", nil
	}
	enc, err := json.Marshal(r.got)
	if err != nil {
		return "encode error " + err.Error(), r.got
	}
	return "ok " + string(enc), r.got
}

// compareMessage requires the same outcome for m with the fast decoders on
// and off, and returns the first type whose fast decoder declined, if any.
func compareMessage(t *testing.T, p *benchPeers, m *inboundMessage) (outcome, missed string) {
	var fast, exact string
	var fastValue, exactValue any
	fastMissed = func(typ reflect.Type) {
		if missed == "" {
			missed = typ.String()
		}
	}
	withFast(true, func() { fast, fastValue = corpusOutcome(p, m) })
	fastMissed = nil
	withFast(false, func() { exact, exactValue = corpusOutcome(p, m) })
	if fast != exact || !reflect.DeepEqual(fastValue, exactValue) {
		t.Errorf("%s %s: the fast decoders read\n\t%.300s\nwhere the exact ones read\n\t%.300s", m.where, m.method, fast, exact)
	}
	return exact, missed
}

// TestFastDecode_Corpus reads every message of the corpus with the fast
// decoders on and off, and logs how many messages a fast decoder declined, by
// the first type that declined.
func TestFastDecode_Corpus(t *testing.T) {
	corpus := loadInboundCorpus(t)
	p := newBenchPeers()
	defer p.closeInputs()
	byType := map[string]int{}
	var missed, failed int
	for i := range corpus {
		outcome, typ := compareMessage(t, p, &corpus[i])
		switch {
		case typ == "":
		case strings.HasPrefix(outcome, "error"):
			// The exact decoder makes the error, as it should.
			failed++
		default:
			missed++
			byType[typ]++
		}
	}
	t.Logf("%d messages: the fast decoders declined %d that decode and %d that fail; first decliners of the ones that decode: %v", len(corpus), missed, failed, byType)
}

func FuzzFastDecode_Corpus(f *testing.F) {
	for _, m := range loadInboundCorpus(f) {
		f.Add(m.line, m.toAgent, m.method)
	}
	p := newBenchPeers()
	f.Cleanup(p.closeInputs)
	f.Fuzz(func(t *testing.T, line []byte, toAgent bool, method string) {
		compareMessage(t, p, &inboundMessage{where: "fuzz", line: line, toAgent: toAgent, method: method})
	})
}

func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// fastTypes are the types with a fast decoder: Meta, whose decodeFast is in
// decode_fast.go, and the generated ones (TestFastDecode_TypesListed).
var fastTypes = append([]reflect.Type{typeOf[Meta]()}, generatedFastTypes...)

// TestFastDecode_TypesListed checks generatedFastTypes and the fast decoders
// against a set that the emitter of the fast decoders does not write: the
// types that types_gen.go declares. Each of them gets a decodeFast (fast.go)
// but an alias, which keeps the methods of json.RawMessage, and an interface
// (Agent, Client and the optional method interfaces). So a generator that
// drops a fast decoder together with its list entry still fails here.
func TestFastDecode_TypesListed(t *testing.T) {
	parse := func(name string) *ast.File {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	want := map[string]bool{}
	for _, decl := range parse("types_gen.go").Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.TYPE {
			for _, spec := range gen.Specs {
				ts := spec.(*ast.TypeSpec)
				if _, iface := ts.Type.(*ast.InterfaceType); ts.Assign == 0 && !iface {
					want[ts.Name.Name] = true
				}
			}
		}
	}
	fast := map[string]bool{}
	for _, decl := range parse("decode_fast_gen.go").Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Name.Name == "decodeFast" {
			fast[fn.Recv.List[0].Type.(*ast.StarExpr).X.(*ast.Ident).Name] = true
		}
	}
	listed := map[string]bool{}
	for _, typ := range generatedFastTypes {
		listed[typ.Name()] = true
	}
	for name := range want {
		if !fast[name] {
			t.Errorf("types_gen.go declares %s, which decode_fast_gen.go gives no decodeFast", name)
		}
		if !listed[name] {
			t.Errorf("generatedFastTypes lacks %s", name)
		}
	}
	for name := range fast {
		if !want[name] {
			t.Errorf("decode_fast_gen.go gives %s a decodeFast, but types_gen.go declares no such type", name)
		}
	}
	for name := range listed {
		if !want[name] {
			t.Errorf("generatedFastTypes lists %s, which is no type of types_gen.go with a fast decoder", name)
		}
	}
}

// compareType decodes data into a fresh value of typ with the fast decoders
// on and off, and requires the same value and error. When data is a value
// that a fast decoder takes, it also calls the decodeFast of typ directly: on
// true, the exact decoder must decode data to the same value.
func compareType(t *testing.T, typ reflect.Type, data []byte) {
	decode := func(on bool) (any, string) {
		v := reflect.New(typ)
		var err error
		withFast(on, func() { err = json.Unmarshal(data, v.Interface()) })
		if err != nil {
			return v.Elem().Interface(), err.Error()
		}
		return v.Elem().Interface(), ""
	}
	fastValue, fastErr := decode(true)
	exactValue, exactErr := decode(false)
	if fastErr != exactErr || !reflect.DeepEqual(fastValue, exactValue) {
		t.Errorf("%v of %q: the fast decoders read %#v (error %q), the exact ones %#v (error %q)", typ, data, fastValue, fastErr, exactValue, exactErr)
	}
	trimmed := len(data) > 0 && !isSpace(data[0]) && !isSpace(data[len(data)-1])
	if !trimmed || !json.Valid(data) {
		return
	}
	v := reflect.New(typ)
	if !v.Interface().(interface{ decodeFast([]byte) bool }).decodeFast(data) {
		return
	}
	if exactErr != "" || !reflect.DeepEqual(v.Elem().Interface(), exactValue) {
		t.Errorf("%v.decodeFast took %q as %#v, the exact decoder reads %#v (error %q)", typ, data, v.Elem().Interface(), exactValue, exactErr)
	}
}

// typeSeeds are values that the fast decoders must decline or read exactly.
var typeSeeds = []string{
	`null`, `{}`, `[]`, `""`, `0`, `true`, `{"type":"text","text":"hi"}`,
	`{"type":"text","text":"a\nb\u00e9\ud83d\ude00\ud800x"}`, "{\"type\":\"text\",\"text\":\"\xff\xfe\"}",
	`{"type":"text","type":"image","text":"x"}`, `{"Type":"text","text":"x"}`, `{"\u0074ype":"text","text":"x"}`,
	`{"type":"text","text":"x","TEXT":"y"}`, "{\"type\":\"text\",\"text\":\"x\",\"\u212aind\":1}",
	`{"type":"text","text":"x","extra":1,"extra":2}`, `{"type":"text","text":null}`, `{"type":"text"}`,
	`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}}`,
	`{"sessionId":"s","update":null}`, `{"sessionId":null,"update":{"sessionUpdate":"plan","entries":[]}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"t","status":"bogus","content":[null,{"type":"diff"}],"title":null}`,
	`{"sessionUpdate":"tool_call","toolCallId":"t","title":"x","kind":7,"locations":[{"path":"/a","line":-1}]}`,
	`{"_meta":{"a":1,"a":2}}`, `{"_meta":{"\u00e9":1}}`, `{"_meta":[]}`, `{"_meta":null}`,
	`[{"name":"a","value":"b"}]`, `[{"group":"g","name":"n","options":[]}]`, `[null]`, `[1]`,
	`{"hint":"h"}`, `{"hint":null}`, `{"id":"a","name":"b"}`, `{"type":"terminal","id":"a","name":"b"}`,
	`9223372036854775807`, `9223372036854775808`, `-9223372036854775808`, `1e2`, `1.5`, `-0`, `1e400`,
	`"\u0000"`, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"authMethods":[{"id":"x","name":"y"}]}`,
	"{ \"type\" : \"text\" ,\n\t\"text\" : \"x\" , \"_meta\" : { \"a\" : [ 1 , 2 ] } }",
	`{"aVeryLongKeyThatIsLongerThanAnyFieldNameOfTheSchema":1,"type":"text","text":"x"}`,
	`{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8,"i":9,"j":10,"k":11,"l":12,"m":13,"n":14,"o":15,"p":16,"q":17,"type":"text","text":"x"}`,
	// A property that repeats, which the exact decoders reject.
	`{"sessionId":"a","sessionId":"b","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}}`,
	`{"sessionId":null,"sessionId":"a","update":{"sessionUpdate":"plan","entries":[]}}`,
	`{"type":"text","text":"x","t\u0065xt":"y"}`, `{"type":"text","t\u0079pe":"text","text":"x"}`,
	`{"_meta":{},"_meta":{},"type":"text","text":"x"}`, `{"code":1,"code":2,"message":"m","x":1,"x":2}`,
	`{"mode":"form","message":"a","message":"b","sessionId":"s","requestedSchema":{"type":"object","properties":{}}}`,
	`{"state":"custom","state":"other","x":1,"x":2}`, `{"action":"accept","action":"custom","content":{"a":"x","a":"y"}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"t","locations":[{"path":"/a","path":"/b"}],"title":"x","title":"y"}`,
}

func FuzzFastDecode_Types(f *testing.F) {
	for _, s := range typeSeeds {
		f.Add([]byte(s), uint16(0))
	}
	// The params and results of the corpus, with the type that they decode to.
	index := map[reflect.Type]int{}
	for i, typ := range fastTypes {
		index[typ] = i
	}
	p := newBenchPeers()
	for _, m := range loadInboundCorpus(f) {
		var msg anyMessage
		if json.Unmarshal(m.line, &msg) != nil {
			continue
		}
		_, value := corpusOutcome(p, &m)
		if i, ok := index[reflect.TypeOf(value)]; ok {
			f.Add([]byte(msg.Params), uint16(i))
			f.Add([]byte(msg.Result), uint16(i))
		}
	}
	p.closeInputs()
	f.Fuzz(func(t *testing.T, data []byte, pick uint16) {
		compareType(t, fastTypes[int(pick)%len(fastTypes)], data)
		for _, typ := range generatedUnions {
			compareType(t, typ, data)
		}
	})
}

// TestFastDecode_Types runs the seeds of FuzzFastDecode_Types against every
// type with a fast decoder.
func TestFastDecode_Types(t *testing.T) {
	for _, s := range typeSeeds {
		for _, typ := range fastTypes {
			compareType(t, typ, []byte(s))
		}
	}
}

// TestFastDecode_ReusedValues decodes each params or result of the corpus
// into a value that holds the one before it of the same type, as a caller
// that reuses a value does, with the fast decoders on and off.
func TestFastDecode_ReusedValues(t *testing.T) {
	p := newBenchPeers()
	defer p.closeInputs()
	last := map[reflect.Type][]byte{}
	for _, m := range loadInboundCorpus(t) {
		var msg anyMessage
		if json.Unmarshal(m.line, &msg) != nil {
			continue
		}
		_, value := corpusOutcome(p, &m)
		typ := reflect.TypeOf(value)
		data := msg.Params
		if data == nil {
			data = msg.Result
		}
		if typ == nil || data == nil {
			continue
		}
		if before, ok := last[typ]; ok {
			decode := func(on bool) (any, string) {
				v := reflect.New(typ)
				var err error
				withFast(on, func() {
					json.Unmarshal(before, v.Interface())
					err = json.Unmarshal(data, v.Interface())
				})
				if err != nil {
					return v.Elem().Interface(), err.Error()
				}
				return v.Elem().Interface(), ""
			}
			fastValue, fastErr := decode(true)
			exactValue, exactErr := decode(false)
			if fastErr != exactErr || !reflect.DeepEqual(fastValue, exactValue) {
				t.Errorf("%s: %v reused: the fast decoders read %#v (error %q), the exact ones %#v (error %q)", m.where, typ, fastValue, fastErr, exactValue, exactErr)
			}
		}
		last[typ] = data
	}
}

// FuzzFastDecode_Leaves compares the leaf decoders with encoding/json.
func FuzzFastDecode_Leaves(f *testing.F) {
	for _, s := range typeSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || isSpace(data[0]) || isSpace(data[len(data)-1]) || !json.Valid(data) {
			return
		}
		compareLeaf(t, data, fastString[string])
		compareLeaf(t, data, fastInt[int64])
		compareLeaf(t, data, fastInt[int])
		compareLeaf(t, data, fastFloat[float64])
		compareLeaf(t, data, fastBool[bool])
		compareLeaf(t, data, fastRaw)
		compareLeaf(t, data, (*Meta).decodeFast)
		compareLeaf(t, data, func(d *[]string, b []byte) bool { return fastSlice(d, b, fastString[string]) })
		compareLeaf(t, data, func(d *map[string]string, b []byte) bool { return fastMap(d, b, fastString[string]) })
		compareLeaf(t, data, func(d **int64, b []byte) bool { return fastPtr(d, b, fastInt[int64]) })
		compareLeaf(t, data, func(d *Patch[[]string], b []byte) bool {
			return fastPatch(d, b, func(d *[]string, b []byte) bool { return fastSlice(d, b, fastString[string]) })
		})
	})
}

// objectKey matches the end of an object key and its colon.
var objectKey = regexp.MustCompile(`"\s*:`)

// compareLeaf requires that dec, on true, reads data as json.Unmarshal does
// into a zero T, and that it declines only where json.Unmarshal fails, for
// the leaves whose decoders decline nothing else.
func compareLeaf[T any](t *testing.T, data []byte, dec func(*T, []byte) bool) {
	var fast, exact T
	ok := dec(&fast, data)
	err := json.Unmarshal(data, &exact)
	if ok && (err != nil || !reflect.DeepEqual(fast, exact)) {
		t.Errorf("%T of %q: the fast decoder reads %#v, json.Unmarshal %#v (error %v)", fast, data, fast, exact, err)
	}
	// A map decoder declines a key that repeats, so any value with a key may
	// decline; whitespace can come between a key and its colon.
	if !ok && err == nil && !objectKey.Match(data) {
		t.Errorf("%T of %q: the fast decoder declines what json.Unmarshal reads as %#v", fast, data, exact)
	}
}

// The slice helpers read the arrays of the corpus, and arrays longer than
// the buffer that fastSlice and fastItems keep their items in, as their exact
// counterparts do.

// sliceSeeds are values that the slice helpers must read exactly or decline,
// with arrays longer than their buffer among them.
func sliceSeeds() []string {
	seeds := []string{
		`[]`, `null`, `[null]`, `[null,null]`, `"x"`, `{}`, `1`, `[1]`, `["a",null,"b"]`, `[1,"a",{},"b"]`,
		`[{"type":"diff","path":"/a","newText":"x"},null,7,{"type":"content","content":{"type":"text","text":"x"}},{"type":"terminal","terminalId":"t"}]`,
		`[{"name":"a","description":"b"},{"name":1},{"name":"c","description":"d","input":{"hint":"h"}}]`,
		`[{"value":"a","name":"b"},{"value":"c","name":"d","description":null}]`,
		`[{"type":"text","text":"a"},{"type":"image","data":"x","mimeType":"image/png"}]`,
	}
	for _, n := range []int{40, 70} {
		strs := make([]string, n)
		objects := make([]string, n)
		for i := range strs {
			strs[i] = strconv.Quote(strconv.Itoa(i))
			objects[i] = `{"type":"text","text":` + strs[i] + `}`
			if i%7 == 3 {
				strs[i], objects[i] = "null", "1"
			}
		}
		seeds = append(seeds, "["+strings.Join(strs, ",")+"]", "["+strings.Join(objects, ",")+"]")
	}
	return seeds
}

// corpusArrays returns every array of the corpus that is a member value, by
// the key path of its member, as "params.update.content".
func corpusArrays(tb testing.TB) map[string][][]byte {
	arrays := map[string][][]byte{}
	var walk func(b []byte, path string)
	walk = func(b []byte, path string) {
		switch b[0] {
		case '{':
			it, _ := objectMembers(b)
			for it.next() {
				p := path + "." + string(it.key)
				if it.value[0] == '[' {
					arrays[p] = append(arrays[p], it.value)
				}
				walk(it.value, p)
			}
		case '[':
			it, _ := arrayItems(b)
			for it.next() {
				walk(it.item, path)
			}
		}
	}
	for _, m := range loadInboundCorpus(tb) {
		if line := trimValue(m.line); json.Valid(line) {
			walk(line, "")
		}
	}
	return arrays
}

// compareItems requires that fastItems reads data as decodeItems does.
func compareItems[T any](t *testing.T, data []byte, dec func(*T, []byte) bool, check ...shapeID) {
	var fast, exact []T
	decoded, ok := fastItems(data, &fast, dec, check...)
	var exactDecoded bool
	var err error
	withFast(false, func() { exactDecoded, err = decodeItems(data, &exact, check...) })
	if ok != (err == nil) || decoded != exactDecoded || !reflect.DeepEqual(fast, exact) {
		t.Errorf("fastItems of %q into %T: %v, %v, %#v; decodeItems: %v, %v, %#v", data, fast, decoded, ok, fast, exactDecoded, err, exact)
	}
}

// compareSlice requires that fastSlice, on true, reads data as json.Unmarshal
// does.
func compareSlice[T any](t *testing.T, data []byte, dec func(*T, []byte) bool) {
	var fast, exact []T
	ok := fastSlice(&fast, data, dec)
	var err error
	withFast(false, func() { err = json.Unmarshal(data, &exact) })
	if ok && (err != nil || !reflect.DeepEqual(fast, exact)) {
		t.Errorf("fastSlice of %q into %T: %#v; json.Unmarshal: %#v (error %v)", data, fast, fast, exact, err)
	}
}

// compareSlices compares the slice helpers with their exact counterparts on
// data, when encoding/json passes data to UnmarshalJSON as it is.
func compareSlices(t *testing.T, data []byte) {
	if len(data) == 0 || isSpace(data[0]) || isSpace(data[len(data)-1]) || !json.Valid(data) {
		return
	}
	compareItems(t, data, fastString[string])
	compareItems(t, data, (*ToolCallContent).decodeFast, shapeToolCallContent)
	compareItems(t, data, (*AvailableCommand).decodeFast, shapeAvailableCommand)
	compareItems(t, data, (*SessionConfigOption).decodeFast, shapeSessionConfigOption)
	compareSlice(t, data, fastString[string])
	compareSlice(t, data, (*SessionConfigSelectOption).decodeFast)
	compareSlice(t, data, (*ContentBlock).decodeFast)
}

func TestFastDecode_Slices(t *testing.T) {
	for _, s := range sliceSeeds() {
		compareSlices(t, []byte(s))
	}
	for _, arrays := range corpusArrays(t) {
		for _, a := range arrays {
			compareSlices(t, a)
		}
	}
}

func FuzzFastDecode_Slices(f *testing.F) {
	for _, s := range sliceSeeds() {
		f.Add([]byte(s))
	}
	for _, arrays := range corpusArrays(f) {
		for _, a := range arrays {
			f.Add(a)
		}
	}
	f.Fuzz(compareSlices)
}

// sliceBenchmarks are the slice helpers that read the most items of the
// corpus, each with the end of the key path of the arrays it reads there.
var sliceBenchmarks = []struct {
	name, path string
	decode     func([]byte) bool
}{
	{"AvailableCommand", ".availableCommands", func(b []byte) bool {
		var v []AvailableCommand
		return fastLenientItems(b, &v, (*AvailableCommand).decodeFast, shapeAvailableCommand)
	}},
	{"SessionConfigOption", ".configOptions", func(b []byte) bool {
		var v []SessionConfigOption
		return fastLenientItems(b, &v, (*SessionConfigOption).decodeFast, shapeSessionConfigOption)
	}},
	{"SessionConfigSelectOption", ".configOptions.options", func(b []byte) bool {
		var v []SessionConfigSelectOption
		return fastSlice(&v, b, (*SessionConfigSelectOption).decodeFast)
	}},
	{"SessionMode", ".availableModes", func(b []byte) bool {
		var v []SessionMode
		return fastLenientItems(b, &v, (*SessionMode).decodeFast, shapeSessionMode)
	}},
	{"ToolCallContent", ".update.content", func(b []byte) bool {
		var v []ToolCallContent
		return fastLenientItems(b, &v, (*ToolCallContent).decodeFast, shapeToolCallContent)
	}},
	{"PermissionOption", ".params.options", func(b []byte) bool {
		var v []PermissionOption
		return fastSlice(&v, b, (*PermissionOption).decodeFast)
	}},
	{"ContentBlock", ".params.prompt", func(b []byte) bool {
		var v []ContentBlock
		return fastSlice(&v, b, (*ContentBlock).decodeFast)
	}},
}

// BenchmarkFastSlices reads, with each of sliceBenchmarks, the arrays of the
// corpus that it reads there. ns/op is the time to read them all once.
func BenchmarkFastSlices(b *testing.B) {
	byPath := corpusArrays(b)
	for _, c := range sliceBenchmarks {
		var arrays [][]byte
		for path, a := range byPath {
			if strings.HasSuffix(path, c.path) {
				arrays = append(arrays, a...)
			}
		}
		b.Run(c.name, func(b *testing.B) {
			if len(arrays) == 0 {
				b.Fatal("no arrays")
			}
			b.ReportAllocs()
			declined := 0
			for i := 0; i < b.N; i++ {
				declined = 0
				for _, a := range arrays {
					if !c.decode(a) {
						declined++
					}
				}
			}
			b.ReportMetric(float64(len(arrays)), "arrays/op")
			b.ReportMetric(float64(declined), "declined/op")
		})
	}
}
