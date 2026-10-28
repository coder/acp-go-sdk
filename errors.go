package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RequestError represents a JSON-RPC error response.
type RequestError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RequestError) Error() string {
	// Prefer a structured, JSON-style string so callers get details by default
	// similar to the TypeScript client.
	// Example: {"code":-32603,"message":"Internal error","data":{"details":"..."}}
	if e == nil {
		return "<nil>"
	}
	// Try to pretty-print compact JSON for stability in logs.
	type view struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data,omitempty"`
	}
	v := view{Code: e.Code, Message: e.Message, Data: e.Data}
	b, err := json.Marshal(v)
	if err == nil {
		return string(b)
	}
	// Fallback if marshal fails.
	if e.Data != nil {
		return fmt.Sprintf("code %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("code %d: %s", e.Code, e.Message)
}

func NewParseError(data any) *RequestError {
	return &RequestError{Code: -32700, Message: "Parse error", Data: data}
}

func NewInvalidRequest(data any) *RequestError {
	return &RequestError{Code: -32600, Message: "Invalid request", Data: data}
}

func NewMethodNotFound(method string) *RequestError {
	return &RequestError{Code: -32601, Message: "Method not found", Data: map[string]any{"method": method}}
}

func NewInvalidParams(data any) *RequestError {
	return &RequestError{Code: -32602, Message: "Invalid params", Data: data}
}

func NewInternalError(data any) *RequestError {
	return &RequestError{Code: -32603, Message: "Internal error", Data: data}
}

func NewRequestCancelled(data any) *RequestError {
	return &RequestError{Code: -32800, Message: "Request cancelled", Data: data}
}

func NewAuthRequired(data any) *RequestError {
	return &RequestError{Code: -32000, Message: "Authentication required", Data: data}
}

const maxUnionContextValueLength = 80

// newUnionDecodeError builds an actionable error for a discriminated-union
// UnmarshalJSON failure. It names the union type and the variant that was
// attempted, wraps the underlying decode error (if any), and includes safe
// structural context. Arbitrary payload values are omitted because these
// errors are commonly logged and wire content may contain secrets.
func newUnionDecodeError(unionType, variant string, payload []byte, cause error) error {
	context := unionPayloadContext(payload)
	if cause != nil {
		return fmt.Errorf("%s: invalid variant payload for %q: %w (payload context: %s)",
			unionType, variant, cause, context)
	}
	return fmt.Errorf("%s: no matching variant for union (payload context: %s)", unionType, context)
}

func unionPayloadContext(payload []byte) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return fmt.Sprintf("non-object JSON (%d bytes)", len(payload))
	}

	parts := []string{fmt.Sprintf("fields=%d", len(fields))}
	for _, key := range []string{"sessionUpdate", "type", "outcome", "toolCallId"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			parts = append(parts, key+"=<non-string>")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, truncateUnionContextValue(value)))
	}
	return strings.Join(parts, ", ")
}

func truncateUnionContextValue(value string) string {
	runes := []rune(value)
	if len(runes) <= maxUnionContextValueLength {
		return value
	}
	return string(runes[:maxUnionContextValueLength]) + "..."
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
