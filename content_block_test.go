package acp

import (
	"encoding/json"
	"testing"
)

func TestContentBlock_MarshalJSON_KeepsMetaAndAnnotations(t *testing.T) {
	t.Parallel()
	const extras = `"_meta":{"trace":"abc"},"annotations":{"audience":["user"],"priority":0.5}`
	tests := []struct {
		name string
		json string
	}{
		{"text", `{"type":"text","text":"hi",` + extras + `}`},
		{"image", `{"type":"image","data":"aGk=","mimeType":"image/png","uri":"file:///a.png",` + extras + `}`},
		{"audio", `{"type":"audio","data":"aGk=","mimeType":"audio/wav",` + extras + `}`},
		{"resource_link", `{"type":"resource_link","name":"a","uri":"file:///a","size":3,` + extras + `}`},
		{"resource", `{"type":"resource","resource":{"uri":"file:///a","text":"hi"},` + extras + `}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var block ContentBlock
			if err := json.Unmarshal([]byte(tt.json), &block); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.json, err)
			}
			got, err := json.Marshal(block)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if ok, got, want := equalJSON(got, []byte(tt.json)); !ok {
				t.Fatalf("round trip changed the block\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestContentBlock_MarshalJSON_WritesMetaSetInGo(t *testing.T) {
	t.Parallel()
	block := TextBlock("hi")
	block.Text.Meta = map[string]any{"trace": "abc"}
	got, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if ok, got, want := equalJSON(got, []byte(`{"type":"text","text":"hi","_meta":{"trace":"abc"}}`)); !ok {
		t.Fatalf("text block with _meta encodes to %s, want %s", got, want)
	}
}
