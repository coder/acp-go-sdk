package ir

import (
	"fmt"
	"maps"
	"slices"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// The proxy methods, and the agent method that the conductor of a proxy
// chain replaces with proxyInitialize when it initializes a proxy.
const (
	proxyInitialize = "proxy/initialize"
	proxySuccessor  = "proxy/successor"
	agentInitialize = "initialize"
)

// ProxyRole is what a proxy in an ACP proxy chain handles and sends. The
// proxy talks only to its conductor. The messages of the client side reach
// it as they are, and the messages to and from its successor travel in a
// Successor envelope.
type ProxyRole struct {
	// Initialize is proxy/initialize, which the conductor sends a proxy
	// instead of Replaces. Without a handler, the proxy forwards it to its
	// successor as Replaces.
	Initialize *MethodInfo
	// Replaces is the agent method initialize, which a proxy rejects.
	Replaces *MethodInfo
	// Successor is proxy/successor, the envelope of each message to or from
	// the successor.
	Successor *MethodInfo
	// FromClient are the methods that a proxy handles from the client side:
	// Initialize, then each agent method but Replaces.
	FromClient []*MethodInfo
	// Agent are the agent methods, which a proxy sends to its successor.
	// Client are the client methods, which a proxy handles from its
	// successor and sends to the client.
	Agent, Client []*MethodInfo
}

// BuildProxyRole returns the proxy role of the methods in groups, or nil
// when meta lists no proxy methods. It fails when meta lists other proxy
// methods than proxy/initialize and proxy/successor, when their types are
// missing or of the wrong kind, or when two methods that a proxy handles or
// sends in one direction share a Go name.
func BuildProxyRole(groups Groups, meta *load.Meta) (*ProxyRole, error) {
	if len(meta.ProxyMethods) == 0 {
		return nil, nil
	}
	wires := slices.Sorted(maps.Values(meta.ProxyMethods))
	if !slices.Equal(wires, []string{proxyInitialize, proxySuccessor}) {
		return nil, fmt.Errorf("proxy methods %q: want exactly %q and %q", wires, proxyInitialize, proxySuccessor)
	}
	role := &ProxyRole{
		Initialize: groups[key("proxy", proxyInitialize)],
		Replaces:   groups[key("agent", agentInitialize)],
		Successor:  groups[key("proxy", proxySuccessor)],
		Agent:      sideMethods(groups, "agent", meta.AgentMethods),
		Client:     sideMethods(groups, "client", meta.ClientMethods),
	}
	if mi := role.Initialize; mi == nil || mi.Req == "" || mi.Resp == "" || mi.Notif != "" {
		return nil, fmt.Errorf("%s needs a request and a response type and no notification type", proxyInitialize)
	}
	if mi := role.Successor; mi == nil || mi.Req == "" || mi.Resp == "" || mi.Notif == "" {
		return nil, fmt.Errorf("%s needs a request, a response and a notification type", proxySuccessor)
	}
	if mi := role.Replaces; mi == nil || mi.Req == "" || mi.Notif != "" {
		return nil, fmt.Errorf("%s, which %s replaces, needs a request type and no notification type", agentInitialize, proxyInitialize)
	}
	role.FromClient = []*MethodInfo{role.Initialize}
	for _, mi := range role.Agent {
		if mi != role.Replaces {
			role.FromClient = append(role.FromClient, mi)
		}
	}
	for _, methods := range [][]*MethodInfo{role.FromClient, role.Agent, role.Client} {
		seen := map[string]string{}
		for _, mi := range methods {
			if other, ok := seen[mi.Name]; ok {
				return nil, fmt.Errorf("%s methods %q and %q share the Go name %s", mi.Side, other, mi.Method, mi.Name)
			}
			seen[mi.Name] = mi.Method
		}
	}
	return role, nil
}

// sideMethods returns the methods of side in the order of their meta keys,
// leaving out those without a request or a notification type, as the Agent
// and Client interfaces do.
func sideMethods(groups Groups, side string, methods map[string]string) []*MethodInfo {
	var out []*MethodInfo
	for _, k := range SortedKeys(methods) {
		if mi := groups[key(side, methods[k])]; mi != nil && (mi.Req != "" || mi.Notif != "") {
			out = append(out, mi)
		}
	}
	return out
}
