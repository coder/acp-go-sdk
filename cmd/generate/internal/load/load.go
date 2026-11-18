// Package load provides utilities to read the ACP JSON schema and
// accompanying metadata into minimal structures used by the generator.
package load

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Meta mirrors schema/meta.json for method maps and version.
type Meta struct {
	Version int `json:"version"`

	AgentMethods    map[string]string `json:"agentMethods"`
	ClientMethods   map[string]string `json:"clientMethods"`
	ProtocolMethods map[string]string `json:"protocolMethods,omitempty"`
}

// Schema is a minimal view over schema/schema.json definitions used by the generator.
type Schema struct {
	Defs map[string]*Definition `json:"$defs"`
}

// Discriminator specifies which property distinguishes union variants.
// Part of JSON Schema discriminator object support.
type Discriminator struct {
	PropertyName string `json:"propertyName"`
}

// Definition is a partial JSON Schema node the generator cares about.
type Definition struct {
	Description string                 `json:"description"`
	Type        any                    `json:"type"`
	Properties  map[string]*Definition `json:"properties"`
	Required    []string               `json:"required"`
	Enum        []any                  `json:"enum"`
	// EnumNotes maps an enum value to the doc comment of its constant. The
	// schema has no such keyword: the merge sets it for the values that only
	// the unstable schema has (MergeStableAndUnstable).
	EnumNotes  map[string]string `json:"-"`
	Items      *Definition       `json:"items"`
	Ref        string            `json:"$ref"`
	AnyOf      []*Definition     `json:"anyOf"`
	OneOf      []*Definition     `json:"oneOf"`
	AllOf      []*Definition     `json:"allOf"`
	DocsIgnore bool              `json:"x-docs-ignore"`
	Title      string            `json:"title"`
	Const      any               `json:"const"`
	XSide      string            `json:"x-side"`
	XMethod    string            `json:"x-method"`
	// Format is the format keyword, such as "int64" for an integer that must
	// hold any 64-bit value.
	Format string `json:"format"`
	// Default holds the JSON Schema default value, when present.
	// Used by generators to synthesize defaulting behavior.
	Default any `json:"default"`
	// Discriminator specifies which property name distinguishes union variants.
	// Part of JSON Schema's discriminator object support.
	Discriminator *Discriminator `json:"discriminator,omitempty"`
	// The x-deserialize-* hints ask a reader to tolerate a bad value of this
	// property instead of failing the message: read a value of the wrong shape
	// as the default, drop array items of the wrong shape, read null as the
	// default.
	DefaultOnError   bool `json:"x-deserialize-default-on-error"`
	SkipInvalidItems bool `json:"x-deserialize-skip-invalid-items"`
	DefaultOnNull    bool `json:"x-deserialize-default-on-null"`
	// AdditionalProperties is the additionalProperties keyword: true, or the
	// schema of each member that Properties does not name.
	AdditionalProperties Members `json:"additionalProperties"`
	// Not is a schema that a value must not match. The catch-all variant of
	// a union uses it to leave out the discriminator values of the others.
	Not *Definition `json:"not"`
	// UnevaluatedProperties is set when the schema says
	// "unevaluatedProperties": true: the object takes members that no part of
	// the schema names, its anyOf alternatives included.
	UnevaluatedProperties isTrue `json:"unevaluatedProperties"`

	// boolSchema records whether this definition was a boolean schema (true/false).
	// JSON Schema allows boolean schemas, where true matches anything and false matches nothing.
	// We ignore the semantic difference in codegen and treat both as permissive/unknown shapes.
	boolSchema *bool `json:"-"`
}

// Members is a keyword, such as additionalProperties, that applies to the
// members of an object that its properties do not name.
type Members struct {
	// Open is set by the literal true: any member is allowed.
	Open bool
	// Schema is set by a schema, which each such member must match.
	Schema *Definition
}

// UnmarshalJSON reads true as Open and an object as Schema. Any other value,
// false included, leaves both unset.
func (m *Members) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*m = Members{Open: bytes.Equal(b, []byte("true"))}
	if bytes.HasPrefix(b, []byte("{")) {
		m.Schema = new(Definition)
		return json.Unmarshal(b, m.Schema)
	}
	return nil
}

// isTrue decodes the JSON literal true as true and any other value as false.
type isTrue bool

func (t *isTrue) UnmarshalJSON(b []byte) error {
	*t = isTrue(bytes.Equal(bytes.TrimSpace(b), []byte("true")))
	return nil
}

// UnmarshalJSON allows Definition to decode both object and boolean JSON Schema forms.
func (d *Definition) UnmarshalJSON(b []byte) error {
	// Trim whitespace for simple equality checks
	tb := bytes.TrimSpace(b)
	if bytes.Equal(tb, []byte("true")) || bytes.Equal(tb, []byte("false")) {
		v := bytes.Equal(tb, []byte("true"))
		// Reset to zero-value and record that this was a boolean schema.
		*d = Definition{}
		d.boolSchema = &v
		return nil
	}
	// Fallback to normal object decoding
	type alias Definition
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*d = Definition(a)
	return nil
}

// ReadMeta loads schema/meta.json.
func ReadMeta(schemaDir string) (*Meta, error) {
	metaBytes, err := os.ReadFile(filepath.Join(schemaDir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("read meta.json: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("parse meta.json: %w", err)
	}
	return &meta, nil
}

// ReadSchemaVersion loads schema/version, the ACP schema release that the
// schema files come from.
func ReadSchemaVersion(schemaDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(schemaDir, "version"))
	if err != nil {
		return "", fmt.Errorf("read version: %w", err)
	}
	version := strings.TrimSpace(string(b))
	if version == "" {
		return "", fmt.Errorf("read version: %s is empty", filepath.Join(schemaDir, "version"))
	}
	return version, nil
}

// ReadSchema loads schema/schema.json.
func ReadSchema(schemaDir string) (*Schema, error) {
	schemaBytes, err := os.ReadFile(filepath.Join(schemaDir, "schema.json"))
	if err != nil {
		return nil, fmt.Errorf("read schema.json: %w", err)
	}
	var schema Schema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return nil, fmt.Errorf("parse schema.json: %w", err)
	}
	return &schema, nil
}

// ReadMetaUnstable loads schema/meta.unstable.json when present.
// The returned boolean indicates whether the file was found.
func ReadMetaUnstable(schemaDir string) (*Meta, bool, error) {
	metaBytes, err := os.ReadFile(filepath.Join(schemaDir, "meta.unstable.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read meta.unstable.json: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, true, fmt.Errorf("parse meta.unstable.json: %w", err)
	}
	return &meta, true, nil
}

// ReadSchemaUnstable loads schema/schema.unstable.json when present.
// The returned boolean indicates whether the file was found.
func ReadSchemaUnstable(schemaDir string) (*Schema, bool, error) {
	schemaBytes, err := os.ReadFile(filepath.Join(schemaDir, "schema.unstable.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read schema.unstable.json: %w", err)
	}
	var schema Schema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return nil, true, fmt.Errorf("parse schema.unstable.json: %w", err)
	}
	return &schema, true, nil
}
