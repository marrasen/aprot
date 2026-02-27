package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marrasen/aprot"
)

// sharedTestHandler is a minimal handler for creating a valid server in
// shared-task tests. The name avoids colliding with anything in other test
// files.
type sharedTestHandler struct{}

func (h *sharedTestHandler) Ping(ctx context.Context) error { return nil }

// setupTestServer creates an aprot server and a taskManager.
// It registers the minimal push events required by taskManager.broadcastNow.
func setupTestServer(t *testing.T) (*aprot.Server, *taskManager) {
	t.Helper()
	registry := aprot.NewRegistry()
	handler := &sharedTestHandler{}
	registry.Register(handler)
	registry.RegisterPushEventFor(handler, TaskStateEvent{})
	registry.RegisterPushEventFor(handler, TaskUpdateEvent{})

	server := aprot.NewServer(registry)
	tm := newTaskManager(server)
	t.Cleanup(func() {
		tm.stop()
		server.Stop(context.Background()) //nolint:errcheck
	})
	return server, tm
}

// findTaskByID returns the SharedTaskState with the given ID from states, or
// the zero value and false if not found.
func findTaskByID(states []SharedTaskState, id string) (SharedTaskState, bool) {
	for _, s := range states {
		if s.ID == id {
			return s, true
		}
	}
	return SharedTaskState{}, false
}

// TestSharedTaskBasic verifies that a newly created task appears in the
// snapshot and is removed after it is closed.
func TestSharedTaskBasic(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("basic task", 1, true, context.Background())
	id := node.id

	states := tm.snapshotAll()
	state, ok := findTaskByID(states, id)
	if !ok {
		t.Fatalf("expected task %s in snapshot, not found", id)
	}
	if state.Title != "basic task" {
		t.Errorf("title: got %q, want %q", state.Title, "basic task")
	}
	if state.Status != TaskNodeStatusCreated {
		t.Errorf("status: got %v, want Created", state.Status)
	}

	node.completeTop()

	// Allow time for the deferred removal (200 ms delay + buffer).
	time.Sleep(300 * time.Millisecond)

	states = tm.snapshotAll()
	if _, found := findTaskByID(states, id); found {
		t.Errorf("task %s should have been removed after close", id)
	}
}

// TestSharedTaskProgress verifies that progress values are reflected in the
// snapshot immediately after calling progress().
func TestSharedTaskProgress(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("progress task", 1, true, context.Background())

	node.progress(3, 10)

	state := node.sharedSnapshot()
	if state.Current != 3 {
		t.Errorf("current: got %d, want 3", state.Current)
	}
	if state.Total != 10 {
		t.Errorf("total: got %d, want 10", state.Total)
	}
}

// TestSharedTaskSubTask verifies that sub-tasks appear in the children list of
// the snapshot and can be independently managed.
func TestSharedTaskSubTask(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("parent task", 1, true, context.Background())
	child := node.createChild("child task")

	if child == nil {
		t.Fatal("createChild returned nil")
	}
	if child.title != "child task" {
		t.Errorf("child title: got %q, want %q", child.title, "child task")
	}

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(state.Children))
	}
	if state.Children[0].Title != "child task" {
		t.Errorf("child title in snapshot: got %q, want %q", state.Children[0].Title, "child task")
	}
	if state.Children[0].Status != TaskNodeStatusRunning {
		t.Errorf("child status: got %v, want Running", state.Children[0].Status)
	}

	// Update child progress and verify it appears in the next snapshot.
	child.mu.Lock()
	child.current = 5
	child.total = 20
	child.mu.Unlock()

	state = node.sharedSnapshot()
	if state.Children[0].Current != 5 {
		t.Errorf("child current: got %d, want 5", state.Children[0].Current)
	}
	if state.Children[0].Total != 20 {
		t.Errorf("child total: got %d, want 20", state.Children[0].Total)
	}
}

