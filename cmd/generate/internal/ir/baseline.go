package ir

// requiredMethods holds the wire methods of the Agent and Client interfaces,
// keyed by side|method. It is frozen at the stable methods of ACP schema 1.25.0
// (SDK v0.17.0), less session/load, which has always had its own interface.
//
// Do not add a method. Every other method, unstable or stable, gets its own
// optional interface, which the connection type-asserts at dispatch. So a
// schema release that adds a method or makes one stable adds no method that
// every agent or client must write. A method listed here that is unstable or
// absent in a schema is left out of the interface.
var requiredMethods = map[string]bool{
	"agent|authenticate":              true,
	"agent|initialize":                true,
	"agent|logout":                    true,
	"agent|session/cancel":            true,
	"agent|session/close":             true,
	"agent|session/delete":            true,
	"agent|session/list":              true,
	"agent|session/new":               true,
	"agent|session/prompt":            true,
	"agent|session/resume":            true,
	"agent|session/set_config_option": true,
	"agent|session/set_mode":          true,

	"client|elicitation/complete":       true,
	"client|elicitation/create":         true,
	"client|fs/read_text_file":          true,
	"client|fs/write_text_file":         true,
	"client|session/request_permission": true,
	"client|session/update":             true,
	"client|terminal/create":            true,
	"client|terminal/kill":              true,
	"client|terminal/output":            true,
	"client|terminal/release":           true,
	"client|terminal/wait_for_exit":     true,
}

// optionalNames keeps the names of optional interfaces that are older than
// the rule <Side><Method>: AgentLoader, which the rule would call
// AgentLoadSession.
var optionalNames = map[string]string{
	"agent|session/load": "AgentLoader",
}
