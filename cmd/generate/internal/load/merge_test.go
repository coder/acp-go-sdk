package load

import (
	"strings"
	"testing"
)

func mustMerge(t *testing.T, stableMeta *Meta, stableSchema *Schema, unstableMeta *Meta, unstableSchema *Schema) (*Meta, *Schema) {
	t.Helper()
	meta, schema, err := MergeStableAndUnstable(stableMeta, stableSchema, unstableMeta, unstableSchema)
	if err != nil {
		t.Fatalf("MergeStableAndUnstable returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("MergeStableAndUnstable returned nil meta")
	}
	if schema == nil {
		t.Fatalf("MergeStableAndUnstable returned nil schema")
	}
	return meta, schema
}

func ref(defName string) *Definition {
	return &Definition{Ref: "#/$defs/" + defName}
}

func TestMergeStableAndUnstable(t *testing.T) {
	t.Run("unstable-only method adds unstable-prefixed root type", func(t *testing.T) {
		stableMeta := &Meta{Version: 1}
		stableSchema := &Schema{Defs: map[string]*Definition{}}

		unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"foo": "unstable/foo"}}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
			},
		}}

		combinedMeta, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if combinedMeta.AgentMethods["foo"] != "unstable/foo" {
			t.Fatalf("combined meta missing unstable method: got %q", combinedMeta.AgentMethods["foo"])
		}
		if combinedSchema.Defs["UnstableFooRequest"] == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if _, ok := combinedSchema.Defs["FooRequest"]; ok {
			t.Fatalf("did not expect FooRequest to be present in merged defs")
		}
		if got := combinedSchema.Defs["UnstableFooRequest"].XMethod; got != "unstable/foo" {
			t.Fatalf("UnstableFooRequest.XMethod mismatch: got %q", got)
		}
		if got := combinedSchema.Defs["UnstableFooRequest"].XSide; got != "agent" {
			t.Fatalf("UnstableFooRequest.XSide mismatch: got %q", got)
		}
	})

	t.Run("allows omitted unstable version", func(t *testing.T) {
		stableMeta := &Meta{Version: 2}
		stableSchema := &Schema{Defs: map[string]*Definition{}}

		unstableMeta := &Meta{AgentMethods: map[string]string{"foo": "unstable/foo"}}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
			},
		}}

		combinedMeta, _ := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if combinedMeta.Version != stableMeta.Version {
			t.Fatalf("expected merged version %d, got %d", stableMeta.Version, combinedMeta.Version)
		}
		if combinedMeta.AgentMethods["foo"] != "unstable/foo" {
			t.Fatalf("combined meta missing unstable method: got %q", combinedMeta.AgentMethods["foo"])
		}
	})

	t.Run("a changed shared definition keeps its name and an unstable-only one is copied", func(t *testing.T) {
		stableMeta := &Meta{Version: 1}
		stableSchema := &Schema{Defs: map[string]*Definition{
			"FooParams": {Description: "stable params", Type: "object"},
		}}

		unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"foo": "unstable/foo"}}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"params": ref("FooParams"),
				},
			},
			"FooParams":  {Description: "unstable params", Type: "object", Required: []string{"id"}, Properties: map[string]*Definition{"opts": ref("FooOptions")}},
			"FooOptions": {Type: "object"},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if combinedSchema.Defs["UnstableFooParams"] != nil {
			t.Fatalf("UnstableFooParams was added for a definition that both schemas have")
		}
		if got := combinedSchema.Defs["FooParams"].Required; len(got) != 1 || got[0] != "id" {
			t.Fatalf("FooParams is not the unstable version: required %v", got)
		}
		unstableReq := combinedSchema.Defs["UnstableFooRequest"]
		if unstableReq == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if unstableReq.Properties == nil || unstableReq.Properties["params"] == nil {
			t.Fatalf("expected UnstableFooRequest to have params property")
		}
		if got := unstableReq.Properties["params"].Ref; got != "#/$defs/FooParams" {
			t.Fatalf("expected params to refer to the shared FooParams; got %q", got)
		}
		// FooParams reaches FooOptions, which only the unstable schema has,
		// so it keeps its name too.
		if combinedSchema.Defs["FooOptions"] == nil || combinedSchema.Defs["UnstableFooOptions"] != nil {
			t.Fatalf("FooOptions: got %v, UnstableFooOptions %v; want FooOptions only", combinedSchema.Defs["FooOptions"], combinedSchema.Defs["UnstableFooOptions"])
		}
		// Ensure we didn't mutate the unstable input schema in-place.
		if got := unstableSchema.Defs["FooRequest"].Properties["params"].Ref; got != "#/$defs/FooParams" {
			t.Fatalf("expected unstable input schema refs to remain unchanged; got %q", got)
		}
	})

	t.Run("traverses unchanged intermediaries to reach changed descendants", func(t *testing.T) {
		stableMeta := &Meta{Version: 1}
		stableSchema := &Schema{Defs: map[string]*Definition{
			"Wrapper": {
				Type: "object",
				Properties: map[string]*Definition{
					"leaf": ref("Leaf"),
				},
			},
			"Leaf": {Description: "stable leaf", Type: "object"},
		}}

		unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"foo": "unstable/foo"}}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"wrapper": ref("Wrapper"),
				},
			},
			// Intentionally unchanged relative to stable.
			"Wrapper": {
				Type: "object",
				Properties: map[string]*Definition{
					"leaf": ref("Leaf"),
				},
			},
			// Changed descendant reachable only through unchanged Wrapper.
			"Leaf": {Description: "unstable leaf", Type: "object", Required: []string{"id"}},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		unstableReq := combinedSchema.Defs["UnstableFooRequest"]
		if unstableReq == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if got := unstableReq.Properties["wrapper"].Ref; got != "#/$defs/Wrapper" {
			t.Fatalf("expected wrapper to refer to the shared Wrapper; got %q", got)
		}
		if got := combinedSchema.Defs["Wrapper"].Properties["leaf"].Ref; got != "#/$defs/Leaf" {
			t.Fatalf("expected leaf to refer to the shared Leaf; got %q", got)
		}
		if got := combinedSchema.Defs["Leaf"].Required; len(got) != 1 {
			t.Fatalf("Leaf is not the unstable version: required %v", got)
		}
		for _, name := range []string{"UnstableWrapper", "UnstableLeaf"} {
			if combinedSchema.Defs[name] != nil {
				t.Fatalf("%s was added; want one type per shape", name)
			}
		}
	})

	t.Run("no duplication for identical referenced types", func(t *testing.T) {
		stableMeta := &Meta{Version: 1}
		stableSchema := &Schema{Defs: map[string]*Definition{
			"Shared": {Description: "shared", Type: "object"},
		}}

		unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"foo": "unstable/foo"}}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"shared": ref("Shared"),
				},
			},
			// Identical to stable; should not be duplicated.
			"Shared": {Description: "shared", Type: "object"},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if combinedSchema.Defs["UnstableFooRequest"] == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if combinedSchema.Defs["UnstableShared"] != nil {
			t.Fatalf("did not expect UnstableShared to be created for identical definition")
		}
		unstableReq := combinedSchema.Defs["UnstableFooRequest"]
		if unstableReq.Properties == nil || unstableReq.Properties["shared"] == nil {
			t.Fatalf("expected UnstableFooRequest to have shared property")
		}
		if got := unstableReq.Properties["shared"].Ref; got != "#/$defs/Shared" {
			t.Fatalf("expected shared ref to remain pointing at stable Shared; got %q", got)
		}
	})

	t.Run("a changed stable root that an unstable method reaches keeps its name and RPC markers", func(t *testing.T) {
		stableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"stableThing": "stable/method"}}
		stableSchema := &Schema{Defs: map[string]*Definition{
			"StableThingRequest": {
				Description: "stable StableThingRequest",
				Type:        "object",
				XMethod:     "stable/method",
				XSide:       "agent",
			},
		}}

		unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"foo": "unstable/foo"}}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"stable": ref("StableThingRequest"),
				},
			},
			// Changed relative to stable (a new property), so it will be duplicated.
			"StableThingRequest": {
				Description: "unstable StableThingRequest",
				Type:        "object",
				XMethod:     "stable/method",
				XSide:       "agent",
				Properties:  map[string]*Definition{"extra": {Type: "string"}},
			},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if combinedSchema.Defs["UnstableStableThingRequest"] != nil {
			t.Fatalf("UnstableStableThingRequest was added; want one type per shape")
		}
		shared := combinedSchema.Defs["StableThingRequest"]
		if shared.XMethod != "stable/method" || shared.XSide != "agent" {
			t.Fatalf("StableThingRequest lost its RPC markers: %q %q", shared.XMethod, shared.XSide)
		}
		if extra := shared.Properties["extra"]; extra == nil || !strings.HasPrefix(extra.Description, "**UNSTABLE**") {
			t.Fatalf("StableThingRequest.extra = %+v, want the unstable property marked UNSTABLE", extra)
		}
		// The stable definition should keep its RPC markers.
		if got := stableSchema.Defs["StableThingRequest"].XMethod; got != "stable/method" {
			t.Fatalf("expected stable StableThingRequest to retain XMethod; got %q", got)
		}

		unstableReq := combinedSchema.Defs["UnstableFooRequest"]
		if unstableReq == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if got := unstableReq.Properties["stable"].Ref; got != "#/$defs/StableThingRequest" {
			t.Fatalf("expected the ref to stay StableThingRequest; got %q", got)
		}
	})

	t.Run("promotes changed shared defs and pulls in new dependencies", func(t *testing.T) {
		stableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"session_new": "session/new"}}
		stableSchema := &Schema{Defs: map[string]*Definition{
			"NewSessionResponse": {
				Type:    "object",
				XMethod: "session/new",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"sessionId": {Type: "string"},
				},
			},
		}}

		unstableMeta := &Meta{Version: 1}
		unstableSchema := &Schema{Defs: map[string]*Definition{
			"NewSessionResponse": {
				Type:    "object",
				XMethod: "session/new",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"sessionId": {Type: "string"},
					"models":    ref("SessionModelState"),
				},
			},
			"SessionModelState": {
				Type: "object",
				Properties: map[string]*Definition{
					"currentModelId": {Type: "string"},
				},
			},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		merged := combinedSchema.Defs["NewSessionResponse"]
		if merged == nil {
			t.Fatalf("expected NewSessionResponse to exist")
		}
		if merged.Properties == nil || merged.Properties["models"] == nil {
			t.Fatalf("expected promoted NewSessionResponse to contain models")
		}
		if got := merged.Properties["models"].Ref; got != "#/$defs/SessionModelState" {
			t.Fatalf("expected models ref to SessionModelState; got %q", got)
		}
		if combinedSchema.Defs["SessionModelState"] == nil {
			t.Fatalf("expected SessionModelState dependency to be copied from unstable")
		}
	})

	t.Run("stable defs are not mutated", func(t *testing.T) {
		stableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"stable": "stable/method"}}
		unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"foo": "unstable/foo"}}

		stableParams := &Definition{Description: "stable params", Type: "object"}
		stableReq := &Definition{
			Type:    "object",
			XMethod: "stable/method",
			XSide:   "agent",
			Properties: map[string]*Definition{
				"params": ref("FooParams"),
			},
		}
		stableSchema := &Schema{Defs: map[string]*Definition{
			"FooParams":     stableParams,
			"StableRequest": stableReq,
		}}

		unstableSchema := &Schema{Defs: map[string]*Definition{
			"FooRequest": {
				Type:    "object",
				XMethod: "unstable/foo",
				XSide:   "agent",
				Properties: map[string]*Definition{
					"params": ref("FooParams"),
				},
			},
			"FooParams": {Description: "unstable params", Type: "object", Required: []string{"id"}},
		}}

		_, _ = mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if got := stableSchema.Defs["FooParams"].Description; got != "stable params" {
			t.Fatalf("expected stable FooParams description to remain unchanged; got %q", got)
		}
		if got := stableSchema.Defs["StableRequest"].Properties["params"].Ref; got != "#/$defs/FooParams" {
			t.Fatalf("expected stable StableRequest refs to remain unchanged; got %q", got)
		}
		if got := stableMeta.AgentMethods["stable"]; got != "stable/method" {
			t.Fatalf("expected stable meta to remain unchanged; got %q", got)
		}
	})
}

