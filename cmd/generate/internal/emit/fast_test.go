package emit

import "testing"

// fastSchema has a type for each rule that the exact decoders follow and
// that the schemas of ACP 1.18.0 through 1.33.0 reach only in part: members
// that hold their own type through a pointer (Tree, Kin), aliases of a
// struct and of json.RawMessage (InnerAlias, BlobAlias), union alternatives
// that are integers with consts, a number enum, a boolean const, and a
// string (Code, Level), integer alternatives of each format tried in order
// (Small), an alternative that is a $ref with a const of its own (Pick),
// the Go type of each integer format (Ints), and patch fields that read null
// as absent (Update, as nullAsAbsentSchema has it).
const fastSchema = `{"$defs": {
  "Tree": {"type": "object", "required": ["value"], "properties": {
    "value": {"type": "integer", "format": "int32", "minimum": 0},
    "left": {"$ref": "#/$defs/Tree"},
    "right": {"description": "The right subtree.", "allOf": [{"$ref": "#/$defs/Tree"}]},
    "middle": {"$ref": "#/$defs/Tree", "x-deserialize-default-on-error": true}}},
  "Kin": {"type": "object", "required": ["kid", "kin", "age"], "properties": {
    "age": {"type": "integer", "format": "uint16"},
    "kid": {"$ref": "#/$defs/Kin", "x-deserialize-default-on-error": true},
    "kin": {"$ref": "#/$defs/Kin", "x-deserialize-default-on-null": true}}},
  "Inner": {"type": "object", "properties": {"n": {"type": "integer", "format": "uint64"}}},
  "InnerAlias": {"description": "Another name of Inner.", "allOf": [{"$ref": "#/$defs/Inner"}]},
  "Blob": {"description": "Any JSON value."},
  "BlobAlias": {"allOf": [{"$ref": "#/$defs/Blob"}]},
  "Holder": {"type": "object", "properties": {
    "inner": {"$ref": "#/$defs/InnerAlias"},
    "blob": {"$ref": "#/$defs/BlobAlias"},
    "code": {"$ref": "#/$defs/Code"},
    "level": {"$ref": "#/$defs/Level", "x-deserialize-default-on-error": true}}},
  "Code": {"anyOf": [
    {"title": "Parse", "type": "integer", "format": "int32", "const": -32700},
    {"title": "Cancelled", "type": "integer", "format": "int32", "const": -32800},
    {"title": "Other", "type": "integer", "format": "int32"}]},
  "Level": {"anyOf": [
    {"title": "Low", "const": "low"},
    {"title": "Half", "type": "number", "enum": [0.5, 1.5]},
    {"title": "On", "const": true},
    {"title": "Word", "type": "string"}]},
  "Small": {"oneOf": [
    {"title": "Short", "type": "integer", "format": "uint16"},
    {"title": "Wide", "type": "integer", "format": "uint64"},
    {"title": "Signed", "type": "integer", "format": "int64"}]},
  "Mode": {"type": "string"},
  "Pick": {"anyOf": [
    {"title": "Auto", "const": "auto", "allOf": [{"$ref": "#/$defs/Mode"}]},
    {"title": "Custom", "allOf": [{"$ref": "#/$defs/Mode"}]}]},
  "Ints": {"type": "object", "properties": {
    "a": {"type": "integer", "format": "int32"},
    "b": {"type": "integer", "format": "int64"},
    "c": {"type": "integer", "format": "uint16"},
    "d": {"type": "integer", "format": "uint32"},
    "e": {"type": "integer", "format": "uint64"},
    "f": {"type": "integer"},
    "g": {"type": ["integer", "null"], "format": "uint32", "minimum": 1},
    "h": {"type": "integer", "format": "uint32", "default": 7, "x-deserialize-default-on-error": true}}},
  "Status": {"type": "string", "enum": ["pending", "completed"]},
  "Update": {"description": "Only changed fields need to be included.", "type": "object", "required": ["id"], "properties": {
    "id": {"type": "string"},
    "title": {"type": ["string", "null"]},
    "status": {"anyOf": [{"$ref": "#/$defs/Status"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "tags": {"type": ["array", "null"], "items": {"type": "string"}, "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "note": {"type": ["string", "null"], "description": "Set to null to clear."}}}
}}`

