package acp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestToReqErr_ContextCanceledMapsToRequestCancelled(t *testing.T) {
	wrapped := errors.Join(context.Canceled, errors.New("extra context"))
	re := toReqErr(wrapped)
	if re == nil {
		t.Fatal("expected request error")
	}
	if re.Code != -32800 {
		t.Fatalf("expected code -32800, got %d", re.Code)
	}
}

func TestToReqErr_DeadlineExceededMapsToInternalError(t *testing.T) {
	re := toReqErr(context.DeadlineExceeded)
	if re == nil {
		t.Fatal("expected request error")
	}
	if re.Code != -32603 {
		t.Fatalf("expected code -32603, got %d", re.Code)
	}
}

func TestNewUnionDecodeError_IncludesSafeContext(t *testing.T) {
	payload := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"toolu_abc123","content":"secret-value","authorization":"Bearer secret"}`)
	err := newUnionDecodeError("SessionUpdate", "tool_call_update", payload, errors.New("bad content"))
	message := err.Error()

	for _, want := range []string{"SessionUpdate", "tool_call_update", "toolu_abc123", "fields=4", "bad content"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}
	for _, secret := range []string{"secret-value", "Bearer secret"} {
		if strings.Contains(message, secret) {
			t.Errorf("error exposed redacted payload value %q: %q", secret, message)
		}
	}
}

func TestNewUnionDecodeError_NonObjectDoesNotEchoPayload(t *testing.T) {
	payload := []byte(`"secret-value"`)
	message := newUnionDecodeError("Example", "", payload, nil).Error()

	if strings.Contains(message, "secret-value") {
		t.Fatalf("error exposed non-object payload: %q", message)
	}
	if !strings.Contains(message, "non-object JSON") {
		t.Fatalf("error lacks structural context: %q", message)
	}
}
