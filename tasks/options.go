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
// For scope-based tasks ([SubTask], [SharedSubTask]) the middleware runs
// synchronously on the caller's goroutine. For manual-lifecycle tasks
// ([StartTask], [OutputWriter], [WriterProgress], [Task.SubTask],
// [TaskSub.SubTask]) the middleware runs in a dedicated goroutine and next
// blocks until the task reaches a terminal state (Close/Fail on the handle,
// the writer's Close, request finalization, or a parent cascade-fail).
//
// A middleware that returns without calling next aborts the task body
// (scope-based fn does not run). For manual tasks, the user can still
// call Close/Fail; their signals are buffered and dropped on the floor
// since the goroutine has already exited.
//
// Panics: a panic from the middleware before it calls next propagates to the
// task entry point (for scope-based tasks directly; for manual tasks it is
// forwarded from the internal goroutine to the [StartTask]/SubTask call). A
// panic after next, in a manual task, has no caller left to receive it and
// is re-raised on the internal goroutine.
type TaskMiddleware func(ctx context.Context, info TaskInfo, next func(context.Context) error) error

// TaskCancelInfo describes a shared task whose cancellation is being
// authorized. It is passed to a [CancelAuthorizer].
type TaskCancelInfo struct {
	ID          string // task ID
	Title       string // task title
	OwnerConnID uint64 // connection that created the task
	OwnerUserID string // user that created the task ("" if the owner was unauthenticated)
}

// CancelAuthorizer decides whether the caller identified by ctx may cancel the
// described shared task. Return nil to allow cancellation, or an error
// (typically [aprot.ErrForbidden]) to deny it. The ctx is the requesting
// caller's context, so the authorizer can read aprot.Connection(ctx).UserID()
// and compare it against task.OwnerUserID.
//
// Install one with [WithCancelAuthorizer]. When none is installed the default
// policy applies: only the connection that created the task may cancel it.
type CancelAuthorizer func(ctx context.Context, task TaskCancelInfo) error

// EnableOption configures the task system at registration time. Pass
// options to [Enable] or [EnableWithMeta].
type EnableOption func(*enableOptions)

type enableOptions struct {
	middleware       TaskMiddleware
	cancelAuthorizer CancelAuthorizer
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

// WithCancelAuthorizer installs a policy deciding who may cancel a shared task
// via the built-in CancelTask RPC (and [CancelSharedTask]). Without it, only
// the connection that created a task may cancel it — a policy that also loses
// cancel rights across a reconnect, since it is keyed by connection ID. An
// authorizer can implement any policy, e.g. "any authenticated user" or
// "same user, surviving reconnect":
//
//	tasks.EnableWithMeta[Meta](reg, tasks.WithCancelAuthorizer(
//	    func(ctx context.Context, t tasks.TaskCancelInfo) error {
//	        if aprot.Connection(ctx).UserID() == "" {
//	            return aprot.ErrForbidden("not authenticated")
//	        }
//	        return nil // any authenticated user may cancel any task
//	    },
//	))
func WithCancelAuthorizer(fn CancelAuthorizer) EnableOption {
	return func(o *enableOptions) { o.cancelAuthorizer = fn }
}