// A definition that differs from its stable twin in description only is not
// a change: the unstable method keeps using the stable type. A real change,
// here a new enum value, still gets an unstable copy.
func TestMergeStableAndUnstable_IgnoresDescriptionOnlyChanges(t *testing.T) {
	stableSchema := &Schema{Defs: map[string]*Definition{
		"SessionId": {Type: "string", Description: "See [docs](https://example.com/v1/session)."},
		"Category":  {Type: "string", Enum: []any{"mode"}},
	}}
	unstableSchema := &Schema{Defs: map[string]*Definition{
		"SessionId": {Type: "string", Description: "See [docs](https://example.com/v1/draft/session)."},
		"Category":  {Type: "string", Enum: []any{"mode", "speed"}},
		"ForkSessionRequest": {
			Type: "object", XMethod: "session/fork", XSide: "agent",
			Properties: map[string]*Definition{"sessionId": ref("SessionId"), "category": ref("Category")},
		},
	}}
	unstableMeta := &Meta{Version: 1, AgentMethods: map[string]string{"session_fork": "session/fork"}}
	_, combined := mustMerge(t, &Meta{Version: 1}, stableSchema, unstableMeta, unstableSchema)

	if _, ok := combined.Defs["UnstableSessionId"]; ok {
		t.Errorf("UnstableSessionId was added for a description-only change")
	}
	if combined.Defs["SessionId"].Description != stableSchema.Defs["SessionId"].Description {
		t.Errorf("SessionId has the unstable description %q", combined.Defs["SessionId"].Description)
	}
	if combined.Defs["UnstableCategory"] != nil {
		t.Errorf("UnstableCategory was added; want one type per shape")
	}
	if got := combined.Defs["Category"].Enum; len(got) != 2 {
		t.Errorf("Category enum = %v, want the unstable version with speed", got)
	}
	req := combined.Defs["UnstableForkSessionRequest"]
	if req == nil {
		t.Fatalf("UnstableForkSessionRequest is missing")
	}
	if got := req.Properties["sessionId"].Ref; got != "#/$defs/SessionId" {
		t.Errorf("sessionId refers to %q, want the stable SessionId", got)
	}
	if got := req.Properties["category"].Ref; got != "#/$defs/Category" {
		t.Errorf("category refers to %q, want Category", got)
	}
}

