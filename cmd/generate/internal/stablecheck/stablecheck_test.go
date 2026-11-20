package stablecheck

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// compare merges a stable and an unstable schema with the given definitions,
// which the PingRequest of the method ping reaches, and compares the code
// that the generator writes for the stable schema alone and for the merged
// one, with the hand-written files of the acp package.
func compare(t *testing.T, stable, unstable map[string]*load.Definition) error {
	t.Helper()
	meta := &load.Meta{Version: 1, AgentMethods: map[string]string{"ping": "ping"}}
	var schemas [2]*load.Schema
	for i, defs := range []map[string]*load.Definition{stable, unstable} {
		schema := &load.Schema{Defs: map[string]*load.Definition{}}
		props := map[string]*load.Definition{}
		for name, def := range defs {
			schema.Defs[name] = def
			props[strings.ToLower(name[:1])+name[1:]] = ref(name)
		}
		schema.Defs["PingRequest"] = &load.Definition{Type: "object", XMethod: "ping", XSide: "agent", Properties: props}
		schemas[i] = schema
	}
	mergedMeta, merged, err := load.MergeStableAndUnstable(meta, schemas[0], meta, schemas[1])
	if err != nil {
		t.Fatal(err)
	}
	return Compare(filepath.Join("..", "..", "..", ".."), "0.0.0", Run{meta, schemas[0]}, Run{mergedMeta, merged})
}

func ref(name string) *load.Definition { return &load.Definition{Ref: "#/$defs/" + name} }

func str() *load.Definition { return &load.Definition{Type: "string"} }

func obj(props map[string]*load.Definition, required ...string) *load.Definition {
	return &load.Definition{Type: "object", Properties: props, Required: required}
}

// variant is an object variant whose kind is a const.
func variant(kind string, props map[string]*load.Definition) *load.Definition {
	props["kind"] = &load.Definition{Type: "string", Const: kind}
	return obj(props, "kind")
}

// schema returns the definitions of a stable schema, and change edits them
// into those of an unstable one.
func schema(change func(defs map[string]*load.Definition)) map[string]*load.Definition {
	defs := map[string]*load.Definition{
		"Note": obj(map[string]*load.Definition{"text": str(), "pinned": {Type: "boolean"}}, "text", "pinned"),
		// The decoder checks a hinted value against the shapes table.
		"Hinted": obj(map[string]*load.Definition{
			"mode":  {Type: "string", Enum: []any{"ask", "auto"}, DefaultOnError: true},
			"kind":  {Type: "string", Const: "v1", DefaultOnError: true},
			"label": {Type: "string", Not: &load.Definition{Const: "none"}, DefaultOnError: true},
			"event": {AllOf: []*load.Definition{ref("Event")}, DefaultOnError: true},
			"size":  {AnyOf: []*load.Definition{{Title: "Name", Type: "string"}, {Title: "Count", Type: "integer"}, {Title: "Ratio", Type: "number"}}, DefaultOnError: true},
			// The decoder checks the range of a hinted integer.
			"count": {Type: "integer", Format: "uint32", Minimum: "0", DefaultOnError: true},
			// The rest rule does not apply to note, which takes any value.
			"labels": {
				Type: "object", Properties: map[string]*load.Definition{"note": {}},
				AdditionalProperties: load.Members{Schema: str()}, DefaultOnError: true,
			},
		}),
		// Event leaves the kinds of its other variants out of its catch-all.
		"Event": {AnyOf: []*load.Definition{
			variant("start", map[string]*load.Definition{}),
			{
				Title: "other", Type: "object", Properties: map[string]*load.Definition{"kind": str()}, Required: []string{"kind"},
				Not:                  &load.Definition{AnyOf: []*load.Definition{obj(map[string]*load.Definition{"kind": {Type: "string", Const: "start"}}, "kind")}},
				AdditionalProperties: load.Members{Open: true},
			},
		}},
		// Value tries its variants in order.
		"Value": {AnyOf: []*load.Definition{{Title: "String", Type: "string"}, {Title: "Integer", Type: "integer"}}},
		// Shape picks its variant by the const kind.
		"Shape": {OneOf: []*load.Definition{
			variant("circle", map[string]*load.Definition{"r": {Type: "number"}}),
			variant("square", map[string]*load.Definition{"side": {Type: "number"}}),
		}},
		"Level": {Type: "string", Enum: []any{"low", "high"}},
		"Count": {Type: "integer", Format: "uint16"},
		// Auth has one variant, which it picks by its required id.
		"Auth": {AnyOf: []*load.Definition{
			{Title: "agent", Type: "object", Properties: map[string]*load.Definition{"id": str()}, Required: []string{"id"}},
		}},
		// Unknown values of Tone decode as other.
		"Tone": {Type: "string", Enum: []any{"warm", "other"}},
		// Plain has no decoder of its own.
		"Plain": obj(map[string]*load.Definition{"mode": str()}),
		// The optional properties of a patch are patch fields.
		"Update": {
			Type: "object", Description: "Only changed fields are sent.",
			Properties: map[string]*load.Definition{"id": str(), "title": str()}, Required: []string{"id"},
		},
		"Flags": obj(map[string]*load.Definition{"read": {Type: "boolean"}, "list": {Type: "boolean"}}),
		"Caps": obj(map[string]*load.Definition{
			"flags": {AllOf: []*load.Definition{ref("Flags")}, Default: map[string]any{"read": false}, DefaultOnError: true},
			"name":  {Type: "string", Default: "a  b", DefaultOnError: true},
			"prefs": {AllOf: []*load.Definition{ref("Prefs")}, DefaultOnError: true},
		}),
		// The decoder of Caps checks no rule of Prefs: each of its members
		// may be null.
		"Prefs": obj(map[string]*load.Definition{"theme": {Type: []any{"string", "null"}}}),
		// MarshalJSON writes an empty list of Tags.
		"Tags":   obj(map[string]*load.Definition{"items": {Type: []any{"array", "null"}, Items: str()}}),
		"Limits": obj(map[string]*load.Definition{"max": {Type: []any{"integer", "null"}, Format: "int64"}}),
		// The Validate of Body calls the Validate of a variant type that has
		// one.
		"Body": {AnyOf: []*load.Definition{{Title: "Note", AllOf: []*load.Definition{ref("Note")}}, {Title: "Limits", AllOf: []*load.Definition{ref("Limits")}}}},
	}
	if change != nil {
		change(defs)
	}
	return defs
}

