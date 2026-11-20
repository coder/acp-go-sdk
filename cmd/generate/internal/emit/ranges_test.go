package emit

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// integerRangesSchema has integers of each format at strict sites: a named
// uint16 type through allOf, a required int32, optional uint32 and uint64
// pointers, uint32 items, map members of a named type whose minimum and
// maximum narrow its format, explicit bounds on a property, an int64, a
// nested struct, and a patch field. Spot.line, Spot.hits and the items of
// marks are lenient sites, and maybe is a lenient site that holds a struct
// with a uint64. tight, the items of caps and Spot.col put a bound next to a
// $ref or an allOf, which has no type of its own, nullcap and Spot.row next
// to a null alternative, and frac has bounds 0.5 and 1e1. least and
// Spot.hits put a minimum on a uint64, which has no maximum but its type's.
const integerRangesSchema = `{"$defs": {
  "Version": {"type": "integer", "format": "uint16", "minimum": 0, "maximum": 65535},
  "Level": {"type": "integer", "format": "int32", "minimum": 1, "maximum": 10},
  "Count": {"type": "object", "required": ["n"], "properties": {"n": {"type": "integer", "format": "uint64", "minimum": 0}}},
  "Spot": {"type": "object", "required": ["path"], "properties": {
    "path": {"type": "string"},
    "line": {"type": ["integer", "null"], "format": "uint32", "minimum": 0, "x-deserialize-default-on-error": true},
    "hits": {"type": ["integer", "null"], "format": "uint64", "minimum": 1, "x-deserialize-default-on-error": true},
    "col": {"$ref": "#/$defs/Version", "maximum": 10, "x-deserialize-default-on-error": true},
    "row": {"anyOf": [{"$ref": "#/$defs/Version"}, {"type": "null"}], "maximum": 10, "x-deserialize-default-on-error": true}}},
  "PingRequest": {"type": "object", "required": ["version", "offset", "count"], "properties": {
    "version": {"allOf": [{"$ref": "#/$defs/Version"}]},
    "offset": {"type": "integer", "format": "int32"},
    "line": {"type": ["integer", "null"], "format": "uint32", "minimum": 0},
    "size": {"type": ["integer", "null"], "format": "uint64", "minimum": 0},
    "least": {"type": ["integer", "null"], "format": "uint64", "minimum": 1},
    "lines": {"type": "array", "items": {"type": "integer", "format": "uint32", "minimum": 0}},
    "levels": {"type": "object", "additionalProperties": {"$ref": "#/$defs/Level"}},
    "narrow": {"type": ["integer", "null"], "format": "uint32", "minimum": 5, "maximum": 10},
    "frac": {"type": ["integer", "null"], "minimum": 0.5, "maximum": 1e1},
    "tight": {"anyOf": [{"$ref": "#/$defs/Version", "minimum": 1}, {"type": "null"}]},
    "caps": {"type": "array", "items": {"allOf": [{"$ref": "#/$defs/Version"}], "maximum": 10}},
    "nullcap": {"anyOf": [{"$ref": "#/$defs/Version"}, {"type": "null"}], "maximum": 10},
    "big": {"type": ["integer", "null"], "format": "int64"},
    "count": {"$ref": "#/$defs/Count"},
    "latest": {"type": ["integer", "null"], "format": "uint16", "description": "Omitted means unchanged. ` + "`null`" + ` clears it."},
    "spot": {"anyOf": [{"$ref": "#/$defs/Spot"}, {"type": "null"}]},
    "marks": {"type": "array", "items": {"type": "integer", "format": "uint16"}, "x-deserialize-skip-invalid-items": true},
    "maybe": {"anyOf": [{"$ref": "#/$defs/Count"}, {"type": "null"}], "x-deserialize-default-on-error": true}}}
}}`

