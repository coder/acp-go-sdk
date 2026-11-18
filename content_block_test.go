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
	block.Text.Meta = Meta{"trace": json.RawMessage(`"abc"`)}
	got, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if ok, got, want := equalJSON(got, []byte(`{"type":"text","text":"hi","_meta":{"trace":"abc"}}`)); !ok {
		t.Fatalf("text block with _meta encodes to %s, want %s", got, want)
	}
}

func TestContentBlock_MarshalJSON_KeepsEveryDigitOfAnInteger(t *testing.T) {
	t.Parallel()
	const in = `{"name":"n","size":9007199254740993,"type":"resource_link","uri":"u"}`
	var block ContentBlock
	if err := json.Unmarshal([]byte(in), &block); err != nil {
		t.Fatalf("unmarshal %s: %v", in, err)
	}
	if out, err := json.Marshal(block); err != nil || string(out) != in {
		t.Errorf("%s decodes and encodes to %s (error %v), want it unchanged", in, out, err)
	}
}

func TestContentBlock_UnmarshalJSON_ReusedValueHoldsOnlyTheLastVariant(t *testing.T) {
	t.Parallel()
	var block ContentBlock
	image := `{"type":"image","data":"aGk=","mimeType":"image/png"}`
	for _, in := range []string{`{"type":"text","text":"hi"}`, image} {
		if err := json.Unmarshal([]byte(in), &block); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
	}
	if block.Text != nil || block.Image == nil {
		t.Fatalf("after a text block and then an image block: text = %+v, image = %+v, want only the image", block.Text, block.Image)
	}
	got, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if ok, got, want := equalJSON(got, []byte(image)); !ok {
		t.Fatalf("reused block encodes to %s, want %s", got, want)
	}
}
