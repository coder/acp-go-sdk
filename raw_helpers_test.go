package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

// A value that json.Marshal cannot encode makes a tool_call_update fail to
// encode, so that sending it returns the error, as v0.19.0 did.
func TestWithUpdateRawInput_UnencodableValueFailsTheSend(t *testing.T) {
	t.Parallel()
	var typeErr *json.UnsupportedTypeError
	_, err := json.Marshal(UpdateToolCall("t", WithUpdateRawInput(make(chan int))))
	if !errors.As(err, &typeErr) {
		t.Errorf("update with a channel as rawInput encodes with error %v, want a *json.UnsupportedTypeError", err)
	}
	var valueErr *json.UnsupportedValueError
	_, err = json.Marshal(UpdateToolCall("t", WithUpdateRawOutput(math.NaN())))
	if !errors.As(err, &valueErr) {
		t.Errorf("update with NaN as rawOutput encodes with error %v, want a *json.UnsupportedValueError", err)
	}

	peerOutput, peerWriter := io.Pipe()
	defer peerWriter.Close()
	conn := NewAgentSideConnection(agentFuncs{}, io.Discard, peerOutput)
	err = conn.SessionUpdate(context.Background(), SessionNotification{SessionId: "s", Update: UpdateToolCall("t", WithUpdateRawInput(make(chan int)))})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) || reqErr.Code != -32602 || !strings.Contains(err.Error(), "unsupported type: chan int") {
		t.Errorf("SessionUpdate returned %v, want invalid params that name the channel", err)
	}
}

// A nil value leaves the field unset, and a value that encodes is sent.
func TestWithUpdateRawInput_NilIsUnset(t *testing.T) {
	t.Parallel()
	update := UpdateToolCall("t", WithUpdateRawInput(nil), WithUpdateRawOutput(map[string]int{"n": 1}))
	const want = `{"rawOutput":{"n":1},"sessionUpdate":"tool_call_update","toolCallId":"t"}`
	if out, err := json.Marshal(update); err != nil || string(out) != want {
		t.Errorf("update encodes to %s (error %v), want %s", out, err, want)
	}
}

// A start update has a plain json.RawMessage field, which cannot hold the
// error, so a value that json.Marshal cannot encode is left out.
func TestWithStartRawInput_UnencodableValueIsLeftOut(t *testing.T) {
	t.Parallel()
	update := StartToolCall("t", "Run", WithStartRawInput(make(chan int)))
	const want = `{"sessionUpdate":"tool_call","title":"Run","toolCallId":"t"}`
	if out, err := json.Marshal(update); err != nil || string(out) != want {
		t.Errorf("update encodes to %s (error %v), want %s", out, err, want)
	}
}
