package tasks

import (
	"context"

	"github.com/marrasen/aprot"
)

// taskMiddleware returns a middleware that sets up and finalizes the task
// delivery, task slot, and task manager on each request context.
func taskMiddleware(tm *taskManager) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			rs := aprot.RequestSenderFromContext(ctx)
			if rs == nil {
				return next(ctx, req)
			}
			d := newRequestDelivery(rs)
			ctx = withDelivery(ctx, d)
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
		d.sendSnapshot(nil)
	}
}
