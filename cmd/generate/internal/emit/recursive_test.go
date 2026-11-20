package emit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// recursiveSchema has types that hold themselves: Tree through a $ref, an
// allOf of one $ref and a lenient $ref; A and B each other, A through a
// required allOf; P, P2, P3 and Q in a ring, where only Q, which sorts last,
// has a check of its own; Dir through the struct of an inline property; and the
// union Shape through its own property. Go rejects a struct that holds
// itself in place, so each such member must hold its value through a
// pointer. Kin requires two members of its own type whose hints read a bad
// value or null as the default, which is nil. Update holds Change through a
// patch field, a Patch that holds
// its value in place, so the member of Change gets the pointer. The variant
// of Pick copies the members of A, so its constructor takes the required b
// as A declares it.
const recursiveSchema = `{"$defs": {
  "Tree": {"type": "object", "required": ["value"], "properties": {
    "value": {"type": "integer", "format": "int32", "minimum": 0},
    "left": {"$ref": "#/$defs/Tree"},
    "right": {"description": "The right subtree.", "allOf": [{"$ref": "#/$defs/Tree"}]},
    "middle": {"$ref": "#/$defs/Tree", "x-deserialize-default-on-error": true}}},
  "Kin": {"type": "object", "required": ["kid", "kin", "age"], "properties": {
    "age": {"type": "integer", "format": "int32", "minimum": 0},
    "kid": {"$ref": "#/$defs/Kin", "x-deserialize-default-on-error": true},
    "kin": {"$ref": "#/$defs/Kin", "x-deserialize-default-on-null": true}}},
  "A": {"type": "object", "required": ["b"], "properties": {"b": {"allOf": [{"$ref": "#/$defs/B"}]}}},
  "B": {"type": "object", "properties": {"a": {"$ref": "#/$defs/A"}}},
  "Update": {"type": "object", "description": "Only changed fields need to be included.",
    "properties": {"next": {"anyOf": [{"$ref": "#/$defs/Change"}, {"type": "null"}]}}},
  "Change": {"type": "object", "properties": {"update": {"$ref": "#/$defs/Update"}}},
  "Pick": {"oneOf": [
    {"type": "object", "allOf": [{"$ref": "#/$defs/A"}], "required": ["kind"], "properties": {"kind": {"type": "string", "const": "a"}}},
    {"type": "string"}]},
  "P": {"type": "object", "properties": {"next": {"$ref": "#/$defs/P2"}}},
  "P2": {"type": "object", "properties": {"next": {"$ref": "#/$defs/P3"}}},
  "P3": {"type": "object", "properties": {"q": {"$ref": "#/$defs/Q"}}},
  "Q": {"type": "object", "required": ["p"], "properties": {"p": {"$ref": "#/$defs/P"}}},
  "Dir": {"type": "object", "properties": {
    "meta": {"type": "object", "properties": {"parent": {"$ref": "#/$defs/Dir"}}}}},
  "Shape": {"properties": {"next": {"$ref": "#/$defs/Shape"}},
    "anyOf": [{"$ref": "#/$defs/Circle"}, {"$ref": "#/$defs/Square"}]},
  "Circle": {"type": "object", "required": ["r"], "properties": {"r": {"type": "number"}}},
  "Square": {"type": "object", "required": ["side"], "properties": {"side": {"type": "number"}}}
}}`

