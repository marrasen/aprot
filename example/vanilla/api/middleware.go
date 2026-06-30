package api

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/marrasen/aprot"
)

// Context key for authenticated user
type contextKey string

const authUserKey contextKey = "auth_user"

// AuthUserFromContext returns the authenticated user from the context.
// Returns nil if no user is authenticated.
func AuthUserFromContext(ctx context.Context) *AuthUser {
	if user, ok := ctx.Value(authUserKey).(*AuthUser); ok {
		return user
	}
	return nil
}

// TokenStore manages authentication tokens and user lookup.
type TokenStore struct {
	tokens map[string]*AuthUser // token -> user
	users  map[string]*AuthUser // userID -> user
}

// NewTokenStore creates a new token store.
func NewTokenStore() *TokenStore {
	return &TokenStore{
		tokens: make(map[string]*AuthUser),
		users:  make(map[string]*AuthUser),
	}
}

// Store stores a token for a user.
func (ts *TokenStore) Store(token string, user *AuthUser) {
	ts.tokens[token] = user
	ts.users[user.ID] = user
}

// Validate returns the user for a token, or nil if invalid.
func (ts *TokenStore) Validate(token string) *AuthUser {
	return ts.tokens[token]
}

// UserByID returns the user for a given ID, or nil if not found.
func (ts *TokenStore) UserByID(id string) *AuthUser {
	return ts.users[id]
}

// LoggingOptions configures [LoggingMiddleware].
//
// The zero value disables param logging entirely, matching the original
// LoggingMiddleware behavior.
type LoggingOptions struct {
	// LogParams enables logging the JSON params alongside method/duration.
	LogParams bool

	// RedactKeys lists JSON object keys whose values are replaced with
	// "[REDACTED]" before logging. Comparison is case-insensitive and the
	// walk recurses into nested objects and arrays. Required reading: aprot
	// params are positional, so for handlers like Login(username, password)
	// the wire payload is a top-level JSON array — the field names that
	// would trigger redaction don't appear. Use SkipMethods for those.
	RedactKeys []string

	// SkipMethods lists fully-qualified method names ("Struct.Method") to
	// omit params for entirely. The log line shows params=[REDACTED] for
	// methods in this list. Use this for handlers whose entire input is
	// sensitive (e.g. "PublicHandlers.Login") or for very chatty methods
	// you don't want polluting logs.
	SkipMethods []string

	// MaxParamLen truncates JSON params longer than this many bytes,
	// appending "...(truncated)". A value <= 0 disables truncation.
	MaxParamLen int
}

// DefaultLoggingOptions returns sensible defaults for production logging:
// params enabled, common sensitive keys redacted, Login skipped, params
// truncated at 1 KiB.
func DefaultLoggingOptions() LoggingOptions {
	return LoggingOptions{
		LogParams:   true,
		RedactKeys:  []string{"password", "passwd", "token", "secret", "api_key", "apiKey", "authorization"},
		SkipMethods: []string{"PublicHandlers.Login"},
		MaxParamLen: 1024,
	}
}

// LoggingMiddleware logs all requests with timing information. Pass a
// [LoggingOptions] to also log the JSON params (with redaction and
// truncation). The zero-arg form preserves the original method/duration
// log line.
//
// Example:
//
//	server.Use(api.LoggingMiddleware(api.DefaultLoggingOptions()))
func LoggingMiddleware(opts ...LoggingOptions) aprot.Middleware {
	var o LoggingOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	skipSet := make(map[string]struct{}, len(o.SkipMethods))
	for _, m := range o.SkipMethods {
		skipSet[m] = struct{}{}
	}
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			start := time.Now()
			conn := aprot.Connection(ctx)

			result, err := next(ctx, req)

			duration := time.Since(start)
			connID := uint64(0)
			remoteAddr := "unknown"
			if conn != nil {
				connID = conn.ID()
				remoteAddr = conn.RemoteAddr()
			}

			paramsField := ""
			if o.LogParams {
				paramsField = " params=" + formatParamsForLog(req, o, skipSet)
			}

			if err != nil {
				log.Printf("[conn:%d %s] %s %s%s - ERROR: %v (%s)", connID, remoteAddr, req.ID, req.Method, paramsField, err, duration)
			} else {
				log.Printf("[conn:%d %s] %s %s%s - OK (%s)", connID, remoteAddr, req.ID, req.Method, paramsField, duration)
			}

			return result, err
		}
	}
}

