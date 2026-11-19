package acp

import (
	"encoding/json"
	"testing"
)

// The required members of a reused value must be in the JSON again: a
// value that the field holds from before does not count.
func TestUsageUpdate_UnmarshalJSON_RequiresMembersOfAReusedValue(t *testing.T) {
	for _, in := range []string{`{}`, `{"size":null,"used":2}`, `{"used":2}`} {
		u := UsageUpdate{Size: 10, Used: 5}
		if err := json.Unmarshal([]byte(in), &u); err == nil {
			t.Errorf("%s into a reused UsageUpdate decodes to %+v, want an error", in, u)
		}
	}
	u := UsageUpdate{Size: 10, Used: 5}
	if err := json.Unmarshal([]byte(`{"size":0,"used":2}`), &u); err != nil || u.Size != 0 || u.Used != 2 {
		t.Errorf(`{"size":0,"used":2} into a reused UsageUpdate decodes to %+v (error %v), want size 0 and used 2`, u, err)
	}
}
