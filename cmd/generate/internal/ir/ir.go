// Package ir defines the intermediate representation used by the Go
// code generator. It organizes methods, bindings, and schema-derived
// types so the emit package can produce helpers, types, and dispatch code.
package ir

import (
	"sort"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/util"
)

// MethodInfo captures association between a wire method and its Go types and interface.
type MethodInfo struct {
	Side   string // "agent" or "client"
	Method string // wire method, e.g., "session/new"
	Req    string // Go type name of Request
	Resp   string // Go type name of Response
	Notif  string // Go type name of Notification
	// Name is the Go method that handles the wire method, e.g., "NewSession".
	Name string
	// Optional names the method's own optional interface, such as
	// "AgentLoader". It is empty for a method of the Agent or Client interface.
	Optional    string
	DocsIgnored bool
}

// Groups is a map keyed by side|wire to MethodInfo.
type Groups map[string]*MethodInfo

func key(side, method string) string { return side + "|" + method }

// PrimaryType mirrors logic from generator: find primary type string from a Definition.
func PrimaryType(d *load.Definition) string {
	if d == nil {
		return ""
	}
	if d.Type == nil {
		// In newer schemas, properties are sometimes wrapped in allOf (commonly around a
		// single $ref). We can't fully resolve $ref here, but we can still detect inline types.
		for _, e := range d.AllOf {
			if e == nil {
				continue
			}
			if t := PrimaryType(e); t != "" {
				return t
			}
		}
		return ""
	}
	switch v := d.Type.(type) {
	case string:
		return v
	case []any:
		var first string
		for _, e := range v {
			if s, ok := e.(string); ok {
				if first == "" {
					first = s
				}
				if s != "null" {
					return s
				}
			}
		}
		return first
	default:
		return ""
	}
}

// IsNullResponse returns true if the response schema is explicitly null or missing.
func IsNullResponse(def *load.Definition) bool {
	if def == nil {
		return true
	}
	if s, ok := def.Type.(string); ok && s == "null" {
		return true
	}
	return false
}

// BuildMethodGroups merges schema-provided links with meta fallback and returns groups.
func BuildMethodGroups(schema *load.Schema, meta *load.Meta) Groups {
	groups := Groups{}
	// From schema
	for name, def := range schema.Defs {
		if def == nil || def.XMethod == "" || def.XSide == "" {
			continue
		}
		k := key(def.XSide, def.XMethod)
		mi := groups[k]
		if mi == nil {
			mi = &MethodInfo{Side: def.XSide, Method: def.XMethod}
			groups[k] = mi
		}
		if strings.HasSuffix(name, "Request") {
			mi.Req = name
		}
		if strings.HasSuffix(name, "Response") {
			mi.Resp = name
		}
		if strings.HasSuffix(name, "Notification") {
			mi.Notif = name
		}
	}
	// From meta fallback (terminal etc.)
	for mk, wire := range meta.AgentMethods {
		k := key("agent", wire)
		if groups[k] == nil {
			base := inferTypeBaseFromMethodKey(mk)
			mi := &MethodInfo{Side: "agent", Method: wire}
			if wire == "session/cancel" {
				mi.Notif = "CancelNotification"
			} else {
				if _, ok := schema.Defs[base+"Request"]; ok {
					mi.Req = base + "Request"
				}
				if _, ok := schema.Defs[base+"Response"]; ok {
					mi.Resp = base + "Response"
				}
			}
			if mi.Req != "" || mi.Notif != "" {
				groups[k] = mi
			}
		}
	}
	for mk, wire := range meta.ClientMethods {
		k := key("client", wire)
		if groups[k] == nil {
			base := inferTypeBaseFromMethodKey(mk)
			mi := &MethodInfo{Side: "client", Method: wire}
			if wire == "session/update" {
				mi.Notif = "SessionNotification"
			} else {
				if _, ok := schema.Defs[base+"Request"]; ok {
					mi.Req = base + "Request"
				}
				if _, ok := schema.Defs[base+"Response"]; ok {
					mi.Resp = base + "Response"
				}
			}
			if mi.Req != "" || mi.Notif != "" {
				groups[k] = mi
			}
		}
	}
	// Post-process names, interfaces and docs-ignore
	for _, mi := range groups {
		mi.Name = methodName(mi)
		mi.Optional = optionalInterface(schema, mi)
		mi.DocsIgnored = isDocsIgnoredMethod(schema, mi)
	}
	return groups
}

