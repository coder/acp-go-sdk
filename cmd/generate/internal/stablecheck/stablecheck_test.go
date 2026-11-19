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
		"Note": obj(map[string]*load.Definition{"text": str()}, "text"),
		// Value tries its variants in order.
		"Value": {AnyOf: []*load.Definition{{Title: "String", Type: "string"}, {Title: "Integer", Type: "integer"}}},
		// Shape picks its variant by the const kind.
		"Shape": {OneOf: []*load.Definition{
			variant("circle", map[string]*load.Definition{"r": {Type: "number"}}),
			variant("square", map[string]*load.Definition{"side": {Type: "number"}}),
		}},
		"Level": {Type: "string", Enum: []any{"low", "high"}},
		"Flags": obj(map[string]*load.Definition{"read": {Type: "boolean"}, "list": {Type: "boolean"}}),
		"Caps": obj(map[string]*load.Definition{
			"flags": {AllOf: []*load.Definition{ref("Flags")}, Default: map[string]any{"read": false}, DefaultOnError: true},
		}),
	}
	if change != nil {
		change(defs)
	}
	return defs
}

func TestCompare_AllowsAdditions(t *testing.T) {
	err := compare(t, schema(nil), schema(func(defs map[string]*load.Definition) {
		defs["Note"].Properties["lang"] = str()
		defs["Value"].AnyOf = append(defs["Value"].AnyOf, &load.Definition{Title: "Number", Type: "number"})
		// A variant with a kind of its own may go anywhere: the kind picks it.
		defs["Shape"].OneOf = []*load.Definition{defs["Shape"].OneOf[0], variant("triangle", map[string]*load.Definition{"base": {Type: "number"}}), defs["Shape"].OneOf[1]}
		defs["Level"].Enum = append(defs["Level"].Enum, "mid")
		// A default may gain members for the properties that the unstable
		// schema adds.
		defs["Flags"].Properties["write"] = &load.Definition{Type: "boolean"}
		defs["Caps"].Properties["flags"].Default = map[string]any{"read": false, "write": false}
		defs["Extra"] = obj(map[string]*load.Definition{"x": str()})
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
			name: "a default gains a member for a stable property",
			change: func(defs map[string]*load.Definition) {
				defs["Caps"].Properties["flags"].Default = map[string]any{"read": false, "list": true}
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
