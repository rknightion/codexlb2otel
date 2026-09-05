package turn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

const encryptedArgumentMarker = "[omitted: encrypted]"

// redactFunctionArguments returns valid JSON for a function-call argument value.
//
// The archive profiler records arguments as a string leaf, so it has no inner value
// class to reuse here. The measured encrypted shape is therefore handled narrowly:
// a string value below a key containing "encrypted" (case-insensitive), or a
// Fernet-shaped string (URL-safe base64 with the version byte and minimum token
// structure), is replaced with encryptedArgumentMarker. Other values, including
// non-string encrypted keys, stay unchanged. The returned omission count is the
// number of replacements.
//
// Reducer Options has no input-specific budget. MaxToolOutputChars is the existing
// reducer-level bound for tool content and is the narrowest applicable setting; the
// summary package's MaxCharsPerToolInput is a later rendering budget, not capture
// control. Redaction happens before this capture bound. When the bound is exceeded,
// a compact valid JSON marker replaces the captured value while InputChars retains
// the original argument length.
func redactFunctionArguments(arguments string, max int) (input string, omitted int, truncated bool) {
	dec := json.NewDecoder(bytes.NewBufferString(arguments))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return encodedArgumentString(arguments, max)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return encodedArgumentString(arguments, max)
	}
	if dec.More() {
		return encodedArgumentString(arguments, max)
	}
	return redactAndBoundFunctionArguments(value, max)
}

func encodedArgumentString(arguments string, max int) (string, int, bool) {
	// The reducer must not emit malformed JSON. Preserve an unexpected wire value
	// as a JSON string rather than abandoning the complete tool-call record.
	encoded, _ := json.Marshal(arguments)
	input, truncated := boundJSON(string(encoded), max)
	return input, 0, truncated
}

func redactAndBoundFunctionArguments(value any, max int) (input string, omitted int, truncated bool) {
	value, omitted = redactEncryptedValue(value, false)
	encoded, err := json.Marshal(value)
	if err != nil {
		// All decoded JSON values marshal, but retain the valid-JSON guarantee if that
		// changes with a future decoder extension.
		input, truncated := boundJSON("null", max)
		return input, omitted, truncated
	}
	input, truncated = boundJSON(string(encoded), max)
	return input, omitted, truncated
}

func redactEncryptedValue(value any, encryptedContext bool) (any, int) {
	switch v := value.(type) {
	case map[string]any:
		omitted := 0
		for key, child := range v {
			var count int
			v[key], count = redactEncryptedValue(child, encryptedContext || strings.Contains(strings.ToLower(key), "encrypted"))
			omitted += count
		}
		return v, omitted
	case []any:
		omitted := 0
		for i, child := range v {
			var count int
			v[i], count = redactEncryptedValue(child, encryptedContext)
			omitted += count
		}
		return v, omitted
	case string:
		if encryptedContext || opaqueEncryptedString(v) {
			return encryptedArgumentMarker, 1
		}
	}
	return value, 0
}

func opaqueEncryptedString(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return err == nil && len(data) >= 73 && data[0] == 0x80
}

func boundJSON(input string, max int) (string, bool) {
	if max <= 0 {
		return "", input != ""
	}
	if len(input) <= max {
		return input, false
	}
	// Every fallback is valid JSON and fits the requested byte budget. The useful
	// object marker is preferred; very small test-only budgets still stay valid.
	for _, marker := range []string{`{"truncated":true}`, "null", `""`, "0"} {
		if len(marker) <= max {
			return marker, true
		}
	}
	return "", true
}