// TestSharedTaskCancel verifies that canceling a task marks it as failed with
// "canceled", cancels its context, and eventually removes it from the manager.
func TestSharedTaskCancel(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("cancel task", 1, true, context.Background())
	id := node.id
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	ok := tm.cancelTask(id)
	if !ok {
		t.Fatal("cancelTask returned false for existing task")
	}

	state := node.sharedSnapshot()
	if state.Status != TaskNodeStatusFailed {
		t.Errorf("status after cancel: got %v, want Failed", state.Status)
	}
	if state.Error != "canceled" {
		t.Errorf("error after cancel: got %q, want %q", state.Error, "canceled")
	}

	// Task context should be canceled.
	select {
	case <-node.ctx.Done():
		// expected
	default:
		t.Error("task context was not canceled after cancelTask")
	}

	// Task should be removed after the deferred delay.
	time.Sleep(300 * time.Millisecond)

	states := tm.snapshotAll()
	if _, found := findTaskByID(states, id); found {
		t.Errorf("task %s should have been removed after cancel", id)
	}
}

// TestSharedTaskCancelNonExistent verifies that cancelTask returns false for
// an unknown task ID.
func TestSharedTaskCancelNonExistent(t *testing.T) {
	_, tm := setupTestServer(t)

	ok := tm.cancelTask("nonexistent-id")
	if ok {
		t.Error("cancelTask should return false for unknown ID")
	}
}

