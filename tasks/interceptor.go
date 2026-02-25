package tasks

import (
	"context"

	"github.com/marrasen/aprot"
)

// taskMiddleware returns a middleware that sets up and finalizes the task
// tree, task slot, and task manager on each request context.
func taskMiddleware(tm *taskManager) aprot.Middleware {
	return func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			rs := aprot.RequestSenderFromContext(ctx)
			if rs == nil {
				return next(ctx, req)
			}
			tree := newTaskTree(rs)
			ctx = withTaskTree(ctx, tree)
			slot := &taskSlot{}
			ctx = withTaskSlot(ctx, slot)
			ctx = withTaskManager(ctx, tm)

			result, err := next(ctx, req)

			finalizeTaskSlot(ctx, slot, err)

			return result, err
		}
	}
}

// finalizeTaskSlot completes or fails any inline tasks created during the handler.
func finalizeTaskSlot(ctx context.Context, slot *taskSlot, err error) {
	canceled := ctx.Err() != nil

	// Finalize shared task.
	if core := slot.sharedCore; core != nil {
		if err != nil {
			core.fail(err.Error())
		} else if canceled {
			core.fail("canceled")
		} else {
			core.closeTask()
		}
	}

	// Finalize request-scoped task.
	if node := slot.taskNode; node != nil {
		if err != nil {
			node.setFailed(err.Error())
		} else if canceled {
			node.setFailed("canceled")
		} else {
			node.setStatus(TaskNodeStatusCompleted)
		}
		node.tree.send()
	}
}
