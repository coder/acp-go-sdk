package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestElicitation_RoundTripKeepsOwnPropertiesAndScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into func() any
		json string
	}{
		{
			name: "form mode with a session scope",
			into: func() any { return new(ElicitationFormMode) },
			json: `{"requestedSchema":{"type":"object","properties":{"name":{"type":"string"}}},"sessionId":"s","toolCallId":"t"}`,
		},
		{
			name: "url mode with a request scope",
			into: func() any { return new(ElicitationUrlMode) },
			json: `{"elicitationId":"e","url":"https://example.com/auth","requestId":7}`,
		},
		{
			name: "form request with a session scope",
			into: func() any { return new(CreateElicitationRequest) },
			json: `{"mode":"form","message":"Your name?","requestedSchema":{"type":"object","properties":{}},"sessionId":"s"}`,
		},
		{
			name: "url request with a request scope",
			into: func() any { return new(CreateElicitationRequest) },
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

func TestCreateElicitationRequest_FormCarriesItsScope(t *testing.T) {
	t.Parallel()
	var req CreateElicitationRequest
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

// The schema 1.20.0 union has an open "other" mode, so the generator no longer
// emits New... constructors for it. These struct literals are the migration
// that the v0.15.0 release notes give.
func TestCreateElicitationRequest_LiteralsCarryTheirScope(t *testing.T) {
	t.Parallel()
	requestID := RequestIdStr("r")
	tests := []struct {
		name string
		req  CreateElicitationRequest
		want string
	}{
		{"form in a session", CreateElicitationRequest{Form: &CreateElicitationForm{
			Mode: "form", RequestedSchema: ElicitationSchema{},
			Session: &ElicitationSessionScope{SessionId: "s"},
		}}, `"sessionId":"s"`},
		{"form in a request", CreateElicitationRequest{Form: &CreateElicitationForm{
			Mode: "form", RequestedSchema: ElicitationSchema{},
			Request: &ElicitationRequestScope{RequestId: RequestId{Str: &requestID}},
		}}, `"requestId":"r"`},
		{"url in a session", CreateElicitationRequest{Url: &CreateElicitationUrl{
			Mode: "url", ElicitationId: "e", Url: "https://example.com",
			Session: &ElicitationSessionScope{SessionId: "s"},
		}}, `"sessionId":"s"`},
		{"url in a request", CreateElicitationRequest{Url: &CreateElicitationUrl{
			Mode: "url", ElicitationId: "e", Url: "https://example.com",
			Request: &ElicitationRequestScope{RequestId: RequestId{Str: &requestID}},
		}}, `"requestId":"r"`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("marshal the literal: %v", err)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Fatalf("literal encodes to %s, want it to hold %s", out, tt.want)
			}
		})
	}
}
