package tasks

import (
	"context"

	"github.com/marrasen/aprot"
)

// taskMiddleware returns a middleware that sets up and finalizes the task
// slot, task manager, and — where there is a client to deliver to — the task
// delivery on each request context.
//
// The manager and the slot are installed on every execution, connection or
// not. The task manager belongs to the [aprot.Server], so a shared task
// started over MCP or REST registers with it and reaches socket watchers
// like any other (#335). Only *delivery* is connection-scoped: with a
// connection, per-request delivery as usual; without one, no delivery at
// all. aprot never fakes a connection to make a connection-shaped path
// work — connection presence is a transport fact, never a capability
// signal (docs/scope.md).
func taskMiddleware(tm *taskManager) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			var d *requestDelivery
			if conn := aprot.Connection(ctx); conn != nil {
				d = newRequestDelivery(conn, req.ID, tm.hooks)
				ctx = withDelivery(ctx, d)
			}
			slot := &taskSlot{}
			ctx = withTaskSlot(ctx, slot)
			ctx = withTaskManager(ctx, tm)

			result, err := next(ctx, req)

			finalizeTaskSlot(ctx, slot, err, d)

			return result, err
		}
	}
}

// finalizeTaskSlot completes or fails any inline tasks created during the handler.
func finalizeTaskSlot(ctx context.Context, slot *taskSlot, err error, d *requestDelivery) {
	node := slot.node
	if node == nil {
		return
	}

	canceled := ctx.Err() != nil

	if node.IsShared() {
		// Shared task: use completeTop/failTop for idempotent lifecycle.
		if err != nil {
			node.failTop(err.Error())
		} else if canceled {
			node.failTop("canceled")
		} else {
			node.completeTop()
		}
	} else {
		// Request-scoped task.
		if err != nil {
			node.setFailed(err.Error())
		} else if canceled {
			node.setFailed("canceled")
		} else {
			node.setStatus(TaskNodeStatusCompleted)
		}
		// Invariant: a request-scoped node reaches the slot only via
		// startRequestTask, which requires a *requestDelivery, so d is
		// non-nil here whenever node is. The guard states that invariant
		// rather than defending against a known case.
		if d != nil {
			d.sendSnapshot(nil)
		}
	}
}