// TestSharedTaskFail verifies that failTop() sets the correct status and error
// message in the snapshot and cancels the task context.
func TestSharedTaskFail(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("fail task", 1, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	node.failTop("something went wrong")

	state := node.sharedSnapshot()
	if state.Status != TaskNodeStatusFailed {
		t.Errorf("status: got %v, want Failed", state.Status)
	}
	if state.Error != "something went wrong" {
		t.Errorf("error: got %q, want %q", state.Error, "something went wrong")
	}

	// failTop() must cancel the task context.
	select {
	case <-node.ctx.Done():
		// expected
	default:
		t.Error("task context was not canceled after failTop()")
	}
}

// TestSharedTaskErr verifies that Err(nil) completes the task and cancels its
// context, and Err(non-nil) fails the task and cancels its context.
func TestSharedTaskErr(t *testing.T) {
	t.Run("nil error completes", func(t *testing.T) {
		_, tm := setupTestServer(t)

		node := tm.create("err nil task", 1, true, context.Background())
		node.mu.Lock()
		node.status = TaskNodeStatusRunning
		node.mu.Unlock()

		task := &Task[string]{node: node}
		task.Err(nil)

		state := node.sharedSnapshot()
		if state.Status != TaskNodeStatusCompleted {
			t.Errorf("status: got %v, want Completed", state.Status)
		}

		select {
		case <-node.ctx.Done():
			// expected — Err(nil) calls Close which cancels the context
		default:
			t.Error("task context was not canceled after Err(nil)")
		}
	})

	t.Run("non-nil error fails", func(t *testing.T) {
		_, tm := setupTestServer(t)

		node := tm.create("err non-nil task", 1, true, context.Background())
		node.mu.Lock()
		node.status = TaskNodeStatusRunning
		node.mu.Unlock()

		task := &Task[string]{node: node}
		task.Err(errors.New("test error"))

		state := node.sharedSnapshot()
		if state.Status != TaskNodeStatusFailed {
			t.Errorf("status: got %v, want Failed", state.Status)
		}
		if state.Error != "test error" {
			t.Errorf("error: got %q, want %q", state.Error, "test error")
		}

		select {
		case <-node.ctx.Done():
			// expected — Err(err) calls Fail which cancels the context
		default:
			t.Error("task context was not canceled after Err(non-nil)")
		}
	})
}

// TestSharedTaskSubErr verifies that TaskSub.Err(nil) completes the
// sub-task and Err(non-nil) fails it.
func TestSharedTaskSubErr(t *testing.T) {
	t.Run("nil error completes", func(t *testing.T) {
		_, tm := setupTestServer(t)

		node := tm.create("sub err nil", 1, true, context.Background())
		task := &Task[string]{node: node}
		sub := task.SubTask("sub nil")

		sub.Err(nil)

		state := node.sharedSnapshot()
		if len(state.Children) != 1 {
			t.Fatalf("expected 1 child, got %d", len(state.Children))
		}
		if state.Children[0].Status != TaskNodeStatusCompleted {
			t.Errorf("sub status: got %v, want Completed", state.Children[0].Status)
		}
	})

	t.Run("non-nil error fails", func(t *testing.T) {
		_, tm := setupTestServer(t)

		node := tm.create("sub err non-nil", 1, true, context.Background())
		task := &Task[string]{node: node}
		sub := task.SubTask("sub fail")

		sub.Err(errors.New("sub error"))

		state := node.sharedSnapshot()
		if len(state.Children) != 1 {
			t.Fatalf("expected 1 child, got %d", len(state.Children))
		}
		if state.Children[0].Status != TaskNodeStatusFailed {
			t.Errorf("sub status: got %v, want Failed", state.Children[0].Status)
		}
		if state.Children[0].Error != "sub error" {
			t.Errorf("sub error: got %q, want %q", state.Children[0].Error, "sub error")
		}
	})
}

// TestSharedTaskSetMeta verifies that typed metadata is stored and appears in
// the snapshot.
func TestSharedTaskSetMeta(t *testing.T) {
	type meta struct {
		UserName string
	}

	_, tm := setupTestServer(t)

	node := tm.create("meta task", 1, true, context.Background())
	task := &Task[meta]{node: node}
	task.SetMeta(meta{UserName: "alice"})

	state := node.sharedSnapshot()
	got, ok := state.Meta.(meta)
	if !ok {
		t.Fatalf("meta type assertion failed: %T", state.Meta)
	}
	if got.UserName != "alice" {
		t.Errorf("UserName: got %q, want %q", got.UserName, "alice")
	}
}

// TestSharedTaskSubSetMeta verifies that metadata can be set on a sub-task.
func TestSharedTaskSubSetMeta(t *testing.T) {
	type meta struct {
		Step int
	}

	_, tm := setupTestServer(t)

	node := tm.create("parent meta", 1, true, context.Background())
	task := &Task[meta]{node: node}
	sub := task.SubTask("sub meta")
	sub.SetMeta(meta{Step: 7})

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(state.Children))
	}
	got, ok := state.Children[0].Meta.(meta)
	if !ok {
		t.Fatalf("sub meta type assertion failed: %T", state.Children[0].Meta)
	}
	if got.Step != 7 {
		t.Errorf("Step: got %d, want 7", got.Step)
	}
}

// TestSharedTaskSubSubTask verifies 3-level nesting: task -> sub -> sub.
func TestSharedTaskSubSubTask(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("top", 1, true, context.Background())
	task := &Task[struct{}]{node: node}

	level1 := task.SubTask("level-1")
	level2 := level1.SubTask("level-2")
	if level2 == nil {
		t.Fatal("SubTask on TaskSub returned nil")
	}

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 top-level child, got %d", len(state.Children))
	}
	level1State := state.Children[0]
	if level1State.Title != "level-1" {
		t.Errorf("level1 title: got %q, want %q", level1State.Title, "level-1")
	}
	if len(level1State.Children) != 1 {
		t.Fatalf("expected 1 level-2 child, got %d", len(level1State.Children))
	}
	if level1State.Children[0].Title != "level-2" {
		t.Errorf("level2 title: got %q, want %q", level1State.Children[0].Title, "level-2")
	}
	if level1State.Children[0].Status != TaskNodeStatusRunning {
		t.Errorf("level2 status: got %v, want Running", level1State.Children[0].Status)
	}
}