const integerRangesTest = `package acp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The Go type of an integer holds the range of its format, so a value out of
// it fails to decode; Validate checks the minimum and maximum that narrow it.
func TestPingRequest_Validate_ChecksIntegerRanges(t *testing.T) {
	const valid = ` + "`" + `"version":1,"offset":0,"count":{"n":0}` + "`" + `
	with := func(members string) string { return "{" + valid + "," + members + "}" }
	tests := []struct {
		name, payload, wantErr string
	}{
		{"valid", "{" + valid + "}", ""},
		{"uint16 at its bounds", with(` + "`" + `"version":65535,"latest":0` + "`" + `), ""},
		{"uint16 above", with(` + "`" + `"version":65536` + "`" + `), "cannot unmarshal number 65536"},
		{"uint16 below", with(` + "`" + `"version":-1` + "`" + `), "cannot unmarshal number -1"},
		{"int32 at its bounds", with(` + "`" + `"offset":-2147483648` + "`" + `), ""},
		{"int32 above", with(` + "`" + `"offset":2147483648` + "`" + `), "cannot unmarshal number 2147483648"},
		{"int32 below", with(` + "`" + `"offset":-2147483649` + "`" + `), "cannot unmarshal number -2147483649"},
		{"uint32 pointer at its maximum", with(` + "`" + `"line":4294967295` + "`" + `), ""},
		{"uint32 pointer above", with(` + "`" + `"line":4294967296` + "`" + `), "cannot unmarshal number 4294967296"},
		{"uint32 pointer below", with(` + "`" + `"line":-1` + "`" + `), "cannot unmarshal number -1"},
		{"uint32 negative zero", with(` + "`" + `"line":-0` + "`" + `), "cannot unmarshal number -0"},
		{"optional pointer nil", with(` + "`" + `"line":null,"size":null,"narrow":null,"latest":null` + "`" + `), ""},
		{"uint64 at its maximum", with(` + "`" + `"size":18446744073709551615` + "`" + `), ""},
		{"uint64 above", with(` + "`" + `"size":18446744073709551616` + "`" + `), "cannot unmarshal number 18446744073709551616"},
		{"uint64 below", with(` + "`" + `"size":-1` + "`" + `), "cannot unmarshal number -1"},
		{"uint64 with a minimum at its maximum", with(` + "`" + `"least":18446744073709551615` + "`" + `), ""},
		{"uint64 below its minimum", with(` + "`" + `"least":0` + "`" + `), "least must be at least 1"},
		{"item out of range", with(` + "`" + `"lines":[0,-1]` + "`" + `), "cannot unmarshal number -1"},
		{"member of a named type with minimum and maximum", with(` + "`" + `"levels":{"a":1,"b":10}` + "`" + `), ""},
		{"member above the maximum of its type", with(` + "`" + `"levels":{"a":11}` + "`" + `), ` + "`" + `levels["a"] must be between 1 and 10` + "`" + `},
		{"member below the minimum of its type", with(` + "`" + `"levels":{"a":0}` + "`" + `), ` + "`" + `levels["a"] must be between 1 and 10` + "`" + `},
		{"explicit bounds", with(` + "`" + `"narrow":5` + "`" + `), ""},
		{"below explicit bounds", with(` + "`" + `"narrow":4` + "`" + `), "narrow must be between 5 and 10"},
		{"above explicit bounds", with(` + "`" + `"narrow":11` + "`" + `), "narrow must be between 5 and 10"},
		{"bounds that are not integer text", with(` + "`" + `"frac":1` + "`" + `), ""},
		{"below a fractional minimum", with(` + "`" + `"frac":0` + "`" + `), "frac must be between 1 and 10"},
		{"above a maximum in exponent form", with(` + "`" + `"frac":11` + "`" + `), "frac must be between 1 and 10"},
		{"bound beside a $ref", with(` + "`" + `"tight":1` + "`" + `), ""},
		{"below a bound beside a $ref", with(` + "`" + `"tight":0` + "`" + `), "tight must be at least 1"},
		{"bound next to a null alternative", with(` + "`" + `"nullcap":10` + "`" + `), ""},
		{"above a bound next to a null alternative", with(` + "`" + `"nullcap":11` + "`" + `), "nullcap must be at most 10"},
		{"item above a bound beside an allOf", with(` + "`" + `"caps":[10,11]` + "`" + `), "caps[1] must be at most 10"},
		{"int64 at its bounds", with(` + "`" + `"big":-9223372036854775808` + "`" + `), ""},
		{"nested struct", ` + "`" + `{"version":1,"offset":0,"count":{"n":-1}}` + "`" + `, "cannot unmarshal number -1"},
		{"patch above", with(` + "`" + `"latest":65536` + "`" + `), "cannot unmarshal number 65536"},
	}
	for _, tt := range tests {
		var req PingRequest
		err := json.Unmarshal([]byte(tt.payload), &req)
		if err == nil {
			err = req.Validate()
		}
		if got := errorText(err); tt.wantErr == "" && got != "" || !strings.Contains(got, tt.wantErr) {
			t.Errorf("%s: reading %s fails with %q, want %q", tt.name, tt.payload, got, tt.wantErr)
		}
	}
}

// At a lenient site an integer out of range reads as absent, or its item is
// dropped, as the Rust types read it, and Validate does not look at it.
func TestPingRequest_UnmarshalJSON_ReadsLenientIntegersOutOfRangeAsAbsent(t *testing.T) {
	var req PingRequest
	in := ` + "`" + `{"version":1,"offset":0,"count":{"n":0},"spot":{"path":"/p","line":-1},"marks":[1,65536,-1,65535],"maybe":{"n":-1}}` + "`" + `
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatal(err)
	}
	if req.Spot == nil || req.Spot.Path != "/p" || req.Spot.Line != nil {
		t.Errorf("spot = %+v, want /p without a line", req.Spot)
	}
	if len(req.Marks) != 2 || req.Marks[0] != 1 || req.Marks[1] != 65535 {
		t.Errorf("marks = %v, want [1 65535]", req.Marks)
	}
	if req.Maybe != nil {
		t.Errorf("maybe = %+v, want absent", req.Maybe)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	for _, line := range []string{"4294967296", "-4294967296", "-0", "1e2", "99999999999999999999"} {
		var spot Spot
		if err := json.Unmarshal([]byte(` + "`" + `{"path":"/p","line":` + "`" + `+line+` + "`" + `}` + "`" + `), &spot); err != nil || spot.Line != nil {
			t.Errorf("line %s reads as %v with error %v, want absent", line, spot.Line, err)
		}
	}
	for col, want := range map[string]Version{"10": 10, "11": 0} {
		var spot Spot
		if err := json.Unmarshal([]byte(` + "`" + `{"path":"/p","col":` + "`" + `+col+` + "`" + `}` + "`" + `), &spot); err != nil || spot.Col != want {
			t.Errorf("col %s beside its $ref reads as %v with error %v, want %v", col, spot.Col, err, want)
		}
	}
	for row, want := range map[string]string{"10": "10", "11": "absent"} {
		var spot Spot
		err := json.Unmarshal([]byte(` + "`" + `{"path":"/p","row":` + "`" + `+row+` + "`" + `}` + "`" + `), &spot)
		got := "absent"
		if spot.Row != nil {
			got = fmt.Sprint(*spot.Row)
		}
		if err != nil || got != want {
			t.Errorf("row %s next to a null alternative reads as %s with error %v, want %s", row, got, err, want)
		}
	}
	for hits, want := range map[string]string{"0": "absent", "1": "1", "9223372036854775808": "9223372036854775808", "18446744073709551615": "18446744073709551615"} {
		var spot Spot
		err := json.Unmarshal([]byte(` + "`" + `{"path":"/p","hits":` + "`" + `+hits+` + "`" + `}` + "`" + `), &spot)
		got := "absent"
		if spot.Hits != nil {
			got = fmt.Sprint(*spot.Hits)
		}
		if err != nil || got != want {
			t.Errorf("hits %s with minimum 1 reads as %s with error %v, want %s", hits, got, err, want)
		}
	}
	var spot Spot
	if err := json.Unmarshal([]byte(` + "`" + `{"path":"/p","line":4294967295}` + "`" + `), &spot); err != nil || spot.Line == nil || *spot.Line != 4294967295 {
		t.Errorf("line 4294967295 reads as %v with error %v, want it kept", spot.Line, err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"version":1,"offset":0,"count":{"n":0},"maybe":{"n":18446744073709551615}}` + "`" + `), &req); err != nil || req.Maybe == nil {
		t.Errorf("maybe with n at the largest uint64 = %+v with error %v, want it kept", req.Maybe, err)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
`

