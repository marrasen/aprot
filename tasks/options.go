package tasks

import "context"

// TaskInfo carries identity for a task lifecycle invocation.
type TaskInfo struct {
	ID       string
	Title    string
	ParentID string // empty for root tasks
}

// TaskMiddleware wraps task execution. The middleware receives the task's
// ctx and TaskInfo, and must call next(ctx) exactly once to allow the task
// to proceed. The ctx passed to next becomes the task's context for the
// duration of execution — decorate it to attach a logger, span, or any
// other ctx-aware machinery. next returns the task's terminal error
// (nil on success; non-nil on failure, including cancellation which
// surfaces with err.Error() == "canceled").
//
// For scope-based tasks ([SubTask], [OutputWriter], [WriterProgress],
// [SharedSubTask]) the middleware runs synchronously on the caller's
// goroutine. For manual lifecycle tasks created via [StartTask], the
// middleware runs in a dedicated goroutine and next blocks until the user
// calls Close or Fail on the returned task handle.
//
// A middleware that returns without calling next aborts the task body
// (scope-based fn does not run). For manual tasks, the user can still
// call Close/Fail; their signals are buffered and dropped on the floor
// since the goroutine has already exited.
type TaskMiddleware func(ctx context.Context, info TaskInfo, next func(context.Context) error) error

// EnableOption configures the task system at registration time. Pass
// options to [Enable] or [EnableWithMeta].
type EnableOption func(*enableOptions)

type enableOptions struct {
	middleware TaskMiddleware
}

func buildEnableOptions(opts []EnableOption) *enableOptions {
	o := &enableOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	return o
}

// WithTaskMiddleware installs middleware mw, called for every task and
// subtask managed by the task system. The middleware's ctx propagates to
// the task body and to any nested subtasks, so logger/span decorations
// flow through the entire task tree.
//
// Example wiring with slog:
//
//	tasks.Enable(registry, tasks.WithTaskMiddleware(
//	    func(ctx context.Context, info tasks.TaskInfo, next func(context.Context) error) error {
//	        logger := slog.With("task_id", info.ID, "task_title", info.Title)
//	        ctx = ctxlog.With(ctx, logger)
//	        logger.InfoContext(ctx, "task started")
//	        err := next(ctx)
//	        if err != nil {
//	            logger.ErrorContext(ctx, "task failed", "err", err)
//	        } else {
//	            logger.InfoContext(ctx, "task completed")
//	        }
//	        return err
//	    },
//	))
func WithTaskMiddleware(mw TaskMiddleware) EnableOption {
	return func(o *enableOptions) { o.middleware = mw }
}