// Each property and alternative that only the unstable version of a shared
// definition has is marked UNSTABLE, unless the schema marks it already; the
// ones that the stable version has are left as they are.
func TestMergeStableAndUnstable_MarksUnstableAdditions(t *testing.T) {
	stableSchema := &Schema{Defs: map[string]*Definition{
		"Caps": {Type: "object", Properties: map[string]*Definition{"fs": {Type: "boolean", Description: "Files."}}},
		"Server": {AnyOf: []*Definition{
			{Title: "stdio", Type: "object", Description: "Stdio."},
			{Properties: map[string]*Definition{"type": {Const: "http"}}, Description: "HTTP."},
		}},
	}}
	unstableSchema := &Schema{Defs: map[string]*Definition{
		"Caps": {Type: "object", Properties: map[string]*Definition{
			"fs":    {Type: "boolean", Description: "Files."},
			"plan":  {Type: "boolean", Description: "Plans."},
			"agent": {Type: "boolean", Description: "**UNSTABLE**\n\nAgents."},
		}},
		"Server": {AnyOf: []*Definition{
			{Title: "stdio", Type: "object", Description: "Stdio."},
			{Properties: map[string]*Definition{"type": {Const: "http"}, "url": {Type: "string"}}, Description: "HTTP."},
			{Properties: map[string]*Definition{"type": {Const: "acp"}}, Description: "ACP."},
		}},
	}}
	// A method reaches both, so that they are promoted.
	for _, schema := range []*Schema{stableSchema, unstableSchema} {
		schema.Defs["InitRequest"] = &Definition{Type: "object", XMethod: "init", XSide: "agent", Properties: map[string]*Definition{"caps": ref("Caps"), "server": ref("Server")}}
	}
	meta := &Meta{Version: 1, AgentMethods: map[string]string{"init": "init"}}
	_, combined := mustMerge(t, meta, stableSchema, meta, unstableSchema)
	caps, server := combined.Defs["Caps"], combined.Defs["Server"]
	for name, want := range map[string]string{"fs": "Files.", "plan": unstableNote + "\n\nPlans.", "agent": "**UNSTABLE**\n\nAgents."} {
		if got := caps.Properties[name].Description; got != want {
			t.Errorf("Caps.%s description = %q, want %q", name, got, want)
		}
	}
	for i, want := range []string{"Stdio.", "HTTP.", unstableNote + "\n\nACP."} {
		if got := server.AnyOf[i].Description; got != want {
			t.Errorf("Server alternative %d description = %q, want %q", i, got, want)
		}
	}
	if got := unstableSchema.Defs["Caps"].Properties["plan"].Description; got != "Plans." {
		t.Errorf("the unstable input changed: %q", got)
	}
}

