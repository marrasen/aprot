package tasks

import (
	"context"
	"io"
	"time"

	"github.com/marrasen/aprot"
)

// SubTask creates a child task under the current task node, runs fn, and marks
// the sub-task as completed (or failed if fn returns an error).
//
// When a sharedContext is present on ctx, SubTask routes through the shared
// task system only (broadcast to all clients). Otherwise it uses the
// request-scoped task tree (visible only to the requesting client).
func SubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	sc := sharedCtxFromContext(ctx)
	if sc != nil {
		return sharedSubTaskInner(ctx, sc, title, fn)
	}

	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return fn(ctx)
	}

	parent := taskNodeFromContext(ctx)

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	if parent == nil {
		parent = root
	}

	child := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: TaskNodeStatusCreated,
	}
	parent.addChild(child)
	tree.send()

	child.setStatus(TaskNodeStatusRunning)
	childCtx := withTaskNode(ctx, child)
	err := fn(childCtx)

	if err != nil {
		child.setFailed(err.Error())
	} else {
		child.setStatus(TaskNodeStatusCompleted)
	}
	tree.send()

	return err
}

// sharedSubTaskInner handles SubTask when a shared context is present.
func sharedSubTaskInner(ctx context.Context, sc *sharedContext, title string, fn func(ctx context.Context) error) error {
	var sharedNode *sharedTaskNode
	if sc.node != nil {
		sharedNode = sc.node.subTask(sc.core, title)
	} else {
		sharedNode = sc.core.subTask(title)
	}

	childCtx := withSharedContext(ctx, &sharedContext{core: sc.core, node: sharedNode})
	err := fn(childCtx)

	sharedNode.mu.Lock()
	if err != nil {
		sharedNode.status = TaskNodeStatusFailed
		sharedNode.error = err.Error()
	} else {
		sharedNode.status = TaskNodeStatusCompleted
	}
	sharedNode.mu.Unlock()
	sc.core.manager.broadcastNow()

	return err
}

// Output sends a text output message attached to the nearest task node.
func Output(ctx context.Context, msg string) {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		nodeID := sc.core.id
		if sc.node != nil {
			nodeID = sc.node.id
		}
		sc.core.manager.sendUpdate(nodeID, &msg, nil, nil)
		return
	}
	tree := taskTreeFromContext(ctx)
	if tree != nil {
		taskID := ""
		if node := taskNodeFromContext(ctx); node != nil {
			taskID = node.id
		}
		tree.sender.SendJSON(taskNodeOutputMessage{
			Type:   aprot.TypeProgress,
			ID:     tree.sender.RequestID(),
			TaskID: taskID,
			Output: msg,
		})
	}
}

// TaskProgress sets the progress (current/total) on the current task node.
func TaskProgress(ctx context.Context, current, total int) {
	if sc := sharedCtxFromContext(ctx); sc != nil && sc.node != nil {
		sc.node.mu.Lock()
		sc.node.current = current
		sc.node.total = total
		sc.node.mu.Unlock()
		sc.core.manager.sendUpdate(sc.node.id, nil, &current, &total)
		return
	}
	node := taskNodeFromContext(ctx)
	if node != nil {
		node.setProgress(current, total)
		tree := node.tree
		tree.sender.SendJSON(taskNodeProgressMessage{
			Type:    aprot.TypeProgress,
			ID:      tree.sender.RequestID(),
			TaskID:  node.id,
			Current: current,
			Total:   total,
		})
	}
}

// StepTaskProgress increments the current progress on the current task node by step.
func StepTaskProgress(ctx context.Context, step int) {
	if sc := sharedCtxFromContext(ctx); sc != nil && sc.node != nil {
		sc.node.mu.Lock()
		sc.node.current += step
		current := sc.node.current
		total := sc.node.total
		sc.node.mu.Unlock()
		sc.core.manager.sendUpdate(sc.node.id, nil, &current, &total)
		return
	}
	node := taskNodeFromContext(ctx)
	if node != nil {
		current, total := node.stepProgress(step)
		tree := node.tree
		tree.sender.SendJSON(taskNodeProgressMessage{
			Type:    aprot.TypeProgress,
			ID:      tree.sender.RequestID(),
			TaskID:  node.id,
			Current: current,
			Total:   total,
		})
	}
}

// OutputWriter returns an io.WriteCloser that creates a child task node
// and sends each Write as output attached to that node.
func OutputWriter(ctx context.Context, title string) io.WriteCloser {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		var node *sharedTaskNode
		if sc.node != nil {
			node = sc.node.subTask(sc.core, title)
		} else {
			node = sc.core.subTask(title)
		}
		return &sharedOutputWriter{core: sc.core, node: node}
	}

	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return discardWriteCloser{}
	}

	parent := taskNodeFromContext(ctx)

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	if parent == nil {
		parent = root
	}

	child := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: TaskNodeStatusCreated,
	}
	parent.addChild(child)
	tree.send()

	child.setStatus(TaskNodeStatusRunning)
	return &taskOutputWriter{
		tree: tree,
		node: child,
	}
}

