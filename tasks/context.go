package tasks

import "context"

type contextKey int

const (
	deliveryKey    contextKey = iota
	taskNodeKey
	taskSlotKey
	taskManagerKey
)

// deliveryFromContext returns the taskDelivery from the context.
func deliveryFromContext(ctx context.Context) taskDelivery {
	if d, ok := ctx.Value(deliveryKey).(taskDelivery); ok {
		return d
	}
	return nil
}

// withDelivery returns a context with the given delivery.
func withDelivery(ctx context.Context, d taskDelivery) context.Context {
	return context.WithValue(ctx, deliveryKey, d)
}

// taskNodeFromContext returns the current taskNode from the context.
func taskNodeFromContext(ctx context.Context) *taskNode {
	if n, ok := ctx.Value(taskNodeKey).(*taskNode); ok {
		return n
	}
	return nil
}

// withTaskNode returns a context with the given task node.
func withTaskNode(ctx context.Context, n *taskNode) context.Context {
	return context.WithValue(ctx, taskNodeKey, n)
}

// taskSlot is a mutable slot placed on the context by the task middleware.
// StartTask populates it so the middleware can detect inline tasks after the
// handler returns and auto-manage their lifecycle.
type taskSlot struct {
	node *taskNode // inline task (shared or request-scoped)
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