func TestWriteTypesJen_ValidateChecksIntegerRanges(t *testing.T) {
	checkGenerated(t, integerRangesSchema, map[string]string{"ranges_test.go": integerRangesTest})
}

// wideRangesSchema has integers without a format, whose Go type is int, with
// bounds that an int on a 32-bit target cannot hold.
const wideRangesSchema = `{"$defs": {
  "Plain": {"type": "integer"},
  "WideRequest": {"type": "object", "properties": {
    "high": {"type": ["integer", "null"], "maximum": 4294967295},
    "low": {"type": ["integer", "null"], "minimum": -4294967296},
    "count": {"anyOf": [{"$ref": "#/$defs/Plain", "maximum": 4294967296}, {"type": "null"}]}}}
}}`

const wideRangesTest = `package acp

import "testing"

func TestWideRequest_Validate_ChecksWideBounds(t *testing.T) {
	one, plain := 1, Plain(1)
	req := WideRequest{High: &one, Low: &one, Count: &plain}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}
`

// A bound that does not fit an int32 compiles on a 32-bit target too.
func TestWriteTypesJen_RangeChecksCompileOn32BitTargets(t *testing.T) {
	checkGenerated(t, wideRangesSchema, map[string]string{"wide_test.go": wideRangesTest})
	checkGenerated(t, wideRangesSchema, map[string]string{"wide_test.go": wideRangesTest}, "GOARCH=386")
}

