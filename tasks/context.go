package tasks

import "context"

type contextKey int

const (
	taskTreeKey contextKey = iota
	taskNodeKey
	sharedContextKey
	taskSlotKey
	taskManagerKey
)

// taskTreeFromContext returns the taskTree from the context.
func taskTreeFromContext(ctx context.Context) *taskTree {
	if t, ok := ctx.Value(taskTreeKey).(*taskTree); ok {
		return t
	}
	return nil
}

// taskNodeFromContext returns the current taskNode from the context.
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

// taskSlot is a mutable slot placed on the context by the interceptor.
// StartTask and StartSharedTask populate it so AfterRequest can detect
// inline tasks after the handler returns and auto-manage their lifecycle.
type taskSlot struct {
	sharedCore *sharedTaskCore
	taskNode   *taskNode
}

// taskSlotFromContext returns the taskSlot from the context.
func taskSlotFromContext(ctx context.Context) *taskSlot {
	if s, ok := ctx.Value(taskSlotKey).(*taskSlot); ok {
		return s
	}
	return nil
}

// withTaskSlot returns a context with the given task slot.
func withTaskSlot(ctx context.Context, s *taskSlot) context.Context {
	return context.WithValue(ctx, taskSlotKey, s)
}

// taskManagerFromContext returns the taskManager from the context.
func taskManagerFromContext(ctx context.Context) *taskManager {
	if tm, ok := ctx.Value(taskManagerKey).(*taskManager); ok {
		return tm
	}
	return nil
}

// withTaskManager returns a context with the given task manager.
func withTaskManager(ctx context.Context, tm *taskManager) context.Context {
	return context.WithValue(ctx, taskManagerKey, tm)
}