const recursiveTest = `package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func roundTrips(t *testing.T, v any, in string) {
	t.Helper()
	if err := json.Unmarshal([]byte(in), v); err != nil {
		t.Fatalf("unmarshal %s: %v", in, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal the value decoded from %s: %v", in, err)
	}
	var a, b any
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round trip of %s gives %s", in, out)
	}
}

func TestTree_HoldsSubtreesThroughPointers(t *testing.T) {
	var tree Tree
	roundTrips(t, &tree, ` + "`" + `{"value":1,"left":{"value":2},"right":{"value":3,"right":{"value":4}}}` + "`" + `)
	if tree.Left == nil || tree.Left.Value != 2 || tree.Left.Left != nil || tree.Right == nil || tree.Right.Right == nil || tree.Right.Right.Value != 4 {
		t.Fatalf("decoded %+v", tree)
	}
	if err := tree.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	tree.Right.Right.Value = -1
	if err := tree.Validate(); err == nil {
		t.Error("Validate takes a subtree whose value is below its minimum")
	}
	var lenient Tree
	if err := json.Unmarshal([]byte(` + "`" + `{"value":1,"middle":{"value":"x"}}` + "`" + `), &lenient); err != nil || lenient.Middle != nil {
		t.Errorf("a bad middle subtree reads as %+v, %v; want absent", lenient.Middle, err)
	}
}

func TestKin_TakesTheDefaultsThatItsHintsReadAsNil(t *testing.T) {
	var kin Kin
	if err := json.Unmarshal([]byte(` + "`" + `{"age":1,"kid":"bad","kin":null}` + "`" + `), &kin); err != nil || kin.Kid != nil || kin.Kin != nil {
		t.Fatalf("decoded %+v, %v", kin, err)
	}
	if err := kin.Validate(); err != nil {
		t.Errorf("Validate rejects the defaults that the hints read: %v", err)
	}
	if kin.Age = -1; kin.Validate() == nil {
		t.Error("Validate takes an age below its minimum")
	}
}

func TestA_RequiresBThroughAPointer(t *testing.T) {
	var a A
	roundTrips(t, &a, ` + "`" + `{"b":{"a":{"b":{}}}}` + "`" + `)
	if a.B == nil || a.B.A == nil || a.B.A.B == nil || a.B.A.B.A != nil {
		t.Fatalf("decoded %+v", a)
	}
	for _, in := range []string{` + "`" + `{}` + "`" + `, ` + "`" + `{"b":null}` + "`" + `} {
		if err := json.Unmarshal([]byte(in), &A{}); err == nil {
			t.Errorf("A decodes %s, which lacks the required b", in)
		}
	}
	if err := (&A{}).Validate(); err == nil {
		t.Error("Validate takes an A without b")
	}
	if err := (&A{B: &B{A: &A{}}}).Validate(); err == nil {
		t.Error("Validate takes an inner A without b")
	}
	if err := (&A{B: &B{}}).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestUpdate_HoldsChangeThroughAPatchAndChangeHoldsUpdateThroughAPointer(t *testing.T) {
	var update Update
	roundTrips(t, &update, ` + "`" + `{"next":{"update":{"next":null}}}` + "`" + `)
	change, ok := update.Next.Value()
	if !ok || change.Update == nil || !change.Update.Next.IsNull() {
		t.Fatalf("decoded %+v", update)
	}
}

func TestNewPickA_TakesTheRequiredBAsADeclaresIt(t *testing.T) {
	pick := NewPickA(&B{})
	out, err := json.Marshal(pick)
	if err != nil {
		t.Fatal(err)
	}
	var back Pick
	roundTrips(t, &back, string(out))
	if back.A == nil || back.A.B == nil || back.A.Kind != "a" {
		t.Errorf("NewPickA(&B{}) encodes as %s", out)
	}
}

func TestQ_ValidatesTheQAtTheEndOfItsRing(t *testing.T) {
	ring := func(inner *Q) *Q { return &Q{P: &P{Next: &P2{Next: &P3{Q: inner}}}} }
	if err := ring(&Q{}).Validate(); err == nil {
		t.Error("Validate takes an inner Q without p")
	}
	if err := (&P{Next: &P2{Next: &P3{Q: &Q{}}}}).Validate(); err == nil {
		t.Error("Validate takes a Q without p")
	}
	if err := ring(&Q{P: &P{}}).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestDir_HoldsItsParentThroughAPointer(t *testing.T) {
	var dir Dir
	roundTrips(t, &dir, ` + "`" + `{"meta":{"parent":{"meta":{}}}}` + "`" + `)
	if dir.Meta.Parent == nil || dir.Meta.Parent.Meta.Parent != nil {
		t.Fatalf("decoded %+v", dir)
	}
}

func TestShape_HoldsTheNextShapeThroughAPointer(t *testing.T) {
	var shape Shape
	roundTrips(t, &shape, ` + "`" + `{"r":1,"next":{"side":2}}` + "`" + `)
	if shape.Circle == nil || shape.Circle.R != 1 || shape.Next == nil || shape.Next.Square == nil || shape.Next.Square.Side != 2 {
		t.Fatalf("decoded %+v", shape)
	}
}
`

func TestWriteTypesJen_RecursiveMembersHoldTheirValuesThroughPointers(t *testing.T) {
	checkGenerated(t, recursiveSchema, map[string]string{"recursive_test.go": recursiveTest})
}

