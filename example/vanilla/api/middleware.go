package api

import (
	"context"
	"log"
	"time"

	"github.com/go-json-experiment/json"
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

// TokenStore manages authentication tokens.
type TokenStore struct {
	tokens map[string]*AuthUser // token -> user
}

// NewTokenStore creates a new token store.
func NewTokenStore() *TokenStore {
	return &TokenStore{
		tokens: make(map[string]*AuthUser),
	}
}

// Store stores a token for a user.
func (ts *TokenStore) Store(token string, user *AuthUser) {
	ts.tokens[token] = user
}

// Validate returns the user for a token, or nil if invalid.
func (ts *TokenStore) Validate(token string) *AuthUser {
	return ts.tokens[token]
}

// LoggingMiddleware logs all requests with timing information.
func LoggingMiddleware() aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			start := time.Now()

			result, err := next(ctx, req)

			duration := time.Since(start)
			if err != nil {
				log.Printf("[%s] %s - ERROR: %v (%s)", req.ID, req.Method, err, duration)
			} else {
				log.Printf("[%s] %s - OK (%s)", req.ID, req.Method, duration)
			}

			return result, err
		}
	}
}

// AuthMiddleware validates tokens and sets up user context.
// It also associates the user with the connection for targeted push.
func AuthMiddleware(tokenStore *TokenStore) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			info := aprot.HandlerInfoFromContext(ctx)

			// Skip auth for methods that don't require it
			if info == nil || !info.Options.RequireAuth {
				return next(ctx, req)
			}

			// Extract token from params
			var params struct {
				AuthToken string `json:"auth_token"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, aprot.ErrUnauthorized("invalid request format")
			}

			if params.AuthToken == "" {
				return nil, aprot.ErrUnauthorized("authentication required")
			}

			// Validate token
			user := tokenStore.Validate(params.AuthToken)
			if user == nil {
				return nil, aprot.ErrUnauthorized("invalid or expired token")
			}

			// Associate user with connection for targeted push
			conn := aprot.Connection(ctx)
			if conn != nil {
				conn.SetUserID(user.ID)
			}

			// Add user to context
			ctx = context.WithValue(ctx, authUserKey, user)
			return next(ctx, req)
		}
	}
}
