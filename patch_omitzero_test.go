//go:build go1.24

package acp

import (
	"encoding/json"
	"testing"
)

// In a struct of a caller's own, the omitzero option (Go 1.24) leaves out an
// unset Patch, which omitempty cannot do for a struct.
func TestPatch_OmitzeroLeavesOutAnUnsetPatch(t *testing.T) {
	t.Parallel()
	type own struct {
		Unset Patch[string] `json:"unset,omitzero"`
		Null  Patch[string] `json:"null,omitzero"`
		Empty Patch[string] `json:"empty,omitzero"`
		Plain Patch[string] `json:"plain,omitempty"`
	}
	in := own{Null: PatchNull[string](), Empty: PatchSet("")}
	const want = `{"null":null,"empty":"","plain":null}`
	if out, err := json.Marshal(in); err != nil || string(out) != want {
		t.Errorf("struct encodes to %s (error %v), want %s", out, err, want)
	}
}
