package aprot

import "context"

// address.go holds the request-scoped address seam (#336): the fan-out
// address carried uniformly on every dispatch path, so a handler can name
// where its user is reachable without knowing which transport it arrived
// on.
//
// The address is the routing key for [Server.PushToUser] and
// [Server.DisconnectUser]. It is not an identity: the principal
// ([PrincipalFrom]) answers "who is asking" and is what authorization
// reads. Consumers set both when they coincide; aprot never derives one
// from the other.
//
// Population per transport:
//
//   - Socket (WebSocket/SSE): [Conn.SetUserID], typically from the
//     [Server.OnAuth] hook or auth middleware. [UserID] reads it back
//     through the connection.
//   - REST / MCP: a wrapping http.Handler that authenticates the request
//     attaches the address with [WithUserID], alongside [WithPrincipal].

// WithUserID returns a context carrying id as the caller's fan-out address,
// as returned by [UserID]. Request-scoped transports (REST, MCP) use it
// from a wrapping http.Handler after authenticating the request, next to
// [WithPrincipal]:
//
//	ctx := aprot.WithPrincipal(r.Context(), user)
//	ctx = aprot.WithUserID(ctx, user.ID)
//	next.ServeHTTP(w, r.WithContext(ctx))
//
// Socket connections populate the address with [Conn.SetUserID] instead;
// [UserID] reads either one.
//
// An empty id is ignored by [UserID], which falls through to the
// connection: a wrapper that forwards a header unconditionally must not
// blank the address of an attached connection when the header is absent.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserID returns the caller's fan-out address, or "" when the execution
// carries none. It resolves the first non-empty of: the value attached with
// [WithUserID], then [Connection] (ctx).UserID() — so one accessor works on
// every dispatch path, socket or request-scoped.
//
// The address is read through to the connection rather than snapshotted at
// dispatch, so it reflects the connection's current value: a handler in the
// very request whose middleware called [Conn.SetUserID] sees the new
// address, and a server-driven subscription refresh after a mid-session
// re-authentication fans out to where the user is now. The consequence is
// that the address can change while a handler runs — read it once into a
// local when a consistent value is needed.
//
// Never authorize on the address. It answers "where do I reach this user",
// not "who is asking" and not "may they do this"; a non-empty address is
// not evidence that the caller authenticated. Read [PrincipalFrom] for the
// authorization input.
func UserID(ctx context.Context) string {
	if id, ok := ctx.Value(userIDKey).(string); ok && id != "" {
		return id
	}
	if c := Connection(ctx); c != nil {
		return c.UserID()
	}
	return ""
}
