package api

import (
	"testing"

	"github.com/go-json-experiment/json/jsontext"
)

func TestRedactJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
		keys  []string
		want  string
	}{
		{
			name:  "flat object redacts matching key",
			input: `{"username":"alice","password":"hunter2"}`,
			keys:  []string{"password"},
			want:  `{"password":"[REDACTED]","username":"alice"}`,
		},
		{
			name:  "case insensitive match preserves original casing",
			input: `{"Password":"hunter2"}`,
			keys:  []string{"password"},
			want:  `{"Password":"[REDACTED]"}`,
		},
		{
			name:  "nested object",
			input: `{"user":{"name":"alice","token":"x"}}`,
			keys:  []string{"token"},
			want:  `{"user":{"name":"alice","token":"[REDACTED]"}}`,
		},
		{
			name:  "array of objects",
			input: `[{"password":"x"},{"password":"y"}]`,
			keys:  []string{"password"},
			want:  `[{"password":"[REDACTED]"},{"password":"[REDACTED]"}]`,
		},
		{
			name:  "positional struct param inside wire array",
			input: `[{"email":"a@b","password":"x"}]`,
			keys:  []string{"password"},
			want:  `[{"email":"a@b","password":"[REDACTED]"}]`,
		},
		{
			name:  "no matching key passes through (sorted)",
			input: `{"username":"alice"}`,
			keys:  []string{"password"},
			want:  `{"username":"alice"}`,
		},
		{
			name:  "invalid JSON passes through unchanged",
			input: `not json`,
			keys:  []string{"password"},
			want:  `not json`,
		},
		{
			name:  "empty keys list passes through unchanged",
			input: `{"password":"x"}`,
			keys:  nil,
			want:  `{"password":"x"}`,
		},
		{
			name:  "multiple keys",
			input: `{"password":"x","token":"y","name":"alice"}`,
			keys:  []string{"password", "token"},
			want:  `{"name":"alice","password":"[REDACTED]","token":"[REDACTED]"}`,
		},
		{
			name:  "non-string value still redacted",
			input: `{"secret":42}`,
			keys:  []string{"secret"},
			want:  `{"secret":"[REDACTED]"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactJSON(jsontext.Value(tc.input), tc.keys)
			if string(got) != tc.want {
				t.Errorf("redactJSON() = %q, want %q", string(got), tc.want)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "under limit untouched", in: "abcdef", max: 100, want: "abcdef"},
		{name: "zero max disables", in: "abcdef", max: 0, want: "abcdef"},
		{name: "negative max disables", in: "abcdef", max: -1, want: "abcdef"},
		{name: "over limit truncates with marker", in: "abcdefghij", max: 4, want: "abcd...(truncated)"},
		{name: "exactly at limit untouched", in: "abcd", max: 4, want: "abcd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateForLog(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}