// TestSharedTaskSnapshotForConn verifies that IsOwner is true for the owning
// connection and false for others.
func TestSharedTaskSnapshotForConn(t *testing.T) {
	_, tm := setupTestServer(t)

	const ownerID uint64 = 42
	const otherID uint64 = 99

	node := tm.create("owner task", ownerID, true, context.Background())

	ownerState := node.sharedSnapshotForConn(ownerID)
	if !ownerState.IsOwner {
		t.Error("IsOwner should be true for the owning connection")
	}

	otherState := node.sharedSnapshotForConn(otherID)
	if otherState.IsOwner {
		t.Error("IsOwner should be false for a different connection")
	}
}

// TestSharedTaskSnapshotForConn_NonTopLevel verifies that IsOwner is always
// false for non-top-level tasks, even when queried by the owner connection.
func TestSharedTaskSnapshotForConn_NonTopLevel(t *testing.T) {
	_, tm := setupTestServer(t)

	const ownerID uint64 = 42

	node := tm.create("sub task", ownerID, false, context.Background())

	ownerState := node.sharedSnapshotForConn(ownerID)
	if ownerState.IsOwner {
		t.Error("IsOwner should be false for non-top-level tasks")
	}
}

// TestSharedTaskSnapshotAllForConn verifies that snapshotAllForConn returns
// IsOwner=true only for tasks owned by the queried connection.
func TestSharedTaskSnapshotAllForConn(t *testing.T) {
	_, tm := setupTestServer(t)

	const conn1 uint64 = 1
	const conn2 uint64 = 2

	node1 := tm.create("task-conn1", conn1, true, context.Background())
	node2 := tm.create("task-conn2", conn2, true, context.Background())

	states1 := tm.snapshotAllForConn(conn1)
	s1, ok := findTaskByID(states1, node1.id)
	if !ok {
		t.Fatal("task for conn1 not found in conn1 snapshot")
	}
	if !s1.IsOwner {
		t.Error("conn1's task should have IsOwner=true for conn1")
	}
	s2, ok := findTaskByID(states1, node2.id)
	if !ok {
		t.Fatal("task for conn2 not found in conn1 snapshot")
	}
	if s2.IsOwner {
		t.Error("conn2's task should have IsOwner=false for conn1")
	}

	states2 := tm.snapshotAllForConn(conn2)
	s1InConn2, ok := findTaskByID(states2, node1.id)
	if !ok {
		t.Fatal("task for conn1 not found in conn2 snapshot")
	}
	if s1InConn2.IsOwner {
		t.Error("conn1's task should have IsOwner=false for conn2")
	}
	s2InConn2, ok := findTaskByID(states2, node2.id)
	if !ok {
		t.Fatal("task for conn2 not found in conn2 snapshot")
	}
	if !s2InConn2.IsOwner {
		t.Error("conn2's task should have IsOwner=true for conn2")
	}
}

// TestSubTaskWithSharedContext verifies that SubTask routes through the shared
// task system when a shared delivery and node are present on the context.
func TestSubTaskWithSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("shared root", 1, true, context.Background())
	ctx := withDelivery(context.Background(), node.delivery)
	ctx = withTaskNode(ctx, node)

	var fnCalled bool
	err := SubTask(ctx, "shared-sub", func(childCtx context.Context) error {
		fnCalled = true
		// The child context must carry a task node with shared delivery.
		childNode := taskNodeFromContext(childCtx)
		if childNode == nil {
			t.Error("childCtx has no task node")
		} else if !childNode.IsShared() {
			t.Error("childCtx node should be shared")
		}
		return nil
	})
	if err != nil {
		t.Errorf("SubTask returned error: %v", err)
	}
	if !fnCalled {
		t.Error("SubTask fn was not called")
	}

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 child after SubTask, got %d", len(state.Children))
	}
	if state.Children[0].Title != "shared-sub" {
		t.Errorf("child title: got %q, want %q", state.Children[0].Title, "shared-sub")
	}
	if state.Children[0].Status != TaskNodeStatusCompleted {
		t.Errorf("child status: got %v, want Completed", state.Children[0].Status)
	}
}

