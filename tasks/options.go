package tasks

import "context"

// TaskStartHook is called when a task transitions into the Running state
// (root task or subtask). The returned context replaces the task's context
// for the lifetime of the task — return ctx unchanged to keep it as-is, or
// return a derived context to attach the title to your structured logger,
// trace span, or other context-aware machinery.
//
// parentID is empty for root tasks; for subtasks it is the ID of the parent
// node. Hooks run synchronously on the caller goroutine — keep them fast and
// non-blocking.
type TaskStartHook func(ctx context.Context, id, title, parentID string) context.Context

// TaskEndHook is called exactly once per task when it terminates. err is nil
// on success and non-nil on failure (cancellation surfaces as a non-nil err
// with message "canceled"). Hooks run synchronously on the caller goroutine.
//
// parentID is empty for root tasks.
type TaskEndHook func(ctx context.Context, id, title, parentID string, err error)

// EnableOption configures the task system at registration time. Pass options
// to [Enable] or [EnableWithMeta].
type EnableOption func(*enableOptions)

type enableOptions struct {
	onStart TaskStartHook
	onEnd   TaskEndHook
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

// WithTaskStartHook installs hook h, called whenever a task starts. The
// returned context replaces the task's context for the rest of its lifetime
// (including subtasks). Use it to attach the task title to your logger:
//
//	tasks.Enable(registry, tasks.WithTaskStartHook(
//	    func(ctx context.Context, id, title, parentID string) context.Context {
//	        logger := slog.With("task_id", id, "task_title", title)
//	        return ctxlog.With(ctx, logger)
//	    },
//	))
func WithTaskStartHook(h TaskStartHook) EnableOption {
	return func(o *enableOptions) { o.onStart = h }
}

// WithTaskEndHook installs hook h, called whenever a task ends. err is nil on
// success, non-nil on failure (including cancellation):
//
//	tasks.Enable(registry, tasks.WithTaskEndHook(
//	    func(ctx context.Context, id, title, parentID string, err error) {
//	        if err != nil {
//	            slog.ErrorContext(ctx, "task failed", "id", id, "title", title, "err", err)
//	            return
//	        }
//	        slog.InfoContext(ctx, "task completed", "id", id, "title", title)
//	    },
//	))
func WithTaskEndHook(h TaskEndHook) EnableOption {
	return func(o *enableOptions) { o.onEnd = h }
}
