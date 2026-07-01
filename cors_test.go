package aprot

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler records whether it was called and always writes 200.
type okHandler struct{ called bool }

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	w.WriteHeader(http.StatusOK)
}

func doReq(t *testing.T, handler http.Handler, method, origin string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/things", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

// An allowed origin gets Access-Control-Allow-Origin and the request still
// reaches the wrapped handler.
func TestCORS_AllowedOrigin(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"https://app.example.com"}})(next)

	rec := doReq(t, h, http.MethodGet, "https://app.example.com", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the request origin", got)
	}
	if !next.called {
		t.Error("wrapped handler should be called for a simple (non-preflight) request")
	}
	if got := rec.Header().Get("Vary"); got == "" {
		t.Error("expected Vary: Origin so caches don't serve one origin's response to another")
	}
}

// A disallowed origin gets no CORS headers; the request still passes through
// (the browser is what blocks the response), so behavior for same-origin and
// non-browser clients is unchanged.
func TestCORS_DisallowedOrigin(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"https://app.example.com"}})(next)

	rec := doReq(t, h, http.MethodGet, "https://evil.example.com", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a disallowed origin", got)
	}
	if !next.called {
		t.Error("wrapped handler should still be called; the browser enforces the block")
	}
}

// A preflight OPTIONS request is answered directly (204) with the allow-* headers
// and never reaches the wrapped handler.
func TestCORS_Preflight(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type", "X-Custom"},
		MaxAge:         600,
	})(next)

	rec := doReq(t, h, http.MethodOptions, "https://app.example.com", map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "Content-Type",
	})

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if next.called {
		t.Error("preflight must not reach the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Errorf("Access-Control-Allow-Methods = %q, want \"GET, POST\"", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Custom" {
		t.Errorf("Access-Control-Allow-Headers = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("Access-Control-Max-Age = %q, want 600", got)
	}
}

// With credentials enabled and an explicit origin, the exact origin is echoed
// (never "*") and Allow-Credentials is set.
func TestCORS_Credentials(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowCredentials: true,
	})(next)

	rec := doReq(t, h, http.MethodGet, "https://app.example.com", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the exact origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

// A wildcard origin without credentials responds with "*".
func TestCORS_WildcardNoCredentials(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"*"}})(next)

	rec := doReq(t, h, http.MethodGet, "https://anything.example.com", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Error("Allow-Credentials must not be set without AllowCredentials")
	}
}

// A wildcard origin WITH credentials must echo the concrete origin, never "*"
// (the Fetch spec forbids "*" with credentials).
func TestCORS_WildcardWithCredentials(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"*"}, AllowCredentials: true})(next)

	rec := doReq(t, h, http.MethodGet, "https://anything.example.com", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the concrete origin (not *) with credentials", got)
	}
}

// A request without an Origin header is not a CORS request; no CORS headers are
// added and the request passes through untouched.
func TestCORS_NoOrigin(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"*"}})(next)

	rec := doReq(t, h, http.MethodGet, "", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a non-CORS request", got)
	}
	if !next.called {
		t.Error("non-CORS request should reach the wrapped handler")
	}
}

// An OPTIONS request that is not a preflight (no Access-Control-Request-Method)
// is passed through rather than short-circuited.
func TestCORS_NonPreflightOptions(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"https://app.example.com"}})(next)

	rec := doReq(t, h, http.MethodOptions, "https://app.example.com", nil)

	if !next.called {
		t.Error("a non-preflight OPTIONS should reach the wrapped handler")
	}
	_ = rec
}

// The zero value allows no origin: every cross-origin request is fully closed
// (no CORS headers) yet still passes through to the wrapped handler.
func TestCORS_ZeroValueClosed(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{})(next)

	rec := doReq(t, h, http.MethodGet, "https://app.example.com", nil)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for the zero-value (closed) config", got)
	}
	if !next.called {
		t.Error("wrapped handler should still run under the closed config")
	}
}

// A preflight from a disallowed origin is short-circuited (204) but must carry
// no Allow-* headers, so the browser blocks the actual request. Guards against
// leaking allow-methods/headers to origins that were never permitted.
func TestCORS_DisallowedOriginPreflight(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{AllowedOrigins: []string{"https://app.example.com"}})(next)

	rec := doReq(t, h, http.MethodOptions, "https://evil.example.com", map[string]string{
		"Access-Control-Request-Method": "POST",
	})

	if next.called {
		t.Error("preflight must not reach the wrapped handler even for a disallowed origin")
	}
	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Credentials",
	} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s = %q, want empty for a disallowed-origin preflight", header, got)
		}
	}
}

// With wildcard AllowedHeaders and credentials, the preflight must echo the
// browser's requested headers rather than the literal "*" (which the Fetch
// standard does not honor alongside credentials).
func TestCORS_WildcardHeadersWithCredentials(t *testing.T) {
	next := &okHandler{}
	h := CORS(CORSOptions{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	})(next)

	rec := doReq(t, h, http.MethodOptions, "https://app.example.com", map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "X-Custom, Content-Type",
	})

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "X-Custom, Content-Type" {
		t.Errorf("Access-Control-Allow-Headers = %q, want the echoed request headers (not \"*\") with credentials", got)
	}
}
