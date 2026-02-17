package aprot

import "context"

type contextKey int

const (
	progressKey contextKey = iota
	connectionKey
	handlerInfoKey
	requestKey
	taskTreeKey
	taskNodeKey
	sharedContextKey
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

// HandlerInfoFromContext returns the HandlerInfo from the context.
// Returns nil if not present.
func HandlerInfoFromContext(ctx context.Context) *HandlerInfo {
	if info, ok := ctx.Value(handlerInfoKey).(*HandlerInfo); ok {
		return info
	}
	return nil
}

// RequestFromContext returns the Request from the context.
// Returns nil if not present.
func RequestFromContext(ctx context.Context) *Request {
	if req, ok := ctx.Value(requestKey).(*Request); ok {
		return req
	}
	return nil
}

// withHandlerInfo returns a context with the given handler info.
func withHandlerInfo(ctx context.Context, info *HandlerInfo) context.Context {
	return context.WithValue(ctx, handlerInfoKey, info)
}

// withRequest returns a context with the given request.
func withRequest(ctx context.Context, req *Request) context.Context {
	return context.WithValue(ctx, requestKey, req)
}

// taskTreeFromContext returns the taskTree from the context.
// Returns nil if not present.
func taskTreeFromContext(ctx context.Context) *taskTree {
	if t, ok := ctx.Value(taskTreeKey).(*taskTree); ok {
		return t
	}
	return nil
}

// taskNodeFromContext returns the current taskNode from the context.
// Returns nil if not present.
func taskNodeFromContext(ctx context.Context) *taskNode {
	if n, ok := ctx.Value(taskNodeKey).(*taskNode); ok {
		return n
	}
	return nil
}

// withTaskTree returns a context with the given task tree.
func withTaskTree(ctx context.Context, t *taskTree) context.Context {
	return context.WithValue(ctx, taskTreeKey, t)
}

// withTaskNode returns a context with the given task node.
func withTaskNode(ctx context.Context, n *taskNode) context.Context {
	return context.WithValue(ctx, taskNodeKey, n)
}

// sharedCtxFromContext returns the sharedContext from the context.
// Returns nil if not present.
func sharedCtxFromContext(ctx context.Context) *sharedContext {
	if sc, ok := ctx.Value(sharedContextKey).(*sharedContext); ok {
		return sc
	}
	return nil
}

// withSharedContext returns a context with the given shared context.
func withSharedContext(ctx context.Context, sc *sharedContext) context.Context {
	return context.WithValue(ctx, sharedContextKey, sc)
}
