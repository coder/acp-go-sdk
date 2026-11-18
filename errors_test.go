package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
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

// The data of an error from the peer reaches the caller with every digit.
func TestRequestError_DataFromThePeerKeepsEveryDigit(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	const data = `{"id":9007199254740993}`
	NewAgentSideConnection(agentFuncs{
		DeleteSessionFunc: func(context.Context, DeleteSessionRequest) (DeleteSessionResponse, error) {
			return DeleteSessionResponse{}, NewRequestError(-32002, "Resource not found", json.RawMessage(data))
		},
	}, a2cW, c2aR)
	c := NewClientSideConnection(&clientFuncs{}, c2aW, a2cR)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.DeleteSession(ctx, DeleteSessionRequest{SessionId: "s"})
	var re *RequestError
	if !errors.As(err, &re) || re.Code != -32002 || string(re.Data) != data {
		t.Fatalf("DeleteSession error = %v, want -32002 with data %s", err, data)
	}
	if got := re.Error(); got != `{"code":-32002,"message":"Resource not found","data":`+data+`}` {
		t.Errorf("Error() = %s", got)
	}
}

func TestNewRequestError_EncodesData(t *testing.T) {
	tests := []struct {
		data any
		want string
	}{
		{map[string]string{"k": "v"}, `{"k":"v"}`},
		{"text", `"text"`},
		{nil, ""},
		{func() {}, ""},
	}
	for _, tt := range tests {
		if got := NewRequestError(1, "m", tt.data).Data; string(got) != tt.want {
			t.Errorf("NewRequestError data %#v = %s, want %s", tt.data, got, tt.want)
		}
	}
	if got := NewMethodNotFound("x/y").Error(); got != `{"code":-32601,"message":"Method not found","data":{"method":"x/y"}}` {
		t.Errorf("NewMethodNotFound Error() = %s", got)
	}
}
