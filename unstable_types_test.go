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

func TestSessionConfigOptionSelect_MetadataRoundTrip(t *testing.T) {
	in := SessionConfigOption{
		Select: &SessionConfigOptionSelect{
			Type:         "select",
			Id:           SessionConfigId("reasoning_effort"),
			Name:         "Reasoning effort",
			Description:  Ptr("Controls thought depth"),
			CurrentValue: SessionConfigValueId("medium"),
			Options: SessionConfigSelectOptions{Ungrouped: &SessionConfigSelectOptionsUngrouped{
				{Name: "Low", Value: SessionConfigValueId("low")},
				{Name: "Medium", Value: SessionConfigValueId("medium")},
			}},
		},
	}
	modelCategory := SessionConfigOptionCategoryOther("model")
	in.Select.Category = &SessionConfigOptionCategory{Other: &modelCategory}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if raw["id"] != "reasoning_effort" || raw["name"] != "Reasoning effort" {
		t.Fatalf("missing identity metadata in json: %s", string(b))
	}

	var out SessionConfigOption
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if out.Select == nil || out.Select.Id != "reasoning_effort" || out.Select.Name != "Reasoning effort" {
		t.Fatalf("identity metadata lost on round-trip: %+v", out.Select)
	}
}

func TestUnstableSessionConfigOptionSelect_MetadataRoundTrip(t *testing.T) {
	payload := []byte(`{"type":"select","id":"model","name":"Model","category":"model","description":"Choose a model","currentValue":"gpt-4.1","options":[{"name":"GPT-4.1","value":"gpt-4.1"}]}`)

	var opt UnstableSessionConfigOption
	if err := json.Unmarshal(payload, &opt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if opt.Select == nil || opt.Select.Id != "model" || opt.Select.Name != "Model" {
		t.Fatalf("missing unstable identity metadata: %+v", opt.Select)
	}

	b, err := json.Marshal(opt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if raw["id"] != "model" || raw["name"] != "Model" {
		t.Fatalf("identity metadata not emitted in unstable json: %s", string(b))
	}
}
