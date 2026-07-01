package aprot

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// CORSOptions configures the [CORS] middleware.
//
// The zero value allows no cross-origin requests. CORS is opt-in: nothing is
// loosened unless you construct the middleware and mount it, mirroring the
// closed-by-default posture of [Server.SetCheckOrigin] for WebSocket.
type CORSOptions struct {
	// AllowedOrigins lists the origins permitted to make cross-origin requests,
	// each matched exactly (scheme + host + optional port) against the request's
	// Origin header. The special value "*" allows any origin. An empty list
	// allows none. When AllowCredentials is true, "*" cannot be sent verbatim
	// per the Fetch standard, so the matched origin is echoed back instead.
	AllowedOrigins []string
	// AllowedMethods lists the HTTP methods permitted for cross-origin requests,
	// advertised in preflight responses. Defaults to GET, POST, PUT, PATCH,
	// DELETE, OPTIONS when empty.
	AllowedMethods []string
	// AllowedHeaders lists the request headers a client may send. Defaults to
	// {"Content-Type"} when empty. The special value "*" allows any header (for
	// credentialed requests the browser's requested headers are echoed, since
	// "*" is not honored alongside credentials).
	AllowedHeaders []string
	// ExposedHeaders lists response headers the browser may expose to client
	// JavaScript beyond the CORS-safelisted set.
	ExposedHeaders []string
	// AllowCredentials sets Access-Control-Allow-Credentials: true, required for
	// cross-origin requests that carry cookies or Authorization. It must be
	// paired with explicit (non-"*") origins; see AllowedOrigins.
	AllowCredentials bool
	// MaxAge is how long, in seconds, a browser may cache a preflight response.
	// Zero omits the header (the browser uses its default).
	MaxAge int
}

// defaultCORSMethods is advertised in preflight when AllowedMethods is empty.
var defaultCORSMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// CORS returns middleware that adds CORS response headers and answers OPTIONS
// preflight requests. It is a standard func(http.Handler) http.Handler wrapper,
// so it composes with any of the three transports — the REST adapter
// ([RESTAdapter]), the SSE handler ([Server.HTTPTransport]), or the WebSocket
// handler ([Server.WebSocket]):
//
//	cors := aprot.CORS(aprot.CORSOptions{
//	    AllowedOrigins:   []string{"https://app.example.com"},
//	    AllowCredentials: true, // needed for cookie-authenticated browsers
//	})
//	http.Handle("/api/", http.StripPrefix("/api", cors(rest)))
//	http.Handle("/sse", cors(server.HTTPTransport()))
//
// It is closed by default: a request whose Origin is not allowed receives no
// CORS headers, so the browser blocks the cross-origin response. Same-origin
// and non-browser clients are unaffected because the wrapped handler still runs
// for non-preflight requests.
//
// For cookie-authenticated deployments, set AllowCredentials and list the exact
// origins — never rely on "*", which browsers reject alongside credentials.
// This is the HTTP-transport analogue of restricting WebSocket origins with
// [Server.SetCheckOrigin].
func CORS(opts CORSOptions) func(http.Handler) http.Handler {
	methods := opts.AllowedMethods
	if len(methods) == 0 {
		methods = defaultCORSMethods
	}
	allowMethods := strings.Join(methods, ", ")

	headers := opts.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Content-Type"}
	}
	allowHeaders := strings.Join(headers, ", ")
	wildcardHeaders := slices.Contains(headers, "*")

	exposeHeaders := strings.Join(opts.ExposedHeaders, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Not a cross-origin request — leave it untouched.
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowOrigin, ok := opts.resolveOrigin(origin)

			// Vary on Origin whenever the response depends on it, so shared
			// caches don't serve one origin's response to another.
			w.Header().Add("Vary", "Origin")

			isPreflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""

			if ok {
				w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
				if opts.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if exposeHeaders != "" {
					w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
				}
			}

			if !isPreflight {
				next.ServeHTTP(w, r)
				return
			}

			// Preflight: answer directly and never reach the wrapped handler.
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			if ok {
				w.Header().Set("Access-Control-Allow-Methods", allowMethods)
				// With credentials the literal "*" isn't honored for headers, so
				// reflect exactly what the browser asked to send.
				if wildcardHeaders && opts.AllowCredentials {
					if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
						w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
					}
				} else {
					w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				}
				if opts.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", strconv.Itoa(opts.MaxAge))
				}
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

// resolveOrigin decides the Access-Control-Allow-Origin value for a request's
// Origin, and whether the origin is allowed at all. A wildcard match echoes the
// concrete origin when credentials are enabled (the Fetch standard forbids "*"
// with credentials) and otherwise returns "*".
func (opts CORSOptions) resolveOrigin(origin string) (allow string, ok bool) {
	for _, o := range opts.AllowedOrigins {
		if o == origin {
			return origin, true
		}
		if o == "*" {
			if opts.AllowCredentials {
				return origin, true
			}
			return "*", true
		}
	}
	return "", false
}
