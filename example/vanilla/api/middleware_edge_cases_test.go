package api

import (
	"testing"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/marrasen/aprot"
)

// Test bug: Truncation in middle of JSON string literal produces invalid JSON
func TestTruncationCorruptsJSON(t *testing.T) {
	// This JSON has a long value that will be cut in the middle of a string
	input := `{"field":"this-is-a-very-long-password-value-here"}`
	max := 20
	result := truncateForLog(input, max)
	t.Logf("Truncated: %q", result)
	// Result is: `{"field":"this-is-a...(truncated)`
	// This is invalid JSON (unclosed string and object)
	// Bug: The truncated output shouldn't be parsed as JSON anymore
	// but the logging code treats it as valid
}

// Test bug: Case sensitivity in SkipMethods
func TestSkipMethodsCaseSensitivity(t *testing.T) {
	opts := LoggingOptions{
		LogParams:   true,
		SkipMethods: []string{"publichandlers.login"}, // lowercase
	}
	skipSet := make(map[string]struct{}, len(opts.SkipMethods))
	for _, m := range opts.SkipMethods {
		skipSet[m] = struct{}{}
	}

	// But actual method comes with different casing
	actualMethod := "PublicHandlers.Login"

	if _, found := skipSet[actualMethod]; found {
		t.Logf("BUG NOT PRESENT: Found %q in skipSet", actualMethod)
	} else {
		t.Logf("BUG FOUND: Method %q not found in skipSet despite being in SkipMethods", actualMethod)
		t.Logf("skipSet keys: %v", opts.SkipMethods)
	}
}

// Test bug: Invalid UTF-8 in params crashes logging
func TestInvalidUTF8InParams(t *testing.T) {
	// This is not valid UTF-8
	invalidJSON := jsontext.Value("\xff\xfe")

	// This should not panic or corrupt the log
	result := redactJSON(invalidJSON, []string{"password"})

	// Should return the raw input unchanged since Unmarshal fails
	if string(result) != string(invalidJSON) {
		t.Errorf("Expected raw bytes to be returned unchanged")
	}
	t.Logf("Result: %v (byte values)", []byte(result))
}

// Test bug: formatParamsForLog with nil req.Params
func TestEmptyParamsEdgeCase(t *testing.T) {
	opts := LoggingOptions{
		LogParams:  true,
		RedactKeys: []string{},
	}
	skipSet := make(map[string]struct{})

	// Simulate request with nil params
	mockReq := &aprot.Request{
		Method: "Test",
		Params: jsontext.Value(nil),
	}

	result := formatParamsForLog(mockReq, opts, skipSet)
	t.Logf("formatParamsForLog with nil Params: %q", result)
	if result != "[]" {
		t.Errorf("Expected '[]' for nil Params, got %q", result)
	}
}
