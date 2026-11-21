package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSubagentUpdate_UnmarshalJSON_ReadsCatchAllStatesAsTheRustCrate decodes
// values of the catch-all variants of StateUpdate and IdleStateUpdate, whose
// schemas leave out the other discriminator values with a not. Each want is
// what agent-client-protocol-schema 1.18.0 (schema 1.32.0) encodes after it
// decodes the value: the rust-diff probe of verify-schema-bump printed it.
// state is hinted with x-deserialize-default-on-error, so a state that fits
// no variant reads as absent.
func TestSubagentUpdate_UnmarshalJSON_ReadsCatchAllStatesAsTheRustCrate(t *testing.T) {
	tests := []struct {
		name     string
		into     any
		in, want string
	}{
		{"state-other", new(SubagentUpdate), `{"sessionId":"s","state":{"state":"paused","reason":"x"}}`, `{"sessionId":"s","state":{"state":"paused","reason":"x"}}`},
		{"state-reserved", new(SubagentUpdate), `{"sessionId":"s","state":{"state":"_vendor","n":1}}`, `{"sessionId":"s","state":{"state":"_vendor","n":1}}`},
		{"state-running", new(SubagentUpdate), `{"sessionId":"s","state":{"state":"running"}}`, `{"sessionId":"s","state":{"state":"running"}}`},
		{"state-idle-end-turn", new(SubagentUpdate), `{"sessionId":"s","state":{"state":"idle","stopReason":"end_turn"}}`, `{"sessionId":"s","state":{"state":"idle","stopReason":"end_turn"}}`},
		{"state-idle-other-stop", new(SubagentUpdate), `{"sessionId":"s","state":{"state":"idle","stopReason":"_custom","note":1}}`, `{"sessionId":"s","state":{"state":"idle","stopReason":"_custom","note":1}}`},
		{"state-not-string", new(SubagentUpdate), `{"sessionId":"s","state":{"state":5}}`, `{"sessionId":"s"}`},
		{"state-missing", new(SubagentUpdate), `{"sessionId":"s","state":{"reason":"x"}}`, `{"sessionId":"s"}`},
		{"idle-other-stop", new(IdleStateUpdate), `{"stopReason":"_custom","extra":1}`, `{"stopReason":"_custom","extra":1}`},
		{"state-update-other", new(StateUpdate), `{"state":"paused","reason":"x"}`, `{"state":"paused","reason":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.in), tt.into); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.in, err)
			}
			out, err := json.Marshal(tt.into)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got, want any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s decoded to %s, want %s", tt.in, out, tt.want)
			}
		})
	}
}
