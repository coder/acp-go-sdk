package acp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// plainParams has no UnmarshalJSON, so decodeJSON hands it to json.Unmarshal.
type plainParams struct {
	SessionId string `json:"sessionId"`
	N         int    `json:"n"`
}

// decodeCases are inputs that json.Unmarshal treats apart: no value,
// whitespace around the value, null, values that are not objects, data after
// the value, and input that is not JSON.
var decodeCases = []string{
	"", " ", "\n\t\r ",
	"null", " null ", "\tnull\n", "Null",
	"{}", " {} ", "\r\n{}\r\n", "[]", " [1, 2] ", "1", "-0.5e3", `"text"`, " true ", "false",
	`{"sessionId":"s","prompt":[{"type":"text","text":"hi"}]}`,
	"\t" + `{"sessionId":"s","prompt":[{"type":"text","text":"hi"}],"_meta":{"k":1}}` + " \n",
	`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}`,
	`{"sessionId":"s","configId":"c","type":"boolean","value":true}`,
	`{"sessionId":"s","configId":"c","value":"v"}`,
	`{"stopReason":"end_turn"}`, `{"sessionId":"s","n":2}`,
	`{"sessionId":1}`, `{"sessionId":"s","prompt":{}}`, `{"sessionId":"s","n":"2"}`, `{"sessionId":"\xff\u00e9"}`,
	"{} x", "{}{}", "null null", "1 2", `"a""b"`, "[] ,", `{"sessionId":"s","prompt":[]} {}`,
	"{", "}", "[", "[1,", `{"sessionId":}`, `{"sessionId" "s"}`, `{"a":1,}`, "nul", "tru", "x", "\x00",
	`"\x"`, `"abc`, "\"a\x01\"", "01", "1.", "-", "+1",
	"\v{}", "{}\f", "\xef\xbb\xbf{}", "\u00a0{}", "{}\x00",
	strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
}

// checkDecode decodes data with json.Unmarshal and with decodeJSON, each into
// a value from start, and fails unless both return the same error and leave
// the same value.
func checkDecode[T any](t *testing.T, data []byte, start func() T) {
	t.Helper()
	want, got := start(), start()
	wantErr := json.Unmarshal(data, &want)
	gotErr := decodeJSON(data, &got)
	if !reflect.DeepEqual(gotErr, wantErr) {
		t.Fatalf("%T %q: error %#v, json.Unmarshal %#v", got, data, gotErr, wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%T %q: value %+v, json.Unmarshal %+v", got, data, got, want)
	}
}

func zero[T any]() T {
	var v T
	return v
}

// holding returns a start func whose values already hold what s decodes to.
func holding[T any](s string) func() T {
	return func() T {
		var v T
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			panic(err)
		}
		return v
	}
}

// checkDecodes decodes data into a struct type with UnmarshalJSON, a union,
// a plain struct and json.RawMessage, fresh and holding data.
func checkDecodes(t *testing.T, data []byte) {
	t.Helper()
	checkDecode(t, data, zero[PromptRequest])
	checkDecode(t, data, holding[PromptRequest](`{"sessionId":"old","prompt":[{"type":"text","text":"old"}],"_meta":{"k":1}}`))
	checkDecode(t, data, zero[SessionNotification])
	checkDecode(t, data, zero[PromptResponse])
	checkDecode(t, data, zero[SetSessionConfigOptionRequest])
	checkDecode(t, data, holding[SetSessionConfigOptionRequest](`{"sessionId":"old","configId":"c","type":"boolean","value":true}`))
	checkDecode(t, data, zero[plainParams])
	checkDecode(t, data, holding[plainParams](`{"sessionId":"old","n":1}`))
	checkDecode(t, data, zero[json.RawMessage])
	checkDecode(t, data, holding[json.RawMessage](`{"old":true}`))
}

func TestDecodeJSON_MatchesUnmarshal(t *testing.T) {
	checkDecodes(t, nil)
	for _, s := range decodeCases {
		checkDecodes(t, []byte(s))
	}
}

// corpusValues returns the params and results of the messages in the inbound
// corpus.
func corpusValues(tb testing.TB) [][]byte {
	var values [][]byte
	for _, m := range loadInboundCorpus(tb) {
		var msg anyMessage
		if json.Unmarshal(m.line, &msg) != nil {
			continue
		}
		for _, raw := range []json.RawMessage{msg.Params, msg.Result} {
			if raw != nil {
				values = append(values, raw)
			}
		}
	}
	return values
}

func TestDecodeJSON_Corpus(t *testing.T) {
	for _, b := range corpusValues(t) {
		checkDecodes(t, b)
		checkDecodes(t, []byte(" "+string(b)+"\n"))
	}
}

func FuzzDecodeJSON(f *testing.F) {
	for _, s := range decodeCases {
		f.Add([]byte(s))
	}
	for _, b := range corpusValues(f) {
		f.Add(b)
	}
	f.Fuzz(checkDecodes)
}
