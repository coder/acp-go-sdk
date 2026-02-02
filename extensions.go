package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ExtensionMethodHandler can be implemented by either an Agent or a Client.
//
// ACP extension methods are JSON-RPC methods whose names begin with "_".
// They provide a stable namespace for custom functionality that is not part
// of the core ACP spec.
//
// If the method is unrecognized, implementations should return NewMethodNotFound(method).
//
// See: https://agentclientprotocol.com/protocol/extensibility#extension-methods
type ExtensionMethodHandler interface {
	HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error)
}

func validateExtensionMethodName(method string) error {
	if method == "" {
		return fmt.Errorf("extension method name must be non-empty")
	}
	if !strings.HasPrefix(method, "_") {
		return fmt.Errorf("extension method name must start with '_' (got %q)", method)
	}
	return nil
}

func isExtensionMethodName(method string) bool {
	return strings.HasPrefix(method, "_")
}

func (a *AgentSideConnection) handleWithExtensions(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	if isExtensionMethodName(method) {
		h, ok := a.agent.(ExtensionMethodHandler)
		if !ok {
			return nil, NewMethodNotFound(method)
		}
		resp, err := h.HandleExtensionMethod(ctx, method, params)
		if err != nil {
			return nil, toReqErr(err)
		}
		return resp, nil
	}

	return a.handle(ctx, method, params)
}

func (c *ClientSideConnection) handleWithExtensions(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	if isExtensionMethodName(method) {
		h, ok := c.client.(ExtensionMethodHandler)
		if !ok {
			return nil, NewMethodNotFound(method)
		}
		resp, err := h.HandleExtensionMethod(ctx, method, params)
		if err != nil {
			return nil, toReqErr(err)
		}
		return resp, nil
	}

	return c.handle(ctx, method, params)
}

// CallExtension sends an ACP extension-method request (method names starting with "_")
// from an agent to its client.
func (c *AgentSideConnection) CallExtension(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := validateExtensionMethodName(method); err != nil {
		return nil, err
	}
	return SendRequest[json.RawMessage](c.conn, ctx, method, params)
}

// NotifyExtension sends an ACP extension-method notification (method names starting with "_")
// from an agent to its client.
func (c *AgentSideConnection) NotifyExtension(ctx context.Context, method string, params any) error {
	if err := validateExtensionMethodName(method); err != nil {
		return err
	}
	return c.conn.SendNotification(ctx, method, params)
}

// CallExtension sends an ACP extension-method request (method names starting with "_")
// from a client to its agent.
func (c *ClientSideConnection) CallExtension(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := validateExtensionMethodName(method); err != nil {
		return nil, err
	}
	return SendRequest[json.RawMessage](c.conn, ctx, method, params)
}

// NotifyExtension sends an ACP extension-method notification (method names starting with "_")
// from a client to its agent.
func (c *ClientSideConnection) NotifyExtension(ctx context.Context, method string, params any) error {
	if err := validateExtensionMethodName(method); err != nil {
		return err
	}
	return c.conn.SendNotification(ctx, method, params)
}