const fastTest = `package acp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fastTypes are the types whose fast decoders the tests compare with the
// exact ones.
var fastTypes = []reflect.Type{
	reflect.TypeOf(Tree{}), reflect.TypeOf(Kin{}), reflect.TypeOf(Holder{}), reflect.TypeOf(InnerAlias{}),
	reflect.TypeOf(Code{}), reflect.TypeOf(Level{}), reflect.TypeOf(Small{}), reflect.TypeOf(Pick{}),
	reflect.TypeOf(Ints{}), reflect.TypeOf(Update{}),
}

// bases are objects that each type takes, which the inputs change a member of.
var bases = map[reflect.Type]string{
	reflect.TypeOf(Tree{}): ` + "`" + `{"value":1}` + "`" + `,
	reflect.TypeOf(Kin{}):  ` + "`" + `{"age":1,"kid":null,"kin":null}` + "`" + `,
	reflect.TypeOf(Update{}): ` + "`" + `{"id":"u"}` + "`" + `,
}

// values exercise the rules of fastSchema: null, the bounds of each integer
// format and one past them, integers that the Go integer types do not take
// (-0 into an unsigned type, 1e0, 1.5), the consts of the unions, and values
// of the other kinds.
var values = []string{
	"null", "true", "false", ` + "`" + `""` + "`" + `, ` + "`" + `"low"` + "`" + `, ` + "`" + `"auto"` + "`" + `, ` + "`" + `"pending"` + "`" + `, ` + "`" + `"x"` + "`" + `,
	"0", "-0", "1", "-1", "1e0", "1E2", "1.5", "0.5", "-0.0", "7", "-32700", "-32800", "-32700.0", "-3.27e4",
	"65535", "65536", "2147483647", "2147483648", "-2147483648", "-2147483649", "4294967295", "4294967296",
	"9223372036854775807", "9223372036854775808", "-9223372036854775808", "-9223372036854775809",
	"18446744073709551615", "18446744073709551616", "1e400",
	"[]", "[null]", ` + "`" + `["a",1,null]` + "`" + `, "{}", ` + "`" + `{"value":1}` + "`" + `, ` + "`" + `{"value":-1}` + "`" + `, ` + "`" + `{"value":1,"left":{"value":2}}` + "`" + `,
	` + "`" + `{"age":70000,"kid":null,"kin":null}` + "`" + `, ` + "`" + `{"n":18446744073709551615}` + "`" + `, ` + "`" + `{"n":-0}` + "`" + `,
}

var keys = []string{
	"value", "left", "right", "middle", "age", "kid", "kin", "n", "inner", "blob", "code", "level",
	"a", "b", "c", "d", "e", "f", "g", "h", "id", "title", "status", "tags", "note",
}

// inputs returns values, and for each key and value an object that holds
// the base of typ with that member, with the member once and twice.
func inputs(typ reflect.Type) []string {
	base := bases[typ]
	if base == "" {
		base = "{}"
	}
	with := func(member string) string {
		if base == "{}" {
			return "{" + member + "}"
		}
		return strings.TrimSuffix(base, "}") + "," + member + "}"
	}
	out := append([]string{base}, values...)
	for _, k := range keys {
		for _, v := range values {
			m := fmt.Sprintf("%q:%s", k, v)
			out = append(out, with(m), with(m+","+m), with(fmt.Sprintf("%q:null,", k)+m))
		}
	}
	return out
}

// compare decodes data into a fresh value of typ with the fast decoders on
// and off, and requires the same value, error, and encoding. It also calls
// the decodeFast of typ: when it takes data, it must leave what the exact
// decoder does. It reports whether decodeFast took data.
func compare(t *testing.T, typ reflect.Type, data string) bool {
	t.Helper()
	decode := func(on bool) (any, string, string) {
		fastDecode = on
		defer func() { fastDecode = true }()
		v := reflect.New(typ)
		msg := ""
		if err := json.Unmarshal([]byte(data), v.Interface()); err != nil {
			msg = err.Error()
		}
		out, err := json.Marshal(v.Interface())
		return v.Elem().Interface(), msg, fmt.Sprintf("%s %v", out, err)
	}
	fastValue, fastErr, fastOut := decode(true)
	exactValue, exactErr, exactOut := decode(false)
	if fastErr != exactErr || fastOut != exactOut || !reflect.DeepEqual(fastValue, exactValue) {
		t.Errorf("%v of %s: the fast decoders read %+v (error %q) and encode %s, the exact ones %+v (error %q) and %s",
			typ, data, fastValue, fastErr, fastOut, exactValue, exactErr, exactOut)
	}
	if !json.Valid([]byte(data)) || isSpace(data[0]) || isSpace(data[len(data)-1]) {
		return false
	}
	v := reflect.New(typ)
	if !v.Interface().(interface{ decodeFast([]byte) bool }).decodeFast([]byte(data)) {
		return false
	}
	if exactErr != "" || !reflect.DeepEqual(v.Elem().Interface(), exactValue) {
		t.Errorf("%v.decodeFast took %s as %+v, the exact decoder reads %+v (error %q)", typ, data, v.Elem().Interface(), exactValue, exactErr)
	}
	return true
}

func TestFastDecoders_ReadAsTheExactOnes(t *testing.T) {
	for _, typ := range fastTypes {
		for _, data := range inputs(typ) {
			compare(t, typ, data)
		}
	}
}

// TestFastDecoders_TakeWhatTheyImplement checks that a fast decoder reads
// these values itself rather than leave them to the exact one, so that the
// test above compares the rules of fastSchema in the fast decoders.
func TestFastDecoders_TakeWhatTheyImplement(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		data string
	}{
		{reflect.TypeOf(Tree{}), ` + "`" + `{"value":1,"left":{"value":2},"right":null,"middle":{"value":-1}}` + "`" + `},
		{reflect.TypeOf(Kin{}), ` + "`" + `{"age":65535,"kid":{"age":1,"kid":null,"kin":null},"kin":null}` + "`" + `},
		{reflect.TypeOf(Holder{}), ` + "`" + `{"inner":{"n":18446744073709551615},"blob":[1,{"a":null}],"code":-32800,"level":1.5}` + "`" + `},
		{reflect.TypeOf(Code{}), "-32700"}, {reflect.TypeOf(Code{}), "-0"}, {reflect.TypeOf(Code{}), "2147483647"},
		{reflect.TypeOf(Level{}), ` + "`" + `"low"` + "`" + `}, {reflect.TypeOf(Level{}), "0.5"}, {reflect.TypeOf(Level{}), "true"}, {reflect.TypeOf(Level{}), ` + "`" + `"high"` + "`" + `},
		{reflect.TypeOf(Small{}), "65536"}, {reflect.TypeOf(Small{}), "-1"},
		{reflect.TypeOf(Pick{}), ` + "`" + `"auto"` + "`" + `}, {reflect.TypeOf(Pick{}), ` + "`" + `"manual"` + "`" + `},
		{reflect.TypeOf(Ints{}), ` + "`" + `{"a":-2147483648,"b":-9223372036854775808,"c":65535,"d":4294967295,"e":18446744073709551615,"f":-1,"g":null,"h":-0}` + "`" + `},
		{reflect.TypeOf(Update{}), ` + "`" + `{"id":"u","title":null,"status":null,"tags":null,"note":null}` + "`" + `},
		{reflect.TypeOf(Update{}), ` + "`" + `{"id":"u","title":"t","status":"bogus","tags":["a",1],"note":"n"}` + "`" + `},
	} {
		if !compare(t, c.typ, c.data) {
			t.Errorf("%v.decodeFast declines %s", c.typ, c.data)
		}
	}
}

func FuzzFastDecoders(f *testing.F) {
	for i, typ := range fastTypes {
		f.Add([]byte(bases[typ]), uint8(i))
		for _, v := range values {
			f.Add([]byte(v), uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, pick uint8) {
		compare(t, fastTypes[int(pick)%len(fastTypes)], string(data))
	})
}
`

// TestWriteTypesJen_FastDecodersReadAsTheExactOnes compares the fast
// decoders of fastSchema with the exact ones, on a 64-bit and a 32-bit
// target, and checks that they read the values of each rule themselves.
func TestWriteTypesJen_FastDecodersReadAsTheExactOnes(t *testing.T) {
	for _, arch := range []string{"amd64", "386"} {
		t.Run(arch, func(t *testing.T) {
			checkGenerated(t, fastSchema, map[string]string{"fast_test.go": fastTest}, "GOARCH="+arch)
		})
	}
}