// methodName returns the Go method that handles a wire method: the
// notification or request type name without its suffix, with two names fixed
// by the SDK's first release.
func methodName(mi *MethodInfo) string {
	switch {
	case mi.Method == "session/update":
		return "SessionUpdate"
	case mi.Method == "session/cancel":
		return "Cancel"
	case mi.Notif != "":
		return strings.TrimSuffix(mi.Notif, "Notification")
	default:
		return strings.TrimSuffix(mi.Req, "Request")
	}
}

// optionalInterface returns the name of the method's own optional interface,
// or "" for a stable method of the frozen Agent and Client interfaces.
func optionalInterface(schema *load.Schema, mi *MethodInfo) string {
	k := key(mi.Side, mi.Method)
	if requiredMethods[k] && !isUnstableMethod(schema, mi) {
		return ""
	}
	if name := optionalNames[k]; name != "" {
		return name
	}
	return util.TitleWord(mi.Side) + mi.Name
}

// isDocsIgnoredMethod if any associated type (req/resp/notif) marked x-docs-ignore.
func isDocsIgnoredMethod(schema *load.Schema, mi *MethodInfo) bool {
	if mi == nil {
		return false
	}
	if mi.Req != "" {
		if d := schema.Defs[mi.Req]; d != nil && d.DocsIgnore {
			return true
		}
	}
	if mi.Resp != "" {
		if d := schema.Defs[mi.Resp]; d != nil && d.DocsIgnore {
			return true
		}
	}
	if mi.Notif != "" {
		if d := schema.Defs[mi.Notif]; d != nil && d.DocsIgnore {
			return true
		}
	}
	return false
}

// isUnstableMethod returns true if any associated Request/Response/Notification
// definition is marked as unstable in its description.
func isUnstableMethod(schema *load.Schema, mi *MethodInfo) bool {
	if mi == nil {
		return false
	}

	// Any method whose public Go types are emitted with an "Unstable" prefix is
	// considered unstable, regardless of description text.
	if strings.HasPrefix(mi.Req, "Unstable") || strings.HasPrefix(mi.Resp, "Unstable") || strings.HasPrefix(mi.Notif, "Unstable") {
		return true
	}

	has := func(name string) bool {
		if name == "" {
			return false
		}
		if d := schema.Defs[name]; d != nil {
			desc := strings.ToLower(d.Description)
			if strings.Contains(desc, "unstable") {
				return true
			}
		}
		return false
	}
	return has(mi.Req) || has(mi.Resp) || has(mi.Notif)
}

// inferTypeBaseFromMethodKey mirrors previous heuristic; prefer schema when available.
func inferTypeBaseFromMethodKey(methodKey string) string {
	if methodKey == "terminal_wait_for_exit" {
		return "WaitForTerminalExit"
	}
	parts := strings.Split(methodKey, "_")
	if len(parts) == 2 {
		n, v := parts[0], parts[1]
		switch v {
		case "new", "create", "release", "wait", "load", "authenticate", "prompt", "cancel", "read", "write":
			return util.TitleWord(v) + util.TitleWord(n)
		default:
			return util.TitleWord(n) + util.TitleWord(v)
		}
	}
	segs := strings.Split(methodKey, "_")
	for i := range segs {
		segs[i] = util.TitleWord(segs[i])
	}
	return strings.Join(segs, "")
}

// SortedKeys returns sorted keys of a map.
func SortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