// An addition below the top level of a shared definition is marked too: a
// property of an inline property, of an alternative that both versions have
// (with or without a discriminator), or of array items, and an alternative of
// a property's union. An added enum value gets the note in EnumNotes.
func TestMergeStableAndUnstable_MarksNestedAndEnumAdditions(t *testing.T) {
	stableSchema := &Schema{Defs: map[string]*Definition{
		"Caps": {Type: "object", Properties: map[string]*Definition{
			"terminal": {Type: "object", Properties: map[string]*Definition{"output": {Type: "boolean"}}},
			"auth":     {AnyOf: []*Definition{{Title: "token", Type: "object"}}},
			"tags":     {Type: "array", Items: &Definition{Type: "object", Properties: map[string]*Definition{"name": {Type: "string"}}}},
			"transport": {AnyOf: []*Definition{
				{Type: "object", Properties: map[string]*Definition{"host": {Type: "string", Description: "Host."}}},
				{Type: "null"},
			}},
		}},
		"Server": {AnyOf: []*Definition{{Properties: map[string]*Definition{"type": {Const: "http"}}, Description: "HTTP."}}},
		"Mode":   {Type: "string", Enum: []any{"ask"}},
		"Item":   {Type: "object", AllOf: []*Definition{ref("Base")}},
		"Base":   {Type: "object", Properties: map[string]*Definition{"id": {Type: "string"}}},
		"Extra":  {Type: "object", Properties: map[string]*Definition{"extra": {Type: "string", Description: "Extra."}}},
	}}
	unstableSchema := &Schema{Defs: map[string]*Definition{
		"Caps": {Type: "object", Properties: map[string]*Definition{
			"terminal": {Type: "object", Properties: map[string]*Definition{"output": {Type: "boolean"}, "pty": {Type: "boolean", Description: "A pty."}}},
			"auth":     {AnyOf: []*Definition{{Title: "token", Type: "object"}, {Title: "oauth", Type: "object", Description: "OAuth."}}},
			"tags":     {Type: "array", Items: &Definition{Type: "object", Properties: map[string]*Definition{"name": {Type: "string"}, "color": {Type: "string"}}}},
			"transport": {AnyOf: []*Definition{
				{Type: "object", Properties: map[string]*Definition{"host": {Type: "string", Description: "Host."}, "port": {Type: "integer", Description: "Port."}}},
				{Type: "null"},
				{Type: "string", Description: "A URL."},
			}},
		}},
		"Server": {AnyOf: []*Definition{{Properties: map[string]*Definition{"type": {Const: "http"}, "url": {Type: "string", Description: "The URL."}}, Description: "HTTP."}}},
		"Mode":   {Type: "string", Enum: []any{"ask", "auto"}},
		"Item": {Type: "object", AllOf: []*Definition{
			ref("Base"), ref("Extra"), {Type: "object", Properties: map[string]*Definition{"inline": {Type: "string"}}},
		}},
		"Base":  {Type: "object", Properties: map[string]*Definition{"id": {Type: "string"}}},
		"Extra": {Type: "object", Properties: map[string]*Definition{"extra": {Type: "string", Description: "Extra."}}},
	}}
	for _, schema := range []*Schema{stableSchema, unstableSchema} {
		schema.Defs["InitRequest"] = &Definition{Type: "object", XMethod: "init", XSide: "agent", Properties: map[string]*Definition{
			"caps": ref("Caps"), "server": ref("Server"), "mode": ref("Mode"), "item": ref("Item"),
		}}
	}
	meta := &Meta{Version: 1, AgentMethods: map[string]string{"init": "init"}}
	_, combined := mustMerge(t, meta, stableSchema, meta, unstableSchema)
	caps := combined.Defs["Caps"].Properties
	for where, got := range map[string]string{
		"Caps.terminal.pty":           caps["terminal"].Properties["pty"].Description,
		"Caps.auth oauth":             caps["auth"].AnyOf[1].Description,
		"Caps.tags[].color":           caps["tags"].Items.Properties["color"].Description,
		"Server http .url":            combined.Defs["Server"].AnyOf[0].Properties["url"].Description,
		"Caps.transport object .port": caps["transport"].AnyOf[0].Properties["port"].Description,
		"Caps.transport string":       caps["transport"].AnyOf[2].Description,
		"Caps.terminal.output":        caps["terminal"].Properties["output"].Description,
		"Caps.transport object":       caps["transport"].AnyOf[0].Description,
		"Caps.transport object .host": caps["transport"].AnyOf[0].Properties["host"].Description,
	} {
		want := strings.HasPrefix(got, unstableNote)
		switch where {
		case "Caps.terminal.output", "Caps.transport object", "Caps.transport object .host":
			want = !want
		}
		if !want {
			t.Errorf("%s description = %q", where, got)
		}
	}
	// An added allOf part marks the properties that it brings into Item. A
	// part that refers to Extra becomes an inline copy, so that Extra itself
	// stays as it is.
	item := combined.Defs["Item"].AllOf
	if len(item) != 3 || item[0].Ref != "#/$defs/Base" || item[1].Ref != "" {
		t.Fatalf("Item.allOf = %+v, want Base, an inline copy of Extra, and the inline part", item)
	}
	for where, got := range map[string]string{
		"Item allOf Extra.extra": item[1].Properties["extra"].Description,
		"Item allOf inline":      item[2].Properties["inline"].Description,
	} {
		if !strings.HasPrefix(got, unstableNote) {
			t.Errorf("%s description = %q", where, got)
		}
	}
	if got := combined.Defs["Extra"].Properties["extra"].Description; got != "Extra." {
		t.Errorf("Extra.extra description = %q, want it unmarked", got)
	}
	if got := combined.Defs["Mode"].EnumNotes; len(got) != 1 || got["auto"] != unstableNote {
		t.Errorf("Mode.EnumNotes = %v, want the note on auto only", got)
	}
	if unstableSchema.Defs["Mode"].EnumNotes != nil {
		t.Error("the unstable input changed")
	}
}

