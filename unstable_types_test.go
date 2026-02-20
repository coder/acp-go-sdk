package acp

import (
	"encoding/json"
	"testing"
)

func TestUnstableSessionConfigSelectOptions_UnmarshalArrayVariants(t *testing.T) {
	t.Run("ungrouped", func(t *testing.T) {
		var got UnstableSessionConfigSelectOptions
		payload := []byte(`[{"name":"fast","value":"fast"}]`)
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("unmarshal ungrouped options: %v", err)
		}
		if got.Ungrouped == nil {
			t.Fatal("expected ungrouped variant to be set")
		}
		if got.Grouped != nil {
			t.Fatal("expected grouped variant to be nil")
		}
		if len(*got.Ungrouped) != 1 {
			t.Fatalf("expected one ungrouped option, got %d", len(*got.Ungrouped))
		}
		if (*got.Ungrouped)[0].Value != UnstableSessionConfigValueId("fast") {
			t.Fatalf("unexpected option value: %q", (*got.Ungrouped)[0].Value)
		}
	})

	t.Run("grouped", func(t *testing.T) {
		var got UnstableSessionConfigSelectOptions
		payload := []byte(`[{"group":"performance","name":"Performance","options":[{"name":"Balanced","value":"balanced"}]}]`)
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("unmarshal grouped options: %v", err)
		}
		if got.Grouped == nil {
			t.Fatal("expected grouped variant to be set")
		}
		if got.Ungrouped != nil {
			t.Fatal("expected ungrouped variant to be nil")
		}
		if len(*got.Grouped) != 1 {
			t.Fatalf("expected one group, got %d", len(*got.Grouped))
		}
		if len((*got.Grouped)[0].Options) != 1 {
			t.Fatalf("expected one option in first group, got %d", len((*got.Grouped)[0].Options))
		}
		if (*got.Grouped)[0].Options[0].Value != UnstableSessionConfigValueId("balanced") {
			t.Fatalf("unexpected grouped option value: %q", (*got.Grouped)[0].Options[0].Value)
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("marshal grouped options union: %v", err)
		}
	})
}