func TestCompare_AllowsAdditions(t *testing.T) {
	err := compare(t, schema(nil), schema(func(defs map[string]*load.Definition) {
		defs["Note"].Properties["lang"] = str()
		// Null went to no stable variant.
		defs["Value"].AnyOf = append(defs["Value"].AnyOf, &load.Definition{Title: "Number", Type: "number"}, &load.Definition{Type: "null"})
		defs["Hinted"].Properties["mode"].Enum = []any{"ask", "auto", "manual"}
		// A hinted union may gain an alternative of another kind.
		defs["Hinted"].Properties["size"].AnyOf = append(defs["Hinted"].Properties["size"].AnyOf, &load.Definition{Title: "Flag", Type: "boolean"})
		// A new kind gets a variant, and the catch-all leaves it out.
		other := defs["Event"].AnyOf[1]
		other.Not.AnyOf = append(other.Not.AnyOf, obj(map[string]*load.Definition{"kind": {Type: "string", Const: "stop"}}, "kind"))
		defs["Event"].AnyOf = []*load.Definition{defs["Event"].AnyOf[0], variant("stop", map[string]*load.Definition{}), other}
		// A variant with a kind of its own may go anywhere: the kind picks it.
		defs["Shape"].OneOf = []*load.Definition{defs["Shape"].OneOf[0], variant("triangle", map[string]*load.Definition{"base": {Type: "number"}}), defs["Shape"].OneOf[1]}
		defs["Level"].Enum = append(defs["Level"].Enum, "mid")
		// A default may gain members for the properties that the unstable
		// schema adds.
		defs["Flags"].Properties["write"] = &load.Definition{Type: "boolean"}
		defs["Caps"].Properties["flags"].Default = map[string]any{"read": false, "write": false}
		defs["Extra"] = obj(map[string]*load.Definition{"x": str()})
		// Variants with a kind of their own make kind the discriminator of
		// Auth, and the new switch on it takes only their kinds.
		defs["Auth"].AnyOf = append([]*load.Definition{
			variant("env_var", map[string]*load.Definition{"name": str()}),
			variant("terminal", map[string]*load.Definition{"command": str()}),
		}, defs["Auth"].AnyOf...)
		defs["Update"].Properties["name"] = str()
		// An enum with other decodes its values in one switch case.
		defs["Tone"].Enum = []any{"warm", "cool", "other"}
		// A decoder for a hint of an added property only handles it.
		defs["Plain"].Properties["level"] = &load.Definition{Type: "string", Default: "low", DefaultOnError: true}
		// The fields Alias and Update share their names with the local
		// type and the receiver type of the stable MarshalJSON of Update.
		defs["Update"].Properties["alias"] = str()
		defs["Update"].Properties["update"] = str()
		// A member that cannot be null makes the decoder of Caps check
		// Prefs, and the check takes every stable value.
		defs["Prefs"].Properties["color"] = str()
		// The catch-all keeps a new member out of its other members.
		defs["Event"].AnyOf[2].Properties["note"] = str()
		// The local type that mirrors Note gains a field.
		defs["Note"].Properties["mood"] = &load.Definition{Type: "string", Default: "calm", DefaultOnError: true}
		// MarshalJSON writes its fields on more lines.
		defs["Tags"].Properties["more"] = &load.Definition{Type: []any{"array", "null"}, Items: str()}
		// The Validate of Body calls a new Validate of Note, which only
		// checks the new member.
		defs["Note"].Properties["count"] = &load.Definition{Type: []any{"integer", "null"}, Format: "uint32", Minimum: "0"}
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompare_FailsOnChangesToStableTypes(t *testing.T) {
	cases := []struct {
		name   string
		change func(defs map[string]*load.Definition)
		want   string
	}{
		{
			name: "a variant goes before a stable one that is tried in order",
			change: func(defs map[string]*load.Definition) {
				alts := defs["Value"].AnyOf
				defs["Value"].AnyOf = []*load.Definition{alts[0], {Title: "Number", Type: "number"}, alts[1]}
			},
			want: "Value.UnmarshalJSON: tries the new Number before the stable Integer",
		},
		{
			// A const property of a definition that a variant refers to
			// becomes the union's discriminator, which names the variants.
			name: "a property of another definition renames a variant",
			change: func(defs map[string]*load.Definition) {
				defs["TextContents"].Properties["encoding"] = &load.Definition{Type: "string", Const: "utf8"}
			},
			want: "Resource.Text: removed",
		},
		{
			name: "a new definition takes the name of a nested type",
			change: func(defs map[string]*load.Definition) {
				defs["ShapeCircle"] = obj(map[string]*load.Definition{"hint": str()})
			},
			want: "Shape.Circle: changed from *ShapeCircle to *ShapeCircleInline",
		},
		{
			// The variants hold the union's own properties only while each
			// of them is an inline object.
			name: "an alternative moves the union's own properties to the wrapper",
			change: func(defs map[string]*load.Definition) {
				defs["Option"].AnyOf = append(defs["Option"].AnyOf, &load.Definition{Title: "raw", Type: "string"})
			},
			want: "OptionA.Id: removed",
		},
		{
			name: "an allOf part changes the Go type of a field",
			change: func(defs map[string]*load.Definition) {
				defs["Note"].Properties["text"].AllOf = []*load.Definition{ref("Flags")}
			},
			want: "Note.Text: changed from string to",
		},
		{
			name: "a format widens the Go type of a named type",
			change: func(defs map[string]*load.Definition) {
				defs["Count"].Format = "int64"
			},
			want: "Count: changed from int to int64",
		},
		{
			// apidiff does not read tags.
			name: "a stable member is no longer required",
			change: func(defs map[string]*load.Definition) {
				defs["Note"].Required = []string{"text"}
			},
			want: "Note.Pinned: changes its tag from `json:\"pinned\"` to `json:\"pinned,omitempty\"`",
		},
		{
			name: "a hinted property loses an enum value",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["mode"].Enum = []any{"ask"}
			},
			want: `shapes[shapeHintedMode]: loses the value "auto"`,
		},
		{
			name: "a hinted union loses the kind of an alternative",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["size"].AnyOf[2] = &load.Definition{Title: "Flag", Type: "boolean"}
			},
			want: "shapes[shapeHintedSize]: takes the kinds [kindBoolean kindInteger kindString] instead of [kindFraction kindInteger kindString]",
		},
		{
			name: "a hinted property changes its const",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["kind"].Const = "v2"
			},
			want: `shapes[shapeHintedKind]: loses the value "v1"`,
		},
		{
			name: "a hinted property changes its not rule",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["label"].Not = &load.Definition{Const: "nothing"}
			},
			want: `shapes[shapeHintedLabel].not: takes values that the stable rule does not`,
		},
		{
			// A value fits a rule only if it does not fit its not rule.
			name: "a hinted property's not rule takes more values",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["label"].Not = &load.Definition{Enum: []any{"none", "blocked"}}
			},
			want: `shapes[shapeHintedLabel].not: takes values that the stable rule does not`,
		},
		{
			name: "a hinted property with a not rule gains a const",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["label"].Const = "x"
			},
			want: `shapes[shapeHintedLabel]: takes only the values ["x"]`,
		},
		{
			name: "a hinted integer takes fewer values",
			change: func(defs map[string]*load.Definition) {
				defs["Hinted"].Properties["count"].Maximum = "100"
			},
			want: "shapes[shapeHintedCount]: takes the integers 0..100 instead of 0..4294967295",
		},
		{
			// The new variant takes the values of a kind that the stable
			// catch-all took.
			name: "a catch-all leaves out a kind that no variant takes",
			change: func(defs map[string]*load.Definition) {
				other := defs["Event"].AnyOf[1]
				other.Not.AnyOf = append(other.Not.AnyOf, obj(map[string]*load.Definition{"kind": {Type: "string", Const: "stop"}}, "kind"))
			},
			want: "shapes[shapeEvent].anyOf[1]: has no merged version",
		},
		{
			name: "a new check of a stable value has a rule for a stable member",
			change: func(defs map[string]*load.Definition) {
				defs["Prefs"].Properties["color"] = str()
				defs["Prefs"].Properties["theme"].Not = &load.Definition{Const: "none"}
			},
			want: "Caps.UnmarshalJSON: checks a stable value against the new shapePrefs, which has a rule for the stable member theme",
		},
		{
			name: "a new method that a stable method calls checks a stable member",
			change: func(defs map[string]*load.Definition) {
				defs["Limits"].Properties["max"].Maximum = "10"
			},
			want: "Limits.Validate: is new and refers to the stable Max",
		},
		{
			name: "a hint on a stable property gives a type a decoder",
			change: func(defs map[string]*load.Definition) {
				defs["Plain"].Properties["mode"] = &load.Definition{Type: "string", Default: "ask", DefaultOnError: true}
			},
			want: "Plain.UnmarshalJSON: is new and refers to the stable Mode",
		},
		{
			name: "a default gains a member for a stable property",
			change: func(defs map[string]*load.Definition) {
				defs["Caps"].Properties["flags"].Default = map[string]any{"read": false, "list": true}
			},
			want: "Caps.UnmarshalJSON: changes",
		},
		{
			name: "a string default changes its inner spacing",
			change: func(defs map[string]*load.Definition) {
				defs["Caps"].Properties["name"].Default = "a b"
			},
			want: "Caps.UnmarshalJSON: changes",
		},
	}
	resource := func(defs map[string]*load.Definition) {
		defs["Resource"] = &load.Definition{AnyOf: []*load.Definition{
			{Title: "Text", AllOf: []*load.Definition{ref("TextContents")}},
			{Title: "Blob", AllOf: []*load.Definition{ref("BlobContents")}},
		}}
		defs["TextContents"] = obj(map[string]*load.Definition{"text": str()}, "text")
		defs["BlobContents"] = obj(map[string]*load.Definition{"blob": str()}, "blob")
		defs["Option"] = &load.Definition{
			Type:       "object",
			Properties: map[string]*load.Definition{"id": str()},
			Required:   []string{"id"},
			AnyOf: []*load.Definition{
				variant("a", map[string]*load.Definition{"a": str()}),
				variant("b", map[string]*load.Definition{"b": str()}),
			},
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := compare(t, schema(resource), schema(func(defs map[string]*load.Definition) {
				resource(defs)
				tc.change(defs)
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one that says %q", err, tc.want)
			}
		})
	}
}