func formatParamsForLog(req *aprot.Request, o LoggingOptions, skipSet map[string]struct{}) string {
	if _, skip := skipSet[req.Method]; skip {
		return "[REDACTED]"
	}
	if len(req.Params) == 0 {
		return "[]"
	}
	v := redactJSON(req.Params, o.RedactKeys)
	return truncateForLog(string(v), o.MaxParamLen)
}

// redactJSON walks raw JSON and replaces values for any object key whose
// name matches a key in keys (case-insensitive) with "[REDACTED]". Recurses
// into nested objects and arrays. Returns the input unchanged when keys is
// empty or the input is not valid JSON. Output uses deterministic key
// ordering so log lines are stable and greppable.
func redactJSON(raw jsontext.Value, keys []string) jsontext.Value {
	if len(keys) == 0 || len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keySet[strings.ToLower(k)] = struct{}{}
	}
	walked := redactWalk(v, keySet)
	out, err := json.Marshal(walked, json.Deterministic(true))
	if err != nil {
		return raw
	}
	return out
}

func redactWalk(v any, keys map[string]struct{}) any {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			if _, ok := keys[strings.ToLower(k)]; ok {
				t[k] = "[REDACTED]"
			} else {
				t[k] = redactWalk(vv, keys)
			}
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = redactWalk(vv, keys)
		}
		return t
	default:
		return v
	}
}

func truncateForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// AuthMiddleware checks that the connection is authenticated and sets up the
// user context. It first checks conn.Get for a cached *AuthUser (set by
// ConnectHookAuth or Login), falling back to a token store lookup by the
// connection's user ID.
// Apply this middleware only to handlers that require authentication.
func AuthMiddleware(tokenStore *TokenStore) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			conn := aprot.Connection(ctx)
			if conn == nil {
				return nil, aprot.ErrUnauthorized("authentication required")
			}

			// Check connection-scoped cache first (set by ConnectHookAuth or Login)
			var user *AuthUser
			if v, ok := conn.Load(authUserKey); ok {
				user, _ = v.(*AuthUser)
			}
			if user == nil {
				// Fallback: look up by user ID in the token store
				if conn.UserID() == "" {
					return nil, aprot.ErrUnauthorized("authentication required")
				}
				user = tokenStore.UserByID(conn.UserID())
				if user == nil {
					return nil, aprot.ErrUnauthorized("invalid session")
				}
			}

			ctx = context.WithValue(ctx, authUserKey, user)
			return next(ctx, req)
		}
	}
}

// ConnectHookAuth returns a ConnectHook that validates a session cookie at
// connection time and caches the authenticated user on the connection via
// conn.Set. This avoids re-loading the user from a store on every request.
//
// Usage:
//
//	server.OnConnect(api.ConnectHookAuth(tokenStore))
func ConnectHookAuth(tokenStore *TokenStore) aprot.ConnectHook {
	return func(ctx context.Context, conn *aprot.Conn) error {
		// Look for a session token in cookies
		for _, cookie := range conn.Info().Cookies {
			if cookie.Name == "session" {
				user := tokenStore.Validate(cookie.Value)
				if user == nil {
					return aprot.ErrConnectionRejected("invalid session")
				}
				conn.SetUserID(user.ID)
				conn.Set(authUserKey, user)
				return nil
			}
		}
		// No session cookie — allow connection (public endpoints still work).
		// Protected handlers will reject via AuthMiddleware.
		return nil
	}
}
