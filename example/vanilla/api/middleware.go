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

// AuthMiddleware checks that the connection is authenticated (via Login)
// and sets up the user context. Login calls conn.SetUserID(), so this
// middleware simply looks up the user by the connection's user ID.
// Apply this middleware only to handlers that require authentication.
func AuthMiddleware(tokenStore *TokenStore) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			conn := aprot.Connection(ctx)
			if conn == nil || conn.UserID() == "" {
				return nil, aprot.ErrUnauthorized("authentication required")
			}

			user := tokenStore.UserByID(conn.UserID())
			if user == nil {
				return nil, aprot.ErrUnauthorized("invalid session")
			}

			ctx = context.WithValue(ctx, authUserKey, user)
			return next(ctx, req)
		}
	}
}