// A property or alternative that only the unstable version has is marked with
// everything inside it, as its descendants become Go types, fields, and
// constants too. A const inside it, such as a variant's discriminator, is
// left as it is.
func TestMergeStableAndUnstable_MarksTheWholeAddedSubtree(t *testing.T) {
	stableSchema := &Schema{Defs: map[string]*Definition{
		"Caps": {Type: "object", Properties: map[string]*Definition{"fs": {Type: "boolean"}}},
	}}
	unstableSchema := &Schema{Defs: map[string]*Definition{
		"Caps": {Type: "object", Properties: map[string]*Definition{
			"fs": {Type: "boolean"},
			"terminal": {Type: "object", Description: "Terminals.", Properties: map[string]*Definition{
				"shell": {Type: "string", Description: "The shell."},
				"mode":  {Type: "string", Enum: []any{"pty", "pipe"}},
				"envs":  {Type: "array", Items: &Definition{Type: "object", Properties: map[string]*Definition{"name": {Type: "string"}}}},
				"kind": {OneOf: []*Definition{
					{Type: "object", Properties: map[string]*Definition{"type": {Const: "local"}, "path": {Type: "string"}}},
				}},
			}},
		}},
	}}
	for _, schema := range []*Schema{stableSchema, unstableSchema} {
		schema.Defs["InitRequest"] = &Definition{Type: "object", XMethod: "init", XSide: "agent", Properties: map[string]*Definition{"caps": ref("Caps")}}
	}
	meta := &Meta{Version: 1, AgentMethods: map[string]string{"init": "init"}}
	_, combined := mustMerge(t, meta, stableSchema, meta, unstableSchema)
	terminal := combined.Defs["Caps"].Properties["terminal"]
	kind := terminal.Properties["kind"].OneOf[0]
	for where, got := range map[string]string{
		"terminal":                 terminal.Description,
		"terminal.shell":           terminal.Properties["shell"].Description,
		"terminal.envs[].name":     terminal.Properties["envs"].Items.Properties["name"].Description,
		"terminal.kind local":      kind.Description,
		"terminal.kind local.path": kind.Properties["path"].Description,
	} {
		if !strings.HasPrefix(got, unstableNote) {
			t.Errorf("%s description = %q, want the note", where, got)
		}
	}
	if got := kind.Properties["type"].Description; got != "" {
		t.Errorf("the discriminator description = %q, want it left out", got)
	}
	if got := terminal.Properties["mode"].EnumNotes; len(got) != 2 {
		t.Errorf("terminal.mode EnumNotes = %v, want both values", got)
	}
	if got := combined.Defs["Caps"].Properties["fs"].Description; got != "" {
		t.Errorf("fs description = %q, want it unmarked", got)
	}
}

