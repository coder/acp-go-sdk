package acp

import "encoding/json"

// decodeJSON decodes data into v as json.Unmarshal(data, v) does: same value,
// same error. When *T implements json.Unmarshaler, json.Unmarshal validates
// data, scans the value again to find its end, and calls UnmarshalJSON with
// the value without the whitespace around it, adding no context to its error.
// decodeJSON validates and trims the whitespace, then calls UnmarshalJSON.
// Data that is not one JSON value goes to json.Unmarshal, which returns its
// syntax error and leaves v as it is.
func decodeJSON[T any](data []byte, v *T) error {
	u, ok := any(v).(json.Unmarshaler)
	// validJSON must stay equal to json.Valid, or decodeJSON would decode
	// data that json.Unmarshal rejects, or the reverse.
	if !ok || !validJSON(data) {
		return json.Unmarshal(data, v)
	}
	// Valid data holds one value, which neither starts nor ends with
	// whitespace.
	i, j := 0, len(data)
	for isSpace(data[i]) {
		i++
	}
	for isSpace(data[j-1]) {
		j--
	}
	return u.UnmarshalJSON(data[i:j])
}
