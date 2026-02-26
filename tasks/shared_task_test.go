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

	core := tm.create("basic task", 1, true, context.Background())
	id := core.id

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

	core.closeTask()

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

	core := tm.create("progress task", 1, true, context.Background())

	core.progress(3, 10)

	state := core.snapshot()
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

	core := tm.create("parent task", 1, true, context.Background())
	child := core.subTask("child task")

	if child == nil {
		t.Fatal("subTask returned nil")
	}
	if child.title != "child task" {
		t.Errorf("child title: got %q, want %q", child.title, "child task")
	}

	state := core.snapshot()
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

	state = core.snapshot()
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

	core := tm.create("cancel task", 1, true, context.Background())
	id := core.id
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()

	ok := tm.cancelTask(id)
	if !ok {
		t.Fatal("cancelTask returned false for existing task")
	}

	state := core.snapshot()
	if state.Status != TaskNodeStatusFailed {
		t.Errorf("status after cancel: got %v, want Failed", state.Status)
	}
	if state.Error != "canceled" {
		t.Errorf("error after cancel: got %q, want %q", state.Error, "canceled")
	}

	// Task context should be canceled.
	select {
	case <-core.ctx.Done():
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

// TestSharedTaskFail verifies that fail() sets the correct status and error
// message in the snapshot and cancels the task context.
func TestSharedTaskFail(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("fail task", 1, true, context.Background())
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()

	core.fail("something went wrong")

	state := core.snapshot()
	if state.Status != TaskNodeStatusFailed {
		t.Errorf("status: got %v, want Failed", state.Status)
	}
	if state.Error != "something went wrong" {
		t.Errorf("error: got %q, want %q", state.Error, "something went wrong")
	}

	// fail() must cancel the task context.
	select {
	case <-core.ctx.Done():
		// expected
	default:
		t.Error("task context was not canceled after fail()")
	}
}

// TestSharedTaskErr verifies that Err(nil) completes the task and cancels its
// context, and Err(non-nil) fails the task and cancels its context.
func TestSharedTaskErr(t *testing.T) {
	t.Run("nil error completes", func(t *testing.T) {
		_, tm := setupTestServer(t)

		core := tm.create("err nil task", 1, true, context.Background())
		core.mu.Lock()
		core.status = TaskNodeStatusRunning
		core.mu.Unlock()

		task := &SharedTask[string]{core: core}
		task.Err(nil)

		state := core.snapshot()
		if state.Status != TaskNodeStatusCompleted {
			t.Errorf("status: got %v, want Completed", state.Status)
		}

		select {
		case <-core.ctx.Done():
			// expected — Err(nil) calls Close which cancels the context
		default:
			t.Error("task context was not canceled after Err(nil)")
		}
	})

	t.Run("non-nil error fails", func(t *testing.T) {
		_, tm := setupTestServer(t)

		core := tm.create("err non-nil task", 1, true, context.Background())
		core.mu.Lock()
		core.status = TaskNodeStatusRunning
		core.mu.Unlock()

		task := &SharedTask[string]{core: core}
		task.Err(errors.New("test error"))

		state := core.snapshot()
		if state.Status != TaskNodeStatusFailed {
			t.Errorf("status: got %v, want Failed", state.Status)
		}
		if state.Error != "test error" {
			t.Errorf("error: got %q, want %q", state.Error, "test error")
		}

		select {
		case <-core.ctx.Done():
			// expected — Err(err) calls Fail which cancels the context
		default:
			t.Error("task context was not canceled after Err(non-nil)")
		}
	})
}

// TestSharedTaskSubErr verifies that SharedTaskSub.Err(nil) completes the
// sub-task and Err(non-nil) fails it.
func TestSharedTaskSubErr(t *testing.T) {
	t.Run("nil error completes", func(t *testing.T) {
		_, tm := setupTestServer(t)

		core := tm.create("sub err nil", 1, true, context.Background())
		task := &SharedTask[string]{core: core}
		sub := task.SubTask("sub nil")

		sub.Err(nil)

		state := core.snapshot()
		if len(state.Children) != 1 {
			t.Fatalf("expected 1 child, got %d", len(state.Children))
		}
		if state.Children[0].Status != TaskNodeStatusCompleted {
			t.Errorf("sub status: got %v, want Completed", state.Children[0].Status)
		}
	})

	t.Run("non-nil error fails", func(t *testing.T) {
		_, tm := setupTestServer(t)

		core := tm.create("sub err non-nil", 1, true, context.Background())
		task := &SharedTask[string]{core: core}
		sub := task.SubTask("sub fail")

		sub.Err(errors.New("sub error"))

		state := core.snapshot()
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

	core := tm.create("meta task", 1, true, context.Background())
	task := &SharedTask[meta]{core: core}
	task.SetMeta(meta{UserName: "alice"})

	state := core.snapshot()
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

	core := tm.create("parent meta", 1, true, context.Background())
	task := &SharedTask[meta]{core: core}
	sub := task.SubTask("sub meta")
	sub.SetMeta(meta{Step: 7})

	state := core.snapshot()
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

	core := tm.create("top", 1, true, context.Background())
	task := &SharedTask[struct{}]{core: core}

	level1 := task.SubTask("level-1")
	level2 := level1.SubTask("level-2")
	if level2 == nil {
		t.Fatal("SubTask on SharedTaskSub returned nil")
	}

	state := core.snapshot()
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

	core := tm.create("owner task", ownerID, true, context.Background())

	ownerState := core.snapshotForConn(ownerID)
	if !ownerState.IsOwner {
		t.Error("IsOwner should be true for the owning connection")
	}

	otherState := core.snapshotForConn(otherID)
	if otherState.IsOwner {
		t.Error("IsOwner should be false for a different connection")
	}
}

// TestSharedTaskSnapshotForConn_NonTopLevel verifies that IsOwner is always
// false for non-top-level tasks, even when queried by the owner connection.
func TestSharedTaskSnapshotForConn_NonTopLevel(t *testing.T) {
	_, tm := setupTestServer(t)

	const ownerID uint64 = 42

	core := tm.create("sub task", ownerID, false, context.Background())

	ownerState := core.snapshotForConn(ownerID)
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

	core1 := tm.create("task-conn1", conn1, true, context.Background())
	core2 := tm.create("task-conn2", conn2, true, context.Background())

	states1 := tm.snapshotAllForConn(conn1)
	s1, ok := findTaskByID(states1, core1.id)
	if !ok {
		t.Fatal("task for conn1 not found in conn1 snapshot")
	}
	if !s1.IsOwner {
		t.Error("conn1's task should have IsOwner=true for conn1")
	}
	s2, ok := findTaskByID(states1, core2.id)
	if !ok {
		t.Fatal("task for conn2 not found in conn1 snapshot")
	}
	if s2.IsOwner {
		t.Error("conn2's task should have IsOwner=false for conn1")
	}

	states2 := tm.snapshotAllForConn(conn2)
	s1InConn2, ok := findTaskByID(states2, core1.id)
	if !ok {
		t.Fatal("task for conn1 not found in conn2 snapshot")
	}
	if s1InConn2.IsOwner {
		t.Error("conn1's task should have IsOwner=false for conn2")
	}
	s2InConn2, ok := findTaskByID(states2, core2.id)
	if !ok {
		t.Fatal("task for conn2 not found in conn2 snapshot")
	}
	if !s2InConn2.IsOwner {
		t.Error("conn2's task should have IsOwner=true for conn2")
	}
}

// TestSubTaskWithSharedContext verifies that SubTask routes through the shared
// task system when a sharedContext is present on the context.
func TestSubTaskWithSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("shared root", 1, true, context.Background())
	sc := &sharedContext{core: core}
	ctx := withSharedContext(context.Background(), sc)

	var fnCalled bool
	err := SubTask(ctx, "shared-sub", func(childCtx context.Context) error {
		fnCalled = true
		// The child context must carry a sharedContext with the same core.
		childSC := sharedCtxFromContext(childCtx)
		if childSC == nil {
			t.Error("childCtx has no sharedContext")
		} else if childSC.core != core {
			t.Error("childCtx sharedContext core mismatch")
		}
		return nil
	})
	if err != nil {
		t.Errorf("SubTask returned error: %v", err)
	}
	if !fnCalled {
		t.Error("SubTask fn was not called")
	}

	state := core.snapshot()
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
// sub-task node when a sharedContext with a node is present.
func TestTaskProgressSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("progress root", 1, true, context.Background())
	node := core.subTask("progress node")

	sc := &sharedContext{core: core, node: node}
	ctx := withSharedContext(context.Background(), sc)

	TaskProgress(ctx, 4, 12)

	node.mu.Lock()
	current := node.current
	total := node.total
	node.mu.Unlock()

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

	core := tm.create("step root", 1, true, context.Background())
	node := core.subTask("step node")
	node.mu.Lock()
	node.current = 2
	node.total = 10
	node.mu.Unlock()

	sc := &sharedContext{core: core, node: node}
	ctx := withSharedContext(context.Background(), sc)

	StepTaskProgress(ctx, 3)

	node.mu.Lock()
	current := node.current
	node.mu.Unlock()

	if current != 5 {
		t.Errorf("node current after step: got %d, want 5", current)
	}
}

// TestSubTaskWithSharedContextNested verifies that nested SubTask calls within
// a shared context create a 3-level hierarchy correctly.
func TestSubTaskWithSharedContextNested(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("nested root", 1, true, context.Background())
	sc := &sharedContext{core: core}
	ctx := withSharedContext(context.Background(), sc)

	err := SubTask(ctx, "level-1", func(ctx1 context.Context) error {
		return SubTask(ctx1, "level-2", func(ctx2 context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Errorf("nested SubTask returned error: %v", err)
	}

	state := core.snapshot()
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

	core := tm.create("error root", 1, true, context.Background())
	sc := &sharedContext{core: core}
	ctx := withSharedContext(context.Background(), sc)

	sentinel := errors.New("task failed")
	err := SubTask(ctx, "failing-sub", func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}

	state := core.snapshot()
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
	// Supply a task tree so the SubTask fallback has something to work with.
	sender := &mockSender{}
	tree := newTaskTree(sender)
	ctx := withTaskTree(context.Background(), tree)

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
// sharedContext is already present on the context.
func TestSharedSubTaskRoutesThroughSharedContext(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("existing shared", 1, true, context.Background())
	sc := &sharedContext{core: core}
	ctx := withSharedContext(context.Background(), sc)

	err := SharedSubTask(ctx, "via-shared", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Errorf("SharedSubTask returned error: %v", err)
	}

	// The sub-task should have been created under the existing core.
	state := core.snapshot()
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
	core := tm.create("cancel-api", 1, true, context.Background())
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()

	err = CancelSharedTask(ctx, core.id)
	if err != nil {
		t.Errorf("CancelSharedTask returned unexpected error: %v", err)
	}

	state := core.snapshot()
	if state.Status != TaskNodeStatusFailed {
		t.Errorf("status after CancelSharedTask: got %v, want Failed", state.Status)
	}

	// CancelSharedTask must cancel the task context.
	select {
	case <-core.ctx.Done():
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

	core := tm.create("ctx cancel", 1, true, context.Background())
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()

	task := &SharedTask[struct{}]{core: core}
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
// carries the task's sharedContext.
func TestSharedTaskWithContext(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("with-ctx", 1, true, context.Background())
	task := &SharedTask[struct{}]{core: core}

	derived := task.WithContext(context.Background())
	sc := sharedCtxFromContext(derived)
	if sc == nil {
		t.Fatal("WithContext did not embed sharedContext")
	}
	if sc.core != core {
		t.Error("WithContext embedded wrong core")
	}
}

// TestSharedTaskID verifies that the ID() method returns the core's task ID.
func TestSharedTaskID(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("id task", 1, true, context.Background())
	task := &SharedTask[struct{}]{core: core}

	if task.ID() != core.id {
		t.Errorf("ID(): got %q, want %q", task.ID(), core.id)
	}
}

// TestSharedTaskSubTaskProgress verifies that SharedTaskSub.Progress updates
// the sub-task node's counters.
func TestSharedTaskSubTaskProgress(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("sub-progress root", 1, true, context.Background())
	task := &SharedTask[struct{}]{core: core}
	sub := task.SubTask("sub progress")

	sub.Progress(7, 14)

	state := core.snapshot()
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

// TestSharedTaskFailIdempotent verifies that calling fail() on an already
// failed task is a no-op — it does not overwrite the original error.
func TestSharedTaskFailIdempotent(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("fail-idempotent", 1, true, context.Background())
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()

	core.fail("first failure")
	core.fail("second failure") // should be ignored

	state := core.snapshot()
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
// by StartSharedTask also cancels the context returned to the caller.
func TestStartSharedTaskCancelPropagation(t *testing.T) {
	_, tm := setupTestServer(t)

	ctx := context.Background()
	ctx = aprot.WithTestConnection(ctx, 1)
	ctx = withTaskManager(ctx, tm)

	taskCtx, task := StartSharedTask[struct{}](ctx, "start-cancel")
	if task == nil {
		t.Fatal("StartSharedTask returned nil task")
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
		t.Fatal("context returned by StartSharedTask was not canceled after cancelTask")
	}
}

// TestSharedTaskCloseIdempotent verifies that calling closeTask() on an
// already-completed task is a no-op.
func TestSharedTaskCloseIdempotent(t *testing.T) {
	_, tm := setupTestServer(t)

	core := tm.create("close-idempotent", 1, true, context.Background())
	core.mu.Lock()
	core.status = TaskNodeStatusRunning
	core.mu.Unlock()

	core.closeTask()
	core.closeTask() // should be ignored

	state := core.snapshot()
	if state.Status != TaskNodeStatusCompleted {
		t.Errorf("status after double close: got %v, want Completed", state.Status)
	}
}