// TestTaskProgressSharedContext verifies that TaskProgress updates the
// sub-task node when a shared node is present on context.
func TestTaskProgressSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("progress root", 1, true, context.Background())
	child := node.createChild("progress node")

	ctx := withDelivery(context.Background(), child.delivery)
	ctx = withTaskNode(ctx, child)

	TaskProgress(ctx, 4, 12)

	child.mu.Lock()
	current := child.current
	total := child.total
	child.mu.Unlock()

	if current != 4 {
		t.Errorf("node current: got %d, want 4", current)
	}
	if total != 12 {
		t.Errorf("node total: got %d, want 12", total)
	}
}

// TestStepTaskProgressSharedContext verifies that StepTaskProgress increments
// the sub-task node progress correctly.
func TestStepTaskProgressSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("step root", 1, true, context.Background())
	child := node.createChild("step node")
	child.mu.Lock()
	child.current = 2
	child.total = 10
	child.mu.Unlock()

	ctx := withDelivery(context.Background(), child.delivery)
	ctx = withTaskNode(ctx, child)

	StepTaskProgress(ctx, 3)

	child.mu.Lock()
	current := child.current
	child.mu.Unlock()

	if current != 5 {
		t.Errorf("node current after step: got %d, want 5", current)
	}
}

// TestSubTaskWithSharedContextNested verifies that nested SubTask calls within
// a shared context create a 3-level hierarchy correctly.
func TestSubTaskWithSharedContextNested(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("nested root", 1, true, context.Background())
	ctx := withDelivery(context.Background(), node.delivery)
	ctx = withTaskNode(ctx, node)

	err := SubTask(ctx, "level-1", func(ctx1 context.Context) error {
		return SubTask(ctx1, "level-2", func(ctx2 context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Errorf("nested SubTask returned error: %v", err)
	}

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 top-level child, got %d", len(state.Children))
	}
	if state.Children[0].Title != "level-1" {
		t.Errorf("level-1 title: got %q, want %q", state.Children[0].Title, "level-1")
	}
	if len(state.Children[0].Children) != 1 {
		t.Fatalf("expected 1 level-2 child, got %d", len(state.Children[0].Children))
	}
	if state.Children[0].Children[0].Title != "level-2" {
		t.Errorf("level-2 title: got %q, want %q", state.Children[0].Children[0].Title, "level-2")
	}
	if state.Children[0].Children[0].Status != TaskNodeStatusCompleted {
		t.Errorf("level-2 status: got %v, want Completed", state.Children[0].Children[0].Status)
	}
}

// TestSubTaskWithSharedContextError verifies that an error returned from the
// SubTask fn marks the child node as failed and propagates the error.
func TestSubTaskWithSharedContextError(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("error root", 1, true, context.Background())
	ctx := withDelivery(context.Background(), node.delivery)
	ctx = withTaskNode(ctx, node)

	sentinel := errors.New("task failed")
	err := SubTask(ctx, "failing-sub", func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(state.Children))
	}
	if state.Children[0].Status != TaskNodeStatusFailed {
		t.Errorf("child status: got %v, want Failed", state.Children[0].Status)
	}
	if state.Children[0].Error != "task failed" {
		t.Errorf("child error: got %q, want %q", state.Children[0].Error, "task failed")
	}
}

// TestSharedSubTaskStandalone verifies SharedSubTask behaviour when no
// aprot.Connection is available. It falls back to the SubTask path.
func TestSharedSubTaskStandalone(t *testing.T) {
	t.Run("falls back when no connection", func(t *testing.T) {
		_, tm := setupTestServer(t)

		ctx := withTaskManager(context.Background(), tm)

		var fnCalled bool
		err := SharedSubTask(ctx, "standalone", func(ctx context.Context) error {
			fnCalled = true
			return nil
		})
		if err != nil {
			t.Errorf("SharedSubTask returned error: %v", err)
		}
		if !fnCalled {
			t.Error("SharedSubTask fn was not called")
		}
	})

	t.Run("falls back and propagates error", func(t *testing.T) {
		_, tm := setupTestServer(t)

		ctx := withTaskManager(context.Background(), tm)

		sentinel := errors.New("standalone error")
		err := SharedSubTask(ctx, "standalone-err", func(ctx context.Context) error {
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("expected sentinel error, got: %v", err)
		}
	})
}

// TestSharedSubTaskWithoutConnection verifies that SharedSubTask falls back to
// SubTask when no connection is present, including routing through the
// request-scoped task tree when one is available.
func TestSharedSubTaskWithoutConnection(t *testing.T) {
	// Supply a request delivery so the SubTask fallback has something to work with.
	sender := &mockSender{}
	d := newRequestDelivery(sender)
	ctx := withDelivery(context.Background(), d)

	var innerCtx context.Context
	err := SharedSubTask(ctx, "no-conn", func(childCtx context.Context) error {
		innerCtx = childCtx
		return nil
	})
	if err != nil {
		t.Errorf("SharedSubTask returned error: %v", err)
	}

	// The fallback SubTask path should have created a task node in the tree.
	node := taskNodeFromContext(innerCtx)
	if node == nil {
		t.Error("expected task node in context after SubTask fallback")
	}
}

// TestSharedSubTaskRoutesThroughSharedContext verifies that SharedSubTask
// routes through the existing shared context (via SubTask internally) when a
// shared node is already present on the context.
func TestSharedSubTaskRoutesThroughSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("existing shared", 1, true, context.Background())
	ctx := withDelivery(context.Background(), node.delivery)
	ctx = withTaskNode(ctx, node)

	err := SharedSubTask(ctx, "via-shared", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Errorf("SharedSubTask returned error: %v", err)
	}

	// The sub-task should have been created under the existing node.
	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 child via shared context, got %d", len(state.Children))
	}
	if state.Children[0].Title != "via-shared" {
		t.Errorf("child title: got %q, want %q", state.Children[0].Title, "via-shared")
	}
}

