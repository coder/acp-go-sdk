package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// RequestError represents a JSON-RPC error response. Data holds the JSON of
// the error's data as it was read, so a number keeps every digit, and a
// "data": null stays null. The generated Error type reads "data": null as no
// data instead, as its schema asks. The New* constructors encode their data
// argument into Data. A request handler whose error has a Data that is not
// valid JSON answers with an internal error (-32603).
type RequestError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RequestError) Error() string {
	// Prefer a structured, JSON-style string so callers get details by default
	// similar to the TypeScript client.
	// Example: {"code":-32603,"message":"Internal error","data":{"details":"..."}}
	if e == nil {
		return "<nil>"
	}
	// Try to pretty-print compact JSON for stability in logs.
	type view RequestError
	b, err := json.Marshal(view(*e))
	if err == nil {
		return string(b)
	}
	// Data is not valid JSON.
	if e.Data != nil {
		return fmt.Sprintf("code %d: %s (data: %s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("code %d: %s", e.Code, e.Message)
}

// NewRequestError returns the error with code and message, and with data
// encoded as JSON. A nil data, or one that json.Marshal cannot encode, gives
// an error without data. The other New* constructors set a code of the
// JSON-RPC or ACP specification the same way.
func NewRequestError(code int, message string, data any) *RequestError {
	return &RequestError{Code: code, Message: message, Data: encodeRaw(data)}
}

func NewParseError(data any) *RequestError {
	return NewRequestError(-32700, "Parse error", data)
}

func NewInvalidRequest(data any) *RequestError {
	return NewRequestError(-32600, "Invalid request", data)
}

func NewMethodNotFound(method string) *RequestError {
	return NewRequestError(-32601, "Method not found", map[string]string{"method": method})
}

func NewInvalidParams(data any) *RequestError {
	return NewRequestError(-32602, "Invalid params", data)
}

func NewInternalError(data any) *RequestError {
	return NewRequestError(-32603, "Internal error", data)
}

func NewRequestCancelled(data any) *RequestError {
	return NewRequestError(-32800, "Request cancelled", data)
}

func NewAuthRequired(data any) *RequestError {
	return NewRequestError(-32000, "Authentication required", data)
}

// toReqErr coerces arbitrary errors into JSON-RPC RequestError.
func toReqErr(err error) *RequestError {
	if err == nil {
		return nil
	}
	if re, ok := err.(*RequestError); ok {
		return re
	}
	if errors.Is(err, context.Canceled) {
		return NewRequestCancelled(map[string]any{"error": err.Error()})
	}
	return NewInternalError(map[string]any{"error": err.Error()})
}