// A definition with x-method whose method the metadata does not list, such
// as proxy/initialize, gets no Go method, so the shared definitions that it
// reaches keep their stable version.
func TestMergeStableAndUnstable_IgnoresRootsThatTheMetadataDoesNotList(t *testing.T) {
	shared := func(props ...string) *Definition {
		d := &Definition{Type: "object", Properties: map[string]*Definition{}}
		for _, p := range props {
			d.Properties[p] = &Definition{Type: "string"}
		}
		return d
	}
	stableSchema := &Schema{Defs: map[string]*Definition{
		"Shared":      shared("id"),
		"InitRequest": {Type: "object", XMethod: "init", XSide: "agent", Properties: map[string]*Definition{"id": {Type: "string"}}},
	}}
	unstableSchema := &Schema{Defs: map[string]*Definition{
		"Shared":       shared("id", "hop"),
		"InitRequest":  {Type: "object", XMethod: "init", XSide: "agent", Properties: map[string]*Definition{"id": {Type: "string"}}},
		"ProxyRequest": {Type: "object", XMethod: "proxy/initialize", XSide: "agent", Properties: map[string]*Definition{"shared": ref("Shared")}},
	}}
	meta := &Meta{Version: 1, AgentMethods: map[string]string{"init": "init"}}
	_, combined := mustMerge(t, meta, stableSchema, meta, unstableSchema)
	if got := combined.Defs["Shared"].Properties; len(got) != 1 {
		t.Errorf("Shared properties = %v, want the stable version", got)
	}
}
