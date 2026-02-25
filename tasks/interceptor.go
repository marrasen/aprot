package tasks

import (
	"context"

	"github.com/marrasen/aprot"
)

// taskInterceptor implements aprot.RequestInterceptor for the task system.
type taskInterceptor struct {
	tm *taskManager
}

// BeforeRequest sets up the task tree, task slot, and task manager on the context.
func (ti *taskInterceptor) BeforeRequest(ctx context.Context) context.Context {
	rs := aprot.RequestSenderFromContext(ctx)
	if rs == nil {
		return ctx
	}
	tree := newTaskTree(rs)
	ctx = withTaskTree(ctx, tree)
	slot := &taskSlot{}
	ctx = withTaskSlot(ctx, slot)
	ctx = withTaskManager(ctx, ti.tm)
	return ctx
}

// AfterRequest finalizes any inline tasks created during the handler.
func (ti *taskInterceptor) AfterRequest(ctx context.Context, err error) {
	slot := taskSlotFromContext(ctx)
	if slot == nil {
		return
	}
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

// Ensure interface compliance at compile time.
var _ aprot.RequestInterceptor = (*taskInterceptor)(nil)