// WriterProgress returns an io.WriteCloser that creates a child task node
// tracking bytes written as progress (current/total).
func WriterProgress(ctx context.Context, title string, size int) io.WriteCloser {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		var node *sharedTaskNode
		if sc.node != nil {
			node = sc.node.subTask(sc.core, title)
		} else {
			node = sc.core.subTask(title)
		}
		node.mu.Lock()
		node.total = size
		node.mu.Unlock()
		return &sharedProgressWriter{
			core:     sc.core,
			node:     node,
			total:    size,
			lastSend: time.Now(),
		}
	}

	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return discardWriteCloser{}
	}

	parent := taskNodeFromContext(ctx)

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	if parent == nil {
		parent = root
	}

	child := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: TaskNodeStatusCreated,
		total:  size,
	}
	parent.addChild(child)
	tree.send()

	child.setStatus(TaskNodeStatusRunning)
	return &taskProgressWriter{
		tree:     tree,
		node:     child,
		total:    size,
		lastSend: time.Now(),
	}
}

// Task is a type-safe, generic wrapper around a request-scoped task node.
type Task[M any] struct {
	node *taskNode
}

// ID returns the task's unique identifier.
func (t *Task[M]) ID() string {
	return t.node.id
}

// Progress updates the task's progress counters.
func (t *Task[M]) Progress(current, total int) {
	t.node.setProgress(current, total)
	tree := t.node.tree
	tree.sender.SendJSON(taskNodeProgressMessage{
		Type:    aprot.TypeProgress,
		ID:      tree.sender.RequestID(),
		TaskID:  t.node.id,
		Current: current,
		Total:   total,
	})
}

// SetMeta sets typed metadata on the task.
func (t *Task[M]) SetMeta(v M) {
	t.node.mu.Lock()
	t.node.meta = v
	t.node.mu.Unlock()
	t.node.tree.send()
}

// Close marks the task as completed.
func (t *Task[M]) Close() {
	t.node.setStatus(TaskNodeStatusCompleted)
	t.node.tree.send()
}

// Fail marks the task as failed with the given error message.
func (t *Task[M]) Fail(message string) {
	t.node.setFailed(message)
	t.node.tree.send()
}

// Err fails the task with err.Error() if err is non-nil, or completes it if nil.
func (t *Task[M]) Err(err error) {
	if err != nil {
		t.Fail(err.Error())
	} else {
		t.Close()
	}
}

// StartTask creates a request-scoped task visible to the calling client.
func StartTask[M any](ctx context.Context, title string) (context.Context, *Task[M]) {
	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return ctx, nil
	}

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	node := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: TaskNodeStatusCreated,
	}
	root.addChild(node)

	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.taskNode = node
	}

	ctx = withTaskNode(ctx, node)
	tree.send()

	node.setStatus(TaskNodeStatusRunning)
	return ctx, &Task[M]{node: node}
}

// StartSharedTask creates a new shared task visible to all clients.
func StartSharedTask[M any](ctx context.Context, title string) (context.Context, *SharedTask[M]) {
	conn := aprot.Connection(ctx)
	if conn == nil {
		return ctx, nil
	}
	tm := taskManagerFromContext(ctx)
	if tm == nil {
		return ctx, nil
	}
	core := tm.create(title, conn.ID(), true, ctx)
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()
	tm.broadcastNow()

	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.sharedCore = core
	}

	ctx = withSharedContext(ctx, &sharedContext{core: core})

	return ctx, &SharedTask[M]{core: core}
}

// SharedSubTask bridges the request-scoped and shared task systems.
func SharedSubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		return SubTask(ctx, title, fn)
	}

	conn := aprot.Connection(ctx)
	var tm *taskManager
	if conn != nil {
		tm = taskManagerFromContext(ctx)
	}

	if tm == nil {
		return SubTask(ctx, title, fn)
	}

	core := tm.create(title, conn.ID(), false, ctx)
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()
	tm.broadcastNow()
	sc := &sharedContext{core: core}
	childCtx := withSharedContext(ctx, sc)

	err := SubTask(childCtx, title, fn)

	if err != nil {
		core.fail(err.Error())
	} else {
		core.closeTask()
	}

	return err
}

// CancelSharedTask cancels a shared task by ID.
func CancelSharedTask(ctx context.Context, taskID string) error {
	tm := taskManagerFromContext(ctx)
	if tm == nil {
		return aprot.NewError(aprot.CodeInternalError, "tasks not enabled")
	}
	if !tm.cancelTask(taskID) {
		return aprot.NewError(aprot.CodeInvalidParams, "task not found: "+taskID)
	}
	return nil
}
