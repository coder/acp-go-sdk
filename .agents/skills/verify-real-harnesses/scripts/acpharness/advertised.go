package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// The scenarios decide what an agent advertises from the raw JSON that
// crossed the wire, not from the SDK's decoded types: those types are the
// code under test. When the two disagree, the scenario fails with a decode
// mismatch instead of skipping a method that the agent offers.

// A capability is something that an agent advertises in its initialize
// result and that a scenario needs. path is where the raw result has it,
// and decoded reads the same thing from the SDK's types.
type capability struct {
	path    string
	decoded func(acp.AgentCapabilities) bool
}

var (
	capLoadSession = capability{"agentCapabilities.loadSession",
		func(c acp.AgentCapabilities) bool { return c.LoadSession }}
	capList = capability{"agentCapabilities.sessionCapabilities.list",
		func(c acp.AgentCapabilities) bool { return c.SessionCapabilities.List != nil }}
	capClose = capability{"agentCapabilities.sessionCapabilities.close",
		func(c acp.AgentCapabilities) bool { return c.SessionCapabilities.Close != nil }}
	capDelete = capability{"agentCapabilities.sessionCapabilities.delete",
		func(c acp.AgentCapabilities) bool { return c.SessionCapabilities.Delete != nil }}
	capResume = capability{"agentCapabilities.sessionCapabilities.resume",
		func(c acp.AgentCapabilities) bool { return c.SessionCapabilities.Resume != nil }}
)

// advertises reports whether the agent on a advertises c in its raw
// initialize result: the value is there and is neither null nor false. It
// returns an error when the SDK decoded c otherwise.
func (a *agentConn) advertises(c capability) (bool, error) {
	if a.rawInit == nil {
		return false, fmt.Errorf("no initialize result was recorded on connection %d", a.n)
	}
	v := jsonAt(a.rawInit, strings.Split(c.path, ".")...)
	wire := v != nil && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte("false"))
	if decoded := c.decoded(a.init.AgentCapabilities); decoded != wire {
		got := "absent"
		if decoded {
			got = "advertised"
		}
		return wire, fmt.Errorf("decode mismatch on connection %d: %s is %s in the initialize result on the wire, but the SDK decoded it as %s",
			a.n, c.path, wireText(v), got)
	}
	return wire, nil
}

// need returns the SKIP or FAIL that a scenario must return when the agent
// on a does not advertise c, or when the SDK decoded c otherwise. It returns
// an empty status when the agent advertises c.
func need(a *agentConn, c capability) (status, string) {
	on, err := a.advertises(c)
	switch {
	case err != nil:
		return fail, err.Error()
	case !on:
		return skip, skipNotAdvertised + ": the initialize result on the wire has no " + c.path
	}
	return "", ""
}

// jsonAt returns the raw value at a path of object keys, or nil when a key
// on the path is missing.
func jsonAt(raw json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		v, ok := obj[k]
		if !ok {
			return nil
		}
		raw = v
	}
	return bytes.TrimSpace(raw)
}

func wireText(v json.RawMessage) string {
	if v == nil {
		return "missing"
	}
	return clip(string(v), 80)
}

// selectOption is a select config option reduced to what the scenarios
// use: its id, its current value and the values it offers, in order.
type selectOption struct {
	ID           string
	CurrentValue string
	Values       []string
}

func (s *selectOption) String() string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("%s=%s with values %s", s.ID, s.CurrentValue, clip(fmt.Sprint(s.Values), 120))
}

// wireSelect reads the first select option whose category is category or
// whose id is id from the configOptions of a raw result, or nil.
func wireSelect(raw json.RawMessage, category, id string) *selectOption {
	var opts []struct {
		Type         string            `json:"type"`
		ID           string            `json:"id"`
		Category     string            `json:"category"`
		CurrentValue json.RawMessage   `json:"currentValue"`
		Options      []json.RawMessage `json:"options"`
	}
	if json.Unmarshal(jsonAt(raw, "configOptions"), &opts) != nil {
		return nil
	}
	for _, o := range opts {
		if o.Type != "select" || (o.Category != category && o.ID != id) {
			continue
		}
		s := &selectOption{ID: o.ID}
		_ = json.Unmarshal(o.CurrentValue, &s.CurrentValue)
		// An entry is an option with a value, or a group of options.
		for _, e := range o.Options {
			var entry struct {
				Value   *string `json:"value"`
				Options []struct {
					Value string `json:"value"`
				} `json:"options"`
			}
			_ = json.Unmarshal(e, &entry)
			if entry.Value != nil {
				s.Values = append(s.Values, *entry.Value)
			}
			for _, g := range entry.Options {
				s.Values = append(s.Values, g.Value)
			}
		}
		return s
	}
	return nil
}

// configSelect returns the decoded select option with the category or id
// from a session's config options, or nil when the raw result that carried
// them has none. It returns an error when the SDK decoded the option
// otherwise than the wire has it.
func configSelect(raw json.RawMessage, cfg []acp.SessionConfigOption, category acp.SessionConfigOptionCategory, id string) (*acp.SessionConfigOptionSelect, error) {
	if raw == nil {
		return nil, fmt.Errorf("no raw result with the config options was recorded")
	}
	wire := wireSelect(raw, string(category), id)
	sel := findSelect(cfg, category, id)
	var decoded *selectOption
	if sel != nil {
		decoded = &selectOption{ID: string(sel.Id), CurrentValue: string(sel.CurrentValue)}
		for _, o := range selectOptions(sel) {
			decoded.Values = append(decoded.Values, string(o.Value))
		}
	}
	if !reflect.DeepEqual(wire, decoded) {
		return nil, fmt.Errorf("decode mismatch: the %s select option on the wire is %v, but the SDK decoded %v", id, wire, decoded)
	}
	return sel, nil
}
