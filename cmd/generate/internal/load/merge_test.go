package load

import "testing"

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

	t.Run("transitive ref rewriting when referenced type is new or changed", func(t *testing.T) {
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
			"FooParams": {Description: "unstable params", Type: "object"},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		if combinedSchema.Defs["UnstableFooParams"] == nil {
			t.Fatalf("expected UnstableFooParams definition to be added")
		}
		unstableReq := combinedSchema.Defs["UnstableFooRequest"]
		if unstableReq == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if unstableReq.Properties == nil || unstableReq.Properties["params"] == nil {
			t.Fatalf("expected UnstableFooRequest to have params property")
		}
		if got := unstableReq.Properties["params"].Ref; got != "#/$defs/UnstableFooParams" {
			t.Fatalf("expected params ref rewritten to UnstableFooParams; got %q", got)
		}
		// Ensure we didn't mutate the unstable input schema in-place.
		if got := unstableSchema.Defs["FooRequest"].Properties["params"].Ref; got != "#/$defs/FooParams" {
			t.Fatalf("expected unstable input schema refs to remain unchanged; got %q", got)
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

	t.Run("x-method/x-side cleared on unstable copy when x-method is stable wire method", func(t *testing.T) {
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
			// Changed relative to stable, so it will be duplicated.
			"StableThingRequest": {
				Description: "unstable StableThingRequest",
				Type:        "object",
				XMethod:     "stable/method",
				XSide:       "agent",
			},
		}}

		_, combinedSchema := mustMerge(t, stableMeta, stableSchema, unstableMeta, unstableSchema)

		unstableCopy := combinedSchema.Defs["UnstableStableThingRequest"]
		if unstableCopy == nil {
			t.Fatalf("expected UnstableStableThingRequest definition to be added")
		}
		if unstableCopy.XMethod != "" {
			t.Fatalf("expected UnstableStableThingRequest.XMethod to be cleared; got %q", unstableCopy.XMethod)
		}
		if unstableCopy.XSide != "" {
			t.Fatalf("expected UnstableStableThingRequest.XSide to be cleared; got %q", unstableCopy.XSide)
		}
		// The stable definition should keep its RPC markers.
		if got := stableSchema.Defs["StableThingRequest"].XMethod; got != "stable/method" {
			t.Fatalf("expected stable StableThingRequest to retain XMethod; got %q", got)
		}

		unstableReq := combinedSchema.Defs["UnstableFooRequest"]
		if unstableReq == nil {
			t.Fatalf("expected UnstableFooRequest definition to be added")
		}
		if got := unstableReq.Properties["stable"].Ref; got != "#/$defs/UnstableStableThingRequest" {
			t.Fatalf("expected ref rewritten to UnstableStableThingRequest; got %q", got)
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
			"FooParams": {Description: "unstable params", Type: "object"},
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