// TestCancelSharedTaskViaAPI verifies that CancelSharedTask returns an error
// for an unknown task ID, and for a known one it sets failed status and
// cancels the task context.
func TestCancelSharedTaskViaAPI(t *testing.T) {
	_, tm := setupTestServer(t)

	ctx := withTaskManager(context.Background(), tm)

	// Unknown task should return an error.
	err := CancelSharedTask(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown task ID, got nil")
	}

	// Known running task should be canceled successfully.
	node := tm.create("cancel-api", 1, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	err = CancelSharedTask(ctx, node.id)
	if err != nil {
		t.Errorf("CancelSharedTask returned unexpected error: %v", err)
	}

	state := node.sharedSnapshot()
	if state.Status != TaskNodeStatusFailed {
		t.Errorf("status after CancelSharedTask: got %v, want Failed", state.Status)
	}

	// CancelSharedTask must cancel the task context.
	select {
	case <-node.ctx.Done():
		// expected
	default:
		t.Error("task context was not canceled after CancelSharedTask")
	}
}

// TestCancelSharedTaskNoManager verifies that CancelSharedTask returns an
// error when no taskManager is present on the context.
func TestCancelSharedTaskNoManager(t *testing.T) {
	ctx := context.Background()

	err := CancelSharedTask(ctx, "any-id")
	if err == nil {
		t.Error("expected error when taskManager is not in context, got nil")
	}
}

// TestSharedTaskAllocID verifies that allocID produces unique string IDs.
func TestSharedTaskAllocID(t *testing.T) {
	_, tm := setupTestServer(t)

	id1 := tm.allocID()
	id2 := tm.allocID()
	id3 := tm.allocID()

	if id1 == id2 || id2 == id3 || id1 == id3 {
		t.Errorf("allocID produced duplicate IDs: %q %q %q", id1, id2, id3)
	}
}

// TestSharedTaskContextCanceledOnClose verifies that the task's context is
// canceled when Close() is called.
func TestSharedTaskContextCanceledOnClose(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("ctx cancel", 1, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	task := &Task[struct{}]{node: node}
	taskCtx := task.Context()

	task.Close()

	select {
	case <-taskCtx.Done():
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Error("task context was not canceled after Close()")
	}
}

// TestSharedTaskWithContext verifies that WithContext returns a context that
// carries the task's delivery and node.
func TestSharedTaskWithContext(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("with-ctx", 1, true, context.Background())
	task := &Task[struct{}]{node: node}

	derived := task.WithContext(context.Background())
	derivedNode := taskNodeFromContext(derived)
	if derivedNode == nil {
		t.Fatal("WithContext did not embed task node")
	}
	if derivedNode != node {
		t.Error("WithContext embedded wrong node")
	}
	derivedDelivery := deliveryFromContext(derived)
	if derivedDelivery == nil {
		t.Fatal("WithContext did not embed delivery")
	}
	if !derivedDelivery.isShared() {
		t.Error("WithContext should embed shared delivery")
	}
}

// TestSharedTaskID verifies that the ID() method returns the node's task ID.
func TestSharedTaskID(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("id task", 1, true, context.Background())
	task := &Task[struct{}]{node: node}

	if task.ID() != node.id {
		t.Errorf("ID(): got %q, want %q", task.ID(), node.id)
	}
}

// TestSharedTaskSubTaskProgress verifies that TaskSub.Progress updates
// the sub-task node's counters.
func TestSharedTaskSubTaskProgress(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("sub-progress root", 1, true, context.Background())
	task := &Task[struct{}]{node: node}
	sub := task.SubTask("sub progress")

	sub.Progress(7, 14)

	state := node.sharedSnapshot()
	if len(state.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(state.Children))
	}
	if state.Children[0].Current != 7 {
		t.Errorf("child current: got %d, want 7", state.Children[0].Current)
	}
	if state.Children[0].Total != 14 {
		t.Errorf("child total: got %d, want 14", state.Children[0].Total)
	}
}

// TestSharedTaskFailIdempotent verifies that calling failTop() on an already
// failed task is a no-op — it does not overwrite the original error.
func TestSharedTaskFailIdempotent(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("fail-idempotent", 1, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	node.failTop("first failure")
	node.failTop("second failure") // should be ignored

	state := node.sharedSnapshot()
	if state.Error != "first failure" {
		t.Errorf("error after double fail: got %q, want %q", state.Error, "first failure")
	}
}

// TestSharedSubTaskCancelPropagation verifies that canceling a task created by
// SharedSubTask also cancels the context passed to fn.
func TestSharedSubTaskCancelPropagation(t *testing.T) {
	_, tm := setupTestServer(t)

	ctx := context.Background()
	ctx = aprot.WithTestConnection(ctx, 1)
	ctx = withTaskManager(ctx, tm)

	fnStarted := make(chan struct{})
	fnDone := make(chan error, 1)

	go func() {
		fnDone <- SharedSubTask(ctx, "cancel-propagation", func(fnCtx context.Context) error {
			close(fnStarted)
			<-fnCtx.Done()
			return fnCtx.Err()
		})
	}()

	// Wait for fn to start running.
	select {
	case <-fnStarted:
	case <-time.After(time.Second):
		t.Fatal("fn did not start within timeout")
	}

	// Find the task by title and cancel it.
	var taskID string
	for _, s := range tm.snapshotAll() {
		if s.Title == "cancel-propagation" {
			taskID = s.ID
			break
		}
	}
	if taskID == "" {
		t.Fatal("shared task not found in snapshot")
	}

	tm.cancelTask(taskID)

	select {
	case <-fnDone:
		// expected: fn's context was canceled
	case <-time.After(time.Second):
		t.Fatal("fn's context was not canceled after cancelTask")
	}
}

// TestStartSharedTaskCancelPropagation verifies that canceling a task created
// by StartTask with Shared() also cancels the context returned to the caller.
func TestStartSharedTaskCancelPropagation(t *testing.T) {
	_, tm := setupTestServer(t)

	ctx := context.Background()
	ctx = aprot.WithTestConnection(ctx, 1)
	ctx = withTaskManager(ctx, tm)

	taskCtx, task := StartTask[struct{}](ctx, "start-cancel", Shared())
	if task == nil {
		t.Fatal("StartTask returned nil task")
	}

	// taskCtx should be canceled when we cancel the task.
	done := make(chan struct{})
	go func() {
		<-taskCtx.Done()
		close(done)
	}()

	tm.cancelTask(task.ID())

	select {
	case <-done:
		// expected: taskCtx was canceled
	case <-time.After(time.Second):
		t.Fatal("context returned by StartTask was not canceled after cancelTask")
	}
}

// TestStartSharedTaskMiddlewareFinalize verifies that a task created via
// StartTask with Shared() is completed (not canceled) by finalizeTaskSlot when
// the handler returns nil.
func TestStartSharedTaskMiddlewareFinalize(t *testing.T) {
	_, tm := setupTestServer(t)

	// Simulate connection.go: request context with defer cancel.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	// Build the context the same way connection.go + middleware do.
	ctx := aprot.WithTestConnection(reqCtx, 1)
	ctx = aprot.WithTestRequestSender(ctx, &mockSender{})

	mw := taskMiddleware(tm)
	handler := mw(func(ctx context.Context, req *aprot.Request) (any, error) {
		taskCtx, task := StartTask[struct{}](ctx, "mw-finalize", Shared())
		if task == nil {
			t.Fatal("StartTask returned nil")
		}
		// Use the task context (as a real handler would).
		select {
		case <-taskCtx.Done():
			return nil, taskCtx.Err()
		default:
		}
		return "ok", nil
	})

	result, err := handler(ctx, &aprot.Request{ID: "r1", Method: "test"})

	// Simulate connection.go defer cancel() firing after middleware returns.
	reqCancel()

	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("handler returned %v, want \"ok\"", result)
	}

	// The task should be Completed, not Failed/"canceled".
	states := tm.snapshotAll()
	if len(states) == 0 {
		// Task may have been removed already (200ms delay). That's fine —
		// it means completeTop() ran (Completed path), not failTop("canceled").
		return
	}
	for _, s := range states {
		if s.Title == "mw-finalize" {
			if s.Status == TaskNodeStatusFailed {
				t.Errorf("task was failed with %q; want Completed", s.Error)
			}
		}
	}
}

// TestSharedSubTaskMiddlewareFinalize verifies that SharedSubTask completes
// the task normally when fn returns nil, even after the request context is
// canceled by defer cancel().
func TestSharedSubTaskMiddlewareFinalize(t *testing.T) {
	_, tm := setupTestServer(t)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	ctx := aprot.WithTestConnection(reqCtx, 1)
	ctx = aprot.WithTestRequestSender(ctx, &mockSender{})

	mw := taskMiddleware(tm)
	handler := mw(func(ctx context.Context, req *aprot.Request) (any, error) {
		err := SharedSubTask(ctx, "sub-mw", func(fnCtx context.Context) error {
			return nil
		})
		return nil, err
	})

	_, err := handler(ctx, &aprot.Request{ID: "r2", Method: "test"})

	reqCancel()

	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	// SharedSubTask creates a non-top-level task; verify it was completed.
	for _, s := range tm.snapshotAll() {
		if s.Title == "sub-mw" && s.Status == TaskNodeStatusFailed {
			t.Errorf("SharedSubTask was failed with %q; want Completed", s.Error)
		}
	}
}

// TestSharedTaskCloseIdempotent verifies that calling completeTop() on an
// already-completed task is a no-op.
func TestSharedTaskCloseIdempotent(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("close-idempotent", 1, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	node.completeTop()
	node.completeTop() // should be ignored

	state := node.sharedSnapshot()
	if state.Status != TaskNodeStatusCompleted {
		t.Errorf("status after double close: got %v, want Completed", state.Status)
	}
}