// threadedTest decodes real session/update params as SessionNotification and
// as ThreadedNotification, a copy with an optional parent of its own type,
// and requires the same result at each level.
const threadedTest = `package acp

import (
	"encoding/json"
	"os"
	"testing"
)

func TestThreadedNotification_DecodesRealUpdatesAndTheirParents(t *testing.T) {
	data, err := os.ReadFile("params.json")
	if err != nil {
		t.Fatal(err)
	}
	var params []json.RawMessage
	if err := json.Unmarshal(data, &params); err != nil {
		t.Fatal(err)
	}
	// original returns the JSON that SessionNotification encodes p as.
	original := func(p json.RawMessage) (string, bool) {
		var n SessionNotification
		if json.Unmarshal(p, &n) != nil || n.Validate() != nil {
			return "", false
		}
		out, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		return string(out), true
	}
	chained := 0
	var parent json.RawMessage
	for _, p := range params {
		want, ok := original(p)
		if !ok {
			continue
		}
		var lone ThreadedNotification
		if err := json.Unmarshal(p, &lone); err != nil || lone.Parent != nil {
			t.Fatalf("%s: decodes with %v and parent %v", p, err, lone.Parent)
		}
		if out, _ := json.Marshal(lone); string(out) != want {
			t.Errorf("%s: encodes as %s, want %s", p, out, want)
		}
		if parent != nil {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(p, &obj); err != nil {
				t.Fatal(err)
			}
			obj["parent"] = parent
			child, _ := json.Marshal(obj)
			var n ThreadedNotification
			if err := json.Unmarshal(child, &n); err != nil {
				t.Fatalf("%s: %v", child, err)
			}
			if err := n.Validate(); err != nil {
				t.Errorf("%s: Validate: %v", child, err)
			}
			if n.Parent == nil {
				t.Fatalf("%s: no parent", child)
			}
			wantParent, _ := original(parent)
			gotParent, _ := json.Marshal(*n.Parent)
			n.Parent = nil
			got, _ := json.Marshal(n)
			if string(got) != want || string(gotParent) != wantParent {
				t.Errorf("%s: decodes as %s with parent %s", child, got, gotParent)
			}
			chained++
		}
		parent = p
	}
	if chained == 0 {
		t.Error("no real update decodes")
	}
}
`

// TestWriteTypesJen_RecursiveCopyOfARealDefDecodesRealMessages generates
// schema 1.32.0 with ThreadedNotification, a copy of SessionNotification with
// an optional parent of its own type in the form that ACP gives a described
// member, an allOf of one $ref. The generated package decodes the
// session/update params of the G2 transcripts with it, alone and with the
// previous one as its parent.
func TestWriteTypesJen_RecursiveCopyOfARealDefDecodesRealMessages(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "..")
	data, err := os.ReadFile(filepath.Join(repo, "schema", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	var threaded map[string]any
	if err := json.Unmarshal(schema.Defs["SessionNotification"], &threaded); err != nil {
		t.Fatal(err)
	}
	threaded["properties"].(map[string]any)["parent"] = map[string]any{
		"description": "The notification that this one follows.",
		"allOf":       []any{map[string]any{"$ref": "#/$defs/ThreadedNotification"}},
	}
	if schema.Defs["ThreadedNotification"], err = json.Marshal(threaded); err != nil {
		t.Fatal(err)
	}
	fixture, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	var params []json.RawMessage
	transcripts, err := filepath.Glob(filepath.Join(repo, ".agents", "skills", "verify-schema-bump", "roundtrip", "testdata", "transcripts", "*.jsonl"))
	if err != nil || len(transcripts) == 0 {
		t.Fatalf("find the G2 transcripts: %v", err)
	}
	for _, path := range transcripts {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(f)
		lines.Buffer(nil, 16<<20)
		for lines.Scan() {
			var record struct {
				Msg struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				} `json:"msg"`
			}
			if err := json.Unmarshal(lines.Bytes(), &record); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if record.Msg.Method == "session/update" {
				params = append(params, record.Msg.Params)
			}
		}
		f.Close()
		if err := lines.Err(); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	checkGenerated(t, string(fixture), map[string]string{"threaded_test.go": threadedTest, "params.json": string(encoded)})
}
