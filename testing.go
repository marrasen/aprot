package aprot

import "context"

// WithTestConnection returns a context carrying a minimal [Conn] with the
// given ID. The connection has no functioning transport and is intended
// exclusively for use in tests.
func WithTestConnection(ctx context.Context, id uint64) context.Context {
	return withConnection(ctx, &Conn{id: id})
}
