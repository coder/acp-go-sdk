package acp

import (
	"encoding/json"
	"testing"
)

func TestSessionConfigSelectOptions_UnmarshalArrayVariants(t *testing.T) {
	t.Run("ungrouped", func(t *testing.T) {
		var got SessionConfigSelectOptions
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
		if (*got.Ungrouped)[0].Value != SessionConfigValueId("fast") {
			t.Fatalf("unexpected option value: %q", (*got.Ungrouped)[0].Value)
		}
	})

	t.Run("grouped", func(t *testing.T) {
		var got SessionConfigSelectOptions
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
		if (*got.Grouped)[0].Options[0].Value != SessionConfigValueId("balanced") {
			t.Fatalf("unexpected grouped option value: %q", (*got.Grouped)[0].Options[0].Value)
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("marshal grouped options union: %v", err)
		}
	})
}

func TestSessionConfigOptionSelect_MetadataRoundTrip(t *testing.T) {
	modelCategory := SessionConfigOptionCategoryModel
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
			Category: &modelCategory,
		},
	}

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

func TestSessionConfigOptionSelect_MetadataUnmarshalAndMarshal(t *testing.T) {
	payload := []byte(`{"type":"select","id":"model","name":"Model","category":"model","description":"Choose a model","currentValue":"gpt-4.1","options":[{"name":"GPT-4.1","value":"gpt-4.1"}]}`)

	var opt SessionConfigOption
	if err := json.Unmarshal(payload, &opt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if opt.Select == nil || opt.Select.Id != "model" || opt.Select.Name != "Model" {
		t.Fatalf("missing identity metadata: %+v", opt.Select)
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
		t.Fatalf("identity metadata not emitted in json: %s", string(b))
	}
}

func TestUnstableElicitation_RoundTripKeepsOwnPropertiesAndScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into func() any
		json string
	}{
		{
			name: "form mode with a session scope",
			into: func() any { return new(UnstableElicitationFormMode) },
			json: `{"requestedSchema":{"type":"object","properties":{"name":{"type":"string"}}},"sessionId":"s","toolCallId":"t"}`,
		},
		{
			name: "url mode with a request scope",
			into: func() any { return new(UnstableElicitationUrlMode) },
			json: `{"elicitationId":"e","url":"https://example.com/auth","requestId":7}`,
		},
		{
			name: "form request with a session scope",
			into: func() any { return new(UnstableCreateElicitationRequest) },
			json: `{"mode":"form","message":"Your name?","requestedSchema":{"type":"object","properties":{}},"sessionId":"s"}`,
		},
		{
			name: "url request with a request scope",
			into: func() any { return new(UnstableCreateElicitationRequest) },
			json: `{"mode":"url","message":"Sign in","elicitationId":"e","url":"https://example.com/auth","requestId":"r"}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := tt.into()
			if err := json.Unmarshal([]byte(tt.json), v); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.json, err)
			}
			got, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if ok, got, want := equalJSON(got, []byte(tt.json)); !ok {
				t.Fatalf("round trip changed the value\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestUnstableCreateElicitationRequest_FormCarriesItsScope(t *testing.T) {
	t.Parallel()
	var req UnstableCreateElicitationRequest
	in := `{"mode":"form","message":"Your name?","requestedSchema":{"type":"object","properties":{}},"sessionId":"s","toolCallId":"t"}`
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Form == nil || req.Form.Session == nil {
		t.Fatalf("form = %+v, want a form with a session scope", req.Form)
	}
	if req.Form.Session.SessionId != "s" || req.Form.Session.ToolCallId == nil || *req.Form.Session.ToolCallId != "t" {
		t.Errorf("session scope = %+v, want sessionId s and toolCallId t", req.Form.Session)
	}
	if req.Form.Message != "Your name?" {
		t.Errorf("message = %q, want the request's message", req.Form.Message)
	}
}
