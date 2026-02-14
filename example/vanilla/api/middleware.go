package api

import (
	"context"
	"log"
	"time"

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

// LoggingMiddleware logs all requests with timing information.
func LoggingMiddleware() aprot.Middleware {
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

			if err != nil {
				log.Printf("[conn:%d %s] %s %s - ERROR: %v (%s)", connID, remoteAddr, req.ID, req.Method, err, duration)
			} else {
				log.Printf("[conn:%d %s] %s %s - OK (%s)", connID, remoteAddr, req.ID, req.Method, duration)
			}

			return result, err
		}
	}
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
