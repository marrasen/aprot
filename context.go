package aprot

import (
	"context"
	"sync"
)

type contextKey int

const (
	progressKey contextKey = iota
	connectionKey
	handlerInfoKey
	requestKey
	triggerCollectorKey
	refreshQueueKey
	streamCompleteHooksKey
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

// CancelCause returns the reason the request context was canceled.
// Returns nil if the context has not been canceled or no cause was set.
// The returned error will be one of ErrClientCanceled, ErrConnectionClosed,
// or ErrServerShutdown.
func CancelCause(ctx context.Context) error {
	return context.Cause(ctx)
}

// StreamCompleteHook is invoked once per streaming request after the
// handler's iterator has finished executing — whether through clean
// completion, client cancellation, connection loss, server shutdown, a
// mid-stream panic, or a transport send failure.
//
// Parameters:
//
//   - err is nil for clean completion, or the cause of termination
//     otherwise. For cancellation-driven termination, err is the
//     context cause sentinel: one of [ErrClientCanceled],
//     [ErrConnectionClosed], or [ErrServerShutdown] — use [errors.Is]
//     to distinguish. For panics it is a wrapped recover value. For
//     transport failures it is the underlying transport error.
//   - items is the number of elements successfully yielded to the
//     client. For [iter.Seq2], each (key, value) pair counts as one.
//
// Preflight errors — i.e. handlers that return (nil, err) before any
// iterator is produced — do NOT invoke the hook. They travel as the
// regular error return from the middleware chain and should be logged
// there, the same way unary handler errors are today.
type StreamCompleteHook func(err error, items int)

// streamHooks holds the ordered list of StreamCompleteHook callbacks
// registered against a single streaming request's context.
type streamHooks struct {
	mu    sync.Mutex
	hooks []StreamCompleteHook
}

func (h *streamHooks) add(hook StreamCompleteHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks = append(h.hooks, hook)
}

// run invokes every registered callback with the final outcome. Called
// by the dispatcher after streamIterator has returned. Hooks fire in
// registration order; a panic in one hook does not prevent later hooks
// from running.
func (h *streamHooks) run(err error, items int) {
	h.mu.Lock()
	hooks := make([]StreamCompleteHook, len(h.hooks))
	copy(hooks, h.hooks)
	h.mu.Unlock()
	for _, hook := range hooks {
		func() {
			defer func() { _ = recover() }()
			hook(err, items)
		}()
	}
}

// OnStreamComplete registers a callback that fires after a streaming
// handler's iterator has finished. Typically called from middleware
// before dispatching to the next handler, so the callback closure can
// capture the start time and emit a log entry once iteration finishes.
//
// Example logging middleware that logs duration and item count for
// both unary and streaming handlers:
//
//	func Logging(next aprot.Handler) aprot.Handler {
//	    return func(ctx context.Context, req *aprot.Request) (any, error) {
//	        start := time.Now()
//	        aprot.OnStreamComplete(ctx, func(err error, items int) {
//	            slog.Info("stream done",
//	                "method", req.Method,
//	                "dur", time.Since(start),
//	                "items", items,
//	                "err", err)
//	        })
//	        result, err := next(ctx, req)
//	        if info := aprot.HandlerInfoFromContext(ctx); info != nil &&
//	            info.Kind != aprot.HandlerKindUnary {
//	            // Streaming handler: hook will fire later (unless this is
//	            // a preflight error, in which case log it here).
//	            if err != nil {
//	                slog.Error("stream preflight error",
//	                    "method", req.Method,
//	                    "dur", time.Since(start),
//	                    "err", err)
//	            }
//	            return result, err
//	        }
//	        slog.Info("unary done",
//	            "method", req.Method, "dur", time.Since(start), "err", err)
//	        return result, err
//	    }
//	}
//
// Calling OnStreamComplete on a unary handler's context is a no-op —
// the hooks slot is only populated for requests whose handler returns
// an iter.Seq shape.
func OnStreamComplete(ctx context.Context, hook StreamCompleteHook) {
	if hook == nil {
		return
	}
	h, ok := ctx.Value(streamCompleteHooksKey).(*streamHooks)
	if !ok || h == nil {
		return
	}
	h.add(hook)
}

// withStreamCompleteHooks attaches a streamHooks container to ctx and
// returns both the new ctx and the container. Called by the dispatcher
// only for streaming handler invocations.
func withStreamCompleteHooks(ctx context.Context) (context.Context, *streamHooks) {
	h := &streamHooks{}
	return context.WithValue(ctx, streamCompleteHooksKey, h), h
}
