package aprot

import "context"

type contextKey int

const (
	progressKey contextKey = iota
	connectionKey
)

// Progress returns the ProgressReporter from the context.
// Returns a no-op reporter if not present.
func Progress(ctx context.Context) ProgressReporter {
	if p, ok := ctx.Value(progressKey).(ProgressReporter); ok {
		return p
	}
	return &noopProgress{}
}

// Connection returns the Connection from the context.
// Returns nil if not present.
func Connection(ctx context.Context) *Conn {
	if c, ok := ctx.Value(connectionKey).(*Conn); ok {
		return c
	}
	return nil
}

// withProgress returns a context with the given progress reporter.
func withProgress(ctx context.Context, p ProgressReporter) context.Context {
	return context.WithValue(ctx, progressKey, p)
}

// withConnection returns a context with the given connection.
func withConnection(ctx context.Context, c *Conn) context.Context {
	return context.WithValue(ctx, connectionKey, c)
}
