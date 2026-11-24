package main

import (
	"github.com/coder/acp-go-sdk"
)

// The session modes, and the config options: a model select, and a web
// search boolean that only a client accepting booleans sees.
const (
	modeAsk  acp.SessionModeId = "ask"
	modeCode acp.SessionModeId = "code"

	configModel     acp.SessionConfigId      = "model"
	configWebSearch acp.SessionConfigId      = "web_search"
	modelFast       acp.SessionConfigValueId = "tck-fast"
	modelDeep       acp.SessionConfigValueId = "tck-deep"
)

var modes = []acp.SessionMode{{Id: modeAsk, Name: "Ask"}, {Id: modeCode, Name: "Code"}}

var models = acp.SessionConfigSelectOptionsUngrouped{{Value: modelFast, Name: "Fast"}, {Value: modelDeep, Name: "Deep"}}

func (s *session) modes() *acp.SessionModeState {
	return &acp.SessionModeState{CurrentModeId: s.mode, AvailableModes: modes}
}

func (s *session) configOptions(booleans bool) []acp.SessionConfigOption {
	opts := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id:           configModel,
		Name:         "Model",
		Category:     acp.Ptr(acp.SessionConfigOptionCategoryModel),
		Type:         "select",
		CurrentValue: s.model,
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &models},
	}}}
	if booleans {
		opts = append(opts, acp.SessionConfigOption{Boolean: &acp.SessionConfigOptionBoolean{
			Id:           configWebSearch,
			Name:         "Web search",
			Category:     acp.Ptr(acp.SessionConfigOptionCategoryModelConfig),
			Type:         "boolean",
			CurrentValue: s.webSearch,
		}})
	}
	return opts
}

// setValue sets a select option. The caller holds a.mu.
func (s *session) setValue(id acp.SessionConfigId, v acp.SessionConfigValueId) error {
	if id != configModel {
		return invalid("no select option with this id", "configId", id)
	}
	for _, m := range models {
		if m.Value == v {
			s.model = v
			return nil
		}
	}
	return invalid("unknown value", "configId", id, "value", v)
}

// setBoolean sets a boolean option, which exists only for a client that
// accepts booleans. The caller holds a.mu.
func (s *session) setBoolean(id acp.SessionConfigId, v, booleans bool) error {
	if id != configWebSearch || !booleans {
		return invalid("no boolean option with this id", "configId", id)
	}
	s.webSearch = v
	return nil
}

func initialID(e acp.InitialSessionConfigOption) acp.SessionConfigId {
	if e.Boolean != nil {
		return e.Boolean.ConfigId
	}
	return e.ValueId.ConfigId
}

// checkInitialConfig rejects initial config values that name an option
// twice. The SDK has already rejected an entry of the wrong shape.
func checkInitialConfig(entries []acp.InitialSessionConfigOption) error {
	seen := make(map[acp.SessionConfigId]bool, len(entries))
	for _, e := range entries {
		id := initialID(e)
		if seen[id] {
			return invalid("configOptions names an option twice", "configId", id)
		}
		seen[id] = true
	}
	return nil
}

// applyInitialConfig applies the initial config values the client sent,
// after checkInitialConfig. It skips an entry the session cannot apply (an
// unknown option, a value the option does not offer, or a value of the
// other type), because a client may send values from another agent. The
// caller holds a.mu.
func applyInitialConfig(s *session, entries []acp.InitialSessionConfigOption, booleans bool) {
	for _, e := range entries {
		if e.Boolean != nil {
			_ = s.setBoolean(e.Boolean.ConfigId, e.Boolean.Value, booleans)
		} else {
			_ = s.setValue(e.ValueId.ConfigId, e.ValueId.Value)
		}
	}
}
