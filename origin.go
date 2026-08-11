package aprot

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// SameOriginCheck returns an origin-check function for [Server.SetCheckOrigin]
// that accepts the server's own origin — the Origin header's host (including
// any port) equals the request's Host, compared case-insensitively — plus any
// extraOrigins given verbatim:
//
//	server.SetCheckOrigin(aprot.SameOriginCheck())                          // production
//	server.SetCheckOrigin(aprot.SameOriginCheck("http://localhost:5173"))   // with a dev proxy
//
// Extra origins are full origins (scheme://host[:port]) matched against the
// whole Origin header case-insensitively, with a trailing slash tolerated.
// They exist for dev proxies such as Vite's changeOrigin, which rewrite Host
// to the backend while forwarding the browser's own Origin.
//
// A missing, unparseable, or "null" Origin is rejected, and "null" cannot be
// allowlisted. Browsers always send Origin on a WebSocket handshake, so this
// is correct for browser-facing deployments — but it rejects non-browser
// clients that omit the header, which is why the helper is opt-in while the
// server's default remains allow-all. Deployments mixing browsers and
// non-browser clients should keep a custom check function instead.
func SameOriginCheck(extraOrigins ...string) func(*http.Request) bool {
	extras := make([]string, len(extraOrigins))
	for i, o := range extraOrigins {
		extras[i] = strings.ToLower(strings.TrimSuffix(o, "/"))
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" || strings.EqualFold(origin, "null") {
			return false
		}
		if slices.Contains(extras, strings.ToLower(strings.TrimSuffix(origin, "/"))) {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
}
