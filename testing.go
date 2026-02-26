package aprot

import "context"

// WithTestConnection returns a context carrying a minimal [Conn] with the
// given ID. The connection has no functioning transport and is intended
// exclusively for use in tests.
func WithTestConnection(ctx context.Context, id uint64) context.Context {
	return withConnection(ctx, &Conn{id: id})
}

// WithTestRequestSender returns a context carrying the given [RequestSender].
// Intended exclusively for use in tests.
func WithTestRequestSender(ctx context.Context, rs RequestSender) context.Context {
	return withRequestSender(ctx, rs)
}
