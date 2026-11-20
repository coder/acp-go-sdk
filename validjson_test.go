package acp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// nested returns n copies of open, then v, then n copies of close.
func nested(open, v, close string, n int) string {
	return strings.Repeat(open, n) + v + strings.Repeat(close, n)
}

// validJSONCases say whether each input is one JSON value, as RFC 8259 and
// encoding/json define it.
var validJSONCases = []struct {
	in    string
	valid bool
}{
	// No value, and whitespace around it.
	{"", false},
	{" \t\r\n", false},
	{" \t\r\n{}\r\n\t ", true},
	{"\v{}", false},
	{"{}\f", false},
	{"\u00a0{}", false},
	{"\xef\xbb\xbf{}", false},
	{"{}\x00", false},
	// Exactly one value.
	{"{}{}", false},
	{"1 2", false},
	{"null null", false},
	{`"a""b"`, false},
	{"[] ,", false},
	{"{} x", false},
	{"truefalse", false},
	// Literals.
	{"true", true},
	{"false", true},
	{"null", true},
	{"[true,false,null]", true},
	{"tru", false},
	{"nul", false},
	{"fals", false},
	{"True", false},
	{"nulll", false},
	{"[truex]", false},
	// Numbers.
	{"0", true},
	{"-0", true},
	{"7", true},
	{"1234567890", true},
	{"-12.5", true},
	{"0.001", true},
	{"1e5", true},
	{"1E+5", true},
	{"-1.5e-10", true},
	{"0e0", true},
	{"[0,-1,2.5e3]", true},
	{"01", false},
	{"-01", false},
	{"00", false},
	{"-", false},
	{"--1", false},
	{"+1", false},
	{"-a", false},
	{".5", false},
	{"1.", false},
	{"1.e5", false},
	{"1e", false},
	{"1e+", false},
	{"1e+-1", false},
	{"1.5.5", false},
	{"0x10", false},
	{"Infinity", false},
	{"NaN", false},
	{"[1-]", false},
	{"[1 2]", false},
	// Strings.
	{`""`, true},
	{`"plain text"`, true},
	{`"\"\\\/\b\f\n\r\t"`, true},
	{`"\u00e9\uABCD\uabcd"`, true},
	{"\"\x7f\"", true},
	{"\"\xff\xfe\"", true},
	{"\"\xc3\"", true},
	{"\"caf\xc3\xa9\"", true},
	{`"abc`, false},
	{`"`, false},
	{`"\`, false},
	{`"\"`, false},
	{"\"\x00\"", false},
	{"\"\x1f\"", false},
	{"\"\t\"", false},
	{"\"\n\"", false},
	{`"\x"`, false},
	{`"\'"`, false},
	{`"\U00e9"`, false},
	{`"\u"`, false},
	{`"\u12"`, false},
	{`"\u123"`, false},
	{`"\u12G4"`, false},
	{`"\u123`, false},
	// Objects and arrays.
	{"{}", true},
	{"[]", true},
	{"{ }", true},
	{"[ ]", true},
	{`{"a":1}`, true},
	{` { "a" : [ 1 , { "b" : null } ] , "a" : "" } `, true},
	{`[[],{},"",0]`, true},
	{"{", false},
	{"}", false},
	{"[", false},
	{"]", false},
	{"[1,", false},
	{"[1,]", false},
	{"[,1]", false},
	{"[,]", false},
	{"[1}", false},
	{`{"a":1]`, false},
	{`{"a":1,}`, false},
	{`{,"a":1}`, false},
	{`{"a"}`, false},
	{`{"a" 1}`, false},
	{`{"a":}`, false},
	{`{"a"::1}`, false},
	{`{1:2}`, false},
	{`{a:1}`, false},
	{`{"a":1 "b":2}`, false},
	// encoding/json lets arrays and objects nest 10000 deep.
	{nested("[", "", "]", 10000), true},
	{nested("[", "", "]", 10001), false},
	{nested(`{"a":`, "1", "}", 10000), true},
	{nested(`{"a":`, "1", "}", 10001), false},
	{nested(`[{"a":`, "1", "}]", 5000), true},
	{"[" + nested(`{"a":[`, "", "]}", 5000) + "]", false},
	{strings.Repeat("[", 10001), false},
}

// describe returns a short form of b for a test message.
func describe(b []byte) string {
	if len(b) > 64 {
		return fmt.Sprintf("%q... (%d bytes)", b[:64], len(b))
	}
	return fmt.Sprintf("%q", b)
}

func TestValidJSON_Cases(t *testing.T) {
	for _, c := range validJSONCases {
		b := []byte(c.in)
		if got := validJSON(b); got != c.valid {
			t.Errorf("validJSON(%s) = %v, want %v", describe(b), got, c.valid)
		}
		if got := json.Valid(b); got != c.valid {
			t.Errorf("json.Valid(%s) = %v, want %v", describe(b), got, c.valid)
		}
	}
}

// TestValidJSON_StringBytes puts every byte at each place in the first
// words of a string, followed by more string or by the end of the input.
func TestValidJSON_StringBytes(t *testing.T) {
	for c := 0; c < 256; c++ {
		for k := 0; k <= 17; k++ {
			prefix := `["` + strings.Repeat("a", k) + string([]byte{byte(c)})
			for _, suffix := range []string{"", `"`, `"]`, `n"]`, `u0041"]`, `aaaaaaaaaaaa"]`} {
				b := []byte(prefix + suffix)
				if got, want := validJSON(b), json.Valid(b); got != want {
					t.Errorf("validJSON(%s) = %v, json.Valid %v", describe(b), got, want)
				}
			}
		}
	}
}

func FuzzValidJSON(f *testing.F) {
	for _, c := range validJSONCases {
		f.Add([]byte(c.in))
	}
	for _, m := range loadInboundCorpus(f) {
		f.Add(m.line)
		for _, n := range []int{len(m.line) - 1, len(m.line) / 2, len(m.line) / 3} {
			if n >= 0 {
				f.Add(m.line[:n])
			}
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if got, want := validJSON(b), json.Valid(b); got != want {
			t.Fatalf("validJSON(%s) = %v, json.Valid %v", describe(b), got, want)
		}
	})
}

// BenchmarkValidJSON validates every message of the inbound corpus once per
// op, with validJSON and with json.Valid.
func BenchmarkValidJSON(b *testing.B) {
	corpus := loadInboundCorpus(b)
	var size int64
	wantValid := 0
	for _, m := range corpus {
		size += int64(len(m.line))
		if json.Valid(m.line) {
			wantValid++
		}
	}
	for _, v := range []struct {
		name  string
		valid func([]byte) bool
	}{
		{"validJSON", validJSON},
		{"json.Valid", json.Valid},
	} {
		b.Run(v.name, func(b *testing.B) {
			b.SetBytes(size)
			for i := 0; i < b.N; i++ {
				n := 0
				for _, m := range corpus {
					if v.valid(m.line) {
						n++
					}
				}
				if n != wantValid {
					b.Fatalf("%d valid messages, want %d", n, wantValid)
				}
			}
		})
	}
}
