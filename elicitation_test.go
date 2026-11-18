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
		{
			name: "request of an unknown mode with members that the schema does not name",
			into: func() any { return new(CreateElicitationRequest) },
			json: `{"mode":"_x","message":"m","sessionId":"s","extra":{"k":1}}`,
		},
		{
			name: "request of an unknown mode with a request scope and a long number",
			into: func() any { return new(CreateElicitationRequest) },
			json: `{"mode":"_x","message":"m","requestId":7,"_meta":{"a":1},"count":12345678901234567890}`,
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

func TestCreateElicitationOther_KeepsOnlyTheMembersThatNoPartNames(t *testing.T) {
	t.Parallel()
	var req CreateElicitationRequest
	if err := json.Unmarshal([]byte(`{"mode":"_x","message":"m","sessionId":"s","toolCallId":"t","extra":true}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Other == nil || req.Other.Session == nil {
		t.Fatalf("decoded %+v, want the other mode with a session scope", req)
	}
	if got := req.Other.AdditionalProperties; len(got) != 1 || string(got["extra"]) != "true" {
		t.Errorf("AdditionalProperties = %s, want only extra", got)
	}

	// toolCallId belongs to the session scope, so a request-scoped mode
	// drops it, as the Rust SDK does.
	var scoped CreateElicitationRequest
	if err := json.Unmarshal([]byte(`{"mode":"_x","message":"m","requestId":"r","toolCallId":"t"}`), &scoped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if scoped.Other == nil || scoped.Other.Request == nil || scoped.Other.AdditionalProperties != nil {
		t.Errorf("decoded %+v, want the request scope and no unnamed member", scoped.Other)
	}

	// A member that the union names keeps its value.
	req.Other.AdditionalProperties["mode"] = json.RawMessage(`"_y"`)
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"mode":"_x"`) {
		t.Errorf("encoded %s, want mode _x", out)
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

// A form's fields decode into the property schema union, and the content of
// an accepted form into the content value union, so a caller reads them
// without a type assertion and an integer keeps every digit.
func TestElicitation_TypedMapsRoundTrip(t *testing.T) {
	t.Parallel()
	const form = `{"message":"m","mode":"form","requestedSchema":{"properties":{"host":{"title":"Host","type":"string"},"port":{"type":"integer"}},"type":"object"},"sessionId":"s"}`
	var req CreateElicitationRequest
	if err := json.Unmarshal([]byte(form), &req); err != nil {
		t.Fatalf("unmarshal %s: %v", form, err)
	}
	props := req.Form.RequestedSchema.Properties
	if host := props["host"].String; host == nil || host.Title == nil || *host.Title != "Host" {
		t.Errorf("properties[host] = %+v, want a string field titled Host", props["host"])
	}
	if props["port"].Integer == nil {
		t.Errorf("properties[port] = %+v, want an integer field", props["port"])
	}

	const accept = `{"action":"accept","content":{"a":["x","y"],"b":true,"f":1.5,"n":9007199254740993,"s":"x"}}`
	var resp CreateElicitationResponse
	if err := json.Unmarshal([]byte(accept), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", accept, err)
	}
	content := resp.Accept.Content
	if n := content["n"].Integer; n == nil || *n != 9007199254740993 {
		t.Errorf("content[n] = %+v, want the integer 9007199254740993", content["n"])
	}
	if out, err := json.Marshal(resp); err != nil || string(out) != accept {
		t.Errorf("%s decodes and encodes to %s (error %v), want it unchanged", accept, out, err)
	}
}

func TestNewElicitationContentValue_EncodesEachKind(t *testing.T) {
	t.Parallel()
	content := map[string]ElicitationContentValue{
		"a": NewElicitationContentValueStringArray("x", "y"),
		"b": NewElicitationContentValueBoolean(true),
		"e": NewElicitationContentValueStringArray(),
		"f": NewElicitationContentValueNumber(1.5),
		"n": NewElicitationContentValueInteger(9007199254740993),
		"s": NewElicitationContentValueString("x"),
	}
	const want = `{"a":["x","y"],"b":true,"e":[],"f":1.5,"n":9007199254740993,"s":"x"}`
	if out, err := json.Marshal(content); err != nil || string(out) != want {
		t.Errorf("content encodes to %s (error %v), want %s", out, err, want)
	}
}
