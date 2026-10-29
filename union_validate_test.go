package acp

import "testing"

func TestUnionValidateAnyOfRequiresAtLeastOneVariant(t *testing.T) {
	var empty SetSessionConfigOptionRequest
	if err := empty.Validate(); err == nil {
		t.Fatal("expected an error for an anyOf union with no variant set")
	}

	set := SetSessionConfigOptionRequest{ValueId: &SetSessionConfigOptionValueId{}}
	if err := set.Validate(); err != nil {
		t.Fatalf("expected no error for a single variant, got %v", err)
	}
}

func TestUnionValidateOneOfRequiresExactlyOneVariant(t *testing.T) {
	var empty ContentBlock
	if err := empty.Validate(); err == nil {
		t.Fatal("expected an error for a oneOf union with no variant set")
	}

	both := ContentBlock{
		Text:  &ContentBlockText{Text: "a"},
		Image: &ContentBlockImage{},
	}
	if err := both.Validate(); err == nil {
		t.Fatal("expected an error for a oneOf union with two variants set")
	}
}

// An empty object deserializes into a variant, so a wire payload always validates.
func TestUnionValidateAcceptsUnmarshalledPayload(t *testing.T) {
	var req SetSessionConfigOptionRequest
	if err := req.UnmarshalJSON([]byte(`{}`)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("expected an unmarshalled payload to validate, got %v", err)
	}
}