// rawBoundSchema has a $defs entry with a $ref and a bound but no type, which
// classify makes an alias of json.RawMessage, so no integer check fits it.
const rawBoundSchema = `{"$defs": {
  "Plain": {"type": "integer"},
  "Mid": {"$ref": "#/$defs/Plain", "maximum": 100},
  "MidRequest": {"type": "object", "required": ["y"], "properties": {
    "y": {"$ref": "#/$defs/Mid"},
    "z": {"type": "array", "items": {"$ref": "#/$defs/Mid"}}}}
}}`

const rawBoundTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestMidRequest_Validate_TakesARawValue(t *testing.T) {
	var req MidRequest
	if err := json.Unmarshal([]byte(` + "`" + `{"y":5,"z":[1]}` + "`" + `), &req); err != nil {
		t.Fatal(err)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}
`

// A bound on a value whose Go type is raw JSON adds no range check, which
// would not compile.
func TestWriteTypesJen_RawJSONGetsNoRangeCheck(t *testing.T) {
	checkGenerated(t, rawBoundSchema, map[string]string{"raw_test.go": rawBoundTest})
}

// A format that formatTypes does not know stops the generator, so that a new
// one gets a Go type before a peer can send a value out of its range.
func TestWriteTypesJen_PanicsOnAnUnknownIntegerFormat(t *testing.T) {
	var schema load.Schema
	in := `{"$defs": {"PingRequest": {"type": "object", "properties": {"n": {"type": "integer", "format": "uint8"}}}}}`
	if err := json.Unmarshal([]byte(in), &schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), `"uint8"`) {
			t.Errorf("WriteTypesJen with format uint8 recovered %v, want a panic that names the format", r)
		}
	}()
	_ = WriteTypesJen(t.TempDir(), &schema, &load.Meta{})
}

// A bound that the Go type does not enforce and that an int64 cannot hold
// stops the generator: math.MaxInt64 stands for no bound in intRange.
func TestWriteTypesJen_PanicsOnABoundBeyondInt64(t *testing.T) {
	var schema load.Schema
	in := `{"$defs": {"PingRequest": {"type": "object", "properties": {"n": {"type": ["integer", "null"], "format": "uint64", "maximum": 9223372036854775808}}}}}`
	if err := json.Unmarshal([]byte(in), &schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "9223372036854775808") {
			t.Errorf("WriteTypesJen with maximum 2^63 on a uint64 recovered %v, want a panic that names the bound", r)
		}
	}()
	_ = WriteTypesJen(t.TempDir(), &schema, &load.Meta{})
}

// optionalIntegersSchema has optional integers that cannot be null, whose Go
// type is not a pointer, so an absent member reads as 0. count has no bound
// but its format's, level and offset have a minimum and a maximum that
// narrow their format, one that leaves out 0 and one that holds it, depth
// has such bounds and a default, and hint is lenient.
const optionalIntegersSchema = `{"$defs": {
  "TallyRequest": {"type": "object", "properties": {
    "count": {"type": "integer", "format": "uint32"},
    "depth": {"type": "integer", "format": "uint32", "minimum": 1, "maximum": 10, "default": 5},
    "level": {"type": "integer", "format": "uint32", "minimum": 1, "maximum": 10},
    "offset": {"type": "integer", "format": "int32", "minimum": -5, "maximum": 5},
    "hint": {"type": "integer", "format": "uint32", "maximum": 10, "x-deserialize-default-on-error": true}}}
}}`

const optionalIntegersTest = `package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

// An optional integer that cannot be null fails to decode out of the range of
// its format, and Validate checks its minimum and maximum when it is set.
// Absent reads as 0, or as the default, which Validate takes, and so does an
// explicit 0: the struct writes 0 as an absent member.
func TestTallyRequest_Validate_ChecksOptionalIntegers(t *testing.T) {
	tests := []struct {
		payload, wantErr string
	}{
		{"{}", ""},
		{` + "`" + `{"count":4294967295}` + "`" + `, ""},
		{` + "`" + `{"count":4294967296}` + "`" + `, "cannot unmarshal number 4294967296"},
		{` + "`" + `{"count":-1}` + "`" + `, "cannot unmarshal number -1"},
		{` + "`" + `{"level":1}` + "`" + `, ""},
		{` + "`" + `{"level":10}` + "`" + `, ""},
		{` + "`" + `{"level":11}` + "`" + `, "level must be between 1 and 10"},
		{` + "`" + `{"level":0}` + "`" + `, ""},
		{` + "`" + `{"offset":-5}` + "`" + `, ""},
		{` + "`" + `{"offset":-6}` + "`" + `, "offset must be between -5 and 5"},
		{` + "`" + `{"offset":6}` + "`" + `, "offset must be between -5 and 5"},
		{` + "`" + `{"depth":11}` + "`" + `, "depth must be between 1 and 10"},
		{` + "`" + `{"depth":0}` + "`" + `, ""},
	}
	for _, tt := range tests {
		var req TallyRequest
		err := json.Unmarshal([]byte(tt.payload), &req)
		if err == nil {
			err = req.Validate()
		}
		if got := errorText(err); tt.wantErr == "" && got != "" || !strings.Contains(got, tt.wantErr) {
			t.Errorf("reading %s fails with %q, want %q", tt.payload, got, tt.wantErr)
		}
	}
	var req TallyRequest
	if err := json.Unmarshal([]byte("{}"), &req); err != nil || req.Depth != 5 {
		t.Errorf("{} reads depth %d with error %v, want the default 5", req.Depth, err)
	}
	req = TallyRequest{}
	if err := json.Unmarshal([]byte(` + "`" + `{"hint":11}` + "`" + `), &req); err != nil || req.Hint != 0 {
		t.Errorf("hint 11 reads as %d with error %v, want absent", req.Hint, err)
	}
	if err := (&TallyRequest{Level: 11}).Validate(); err == nil {
		t.Error("Validate of a TallyRequest built with level 11 = nil, want an error")
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
`

// No integer of schema 1.18.0 through 1.33.0 is optional and not nullable,
// so this schema stands for one.
func TestWriteTypesJen_ValidateChecksOptionalIntegers(t *testing.T) {
	checkGenerated(t, optionalIntegersSchema, map[string]string{"optional_test.go": optionalIntegersTest})
}

// A default fills an absent member, so a default that its own bounds or its
// format leave out would make Validate reject a message that omits the
// member, or would not decode. Such a schema contradicts itself, and the
// generator stops on it.
func TestWriteTypesJen_PanicsOnADefaultOutsideItsBounds(t *testing.T) {
	for _, tt := range []struct{ name, prop string }{
		{"above the maximum", `{"type": "integer", "format": "int32", "minimum": 1, "maximum": 10, "default": 20}`},
		{"below the format", `{"type": "integer", "format": "uint32", "default": -1}`},
		{"not an integer", `{"type": "integer", "format": "int32", "default": 1.5}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var schema load.Schema
			in := `{"$defs": {"PingRequest": {"type": "object", "properties": {"level": ` + tt.prop + `}}}}`
			if err := json.Unmarshal([]byte(in), &schema); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), `"level"`) {
					t.Errorf("WriteTypesJen with level %s recovered %v, want a panic that names the property", tt.prop, r)
				}
			}()
			_ = WriteTypesJen(t.TempDir(), &schema, &load.Meta{})
		})
	}
}
