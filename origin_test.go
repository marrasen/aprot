package aprot

import (
	"net/http"
	"testing"
)

func TestSameOriginCheck(t *testing.T) {
	const noOrigin = "\x00absent" // sentinel: don't set the Origin header at all

	cases := []struct {
		name   string
		origin string
		host   string
		extras []string
		want   bool
	}{
		{"same host http", "http://app.example.com", "app.example.com", nil, true},
		{"same host https", "https://app.example.com", "app.example.com", nil, true},
		{"same host and port", "https://app.example.com:8443", "app.example.com:8443", nil, true},
		{"case-insensitive host", "https://APP.Example.COM", "app.example.com", nil, true},
		{"origin port not in host", "https://app.example.com:9999", "app.example.com", nil, false},
		{"host port not in origin", "https://app.example.com", "app.example.com:8443", nil, false},
		{"suffix look-alike", "https://app.example.com.evil.com", "app.example.com", nil, false},
		{"prefix look-alike", "https://evilapp.example.com", "app.example.com", nil, false},
		{"host in path only", "https://evil.com/app.example.com", "app.example.com", nil, false},
		{"missing origin", noOrigin, "app.example.com", nil, false},
		{"empty origin", "", "app.example.com", nil, false},
		{"null origin", "null", "app.example.com", nil, false},
		{"null origin uppercase", "NULL", "app.example.com", nil, false},
		{"unparseable origin", "http://exa mple.com", "app.example.com", nil, false},
		{"schemeless origin", "app.example.com", "app.example.com", nil, false},
		{"extra origin", "http://localhost:5173", "app.example.com", []string{"http://localhost:5173"}, true},
		{"extra with trailing slash configured", "http://localhost:5173", "app.example.com", []string{"http://localhost:5173/"}, true},
		{"extra case-insensitive", "HTTP://LOCALHOST:5173", "app.example.com", []string{"http://localhost:5173"}, true},
		{"extra does not widen", "http://localhost:9999", "app.example.com", []string{"http://localhost:5173"}, false},
		{"null cannot be allowlisted", "null", "app.example.com", []string{"null"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := SameOriginCheck(tc.extras...)
			r := &http.Request{Host: tc.host, Header: http.Header{}}
			if tc.origin != noOrigin {
				r.Header.Set("Origin", tc.origin)
			}
			if got := check(r); got != tc.want {
				t.Errorf("SameOriginCheck(%v) with Origin %q, Host %q = %v, want %v",
					tc.extras, tc.origin, tc.host, got, tc.want)
			}
		})
	}
}
