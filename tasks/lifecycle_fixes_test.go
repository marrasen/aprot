package tasks

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marrasen/aprot"
)

// captureProgress installs a broadcast sink on the manager that records every
// TaskUpdateEvent and returns a function to read the captured events.
func captureProgress(tm *taskManager) func() []TaskUpdateEvent {
	var mu sync.Mutex
	var got []TaskUpdateEvent
	tm.broadcast = func(data any) {
		if ev, ok := data.(TaskUpdateEvent); ok {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}
	}
	return func() []TaskUpdateEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]TaskUpdateEvent, len(got))
		copy(out, got)
		return out
	}
}

// A burst of progress updates within the throttle window must still deliver the
// final value: the trailing update is flushed after the window instead of being
// silently dropped (which would leave clients stuck at a stale value). (#207 P2)
func TestProgressThrottleFlushesTrailingUpdate(t *testing.T) {
	_, tm := setupTestServer(t)
	events := captureProgress(tm)

	const id = "task-x"
	c1, t1 := 1, 100
	tm.sendUpdate(id, nil, &c1, &t1) // sends immediately
	c2, t2 := 50, 100
	tm.sendUpdate(id, nil, &c2, &t2) // within window — throttled
	c3, t3 := 97, 100
	tm.sendUpdate(id, nil, &c3, &t3) // within window — throttled, latest value

	// Wait past the throttle window for the trailing flush.
	time.Sleep(progressThrottleInterval + 50*time.Millisecond)

	evs := events()
	if len(evs) == 0 {
		t.Fatal("no progress events broadcast")
	}
	last := evs[len(evs)-1]
	if last.Current == nil || *last.Current != 97 {
		t.Fatalf("trailing progress update was dropped: last broadcast current=%v, want 97", last.Current)
	}
}

// The throttle bookkeeping for a task must be removed when the task is removed,
// or the map grows without bound on a long-running server. (#207 P2)
func TestProgressThrottleCleanedUpOnRemove(t *testing.T) {
	_, tm := setupTestServer(t)

	const id = "task-cleanup"
	c, total := 1, 10
	tm.sendUpdate(id, nil, &c, &total) // creates a throttle entry

	tm.progressMu.Lock()
	_, exists := tm.throttles[id]
	tm.progressMu.Unlock()
	if !exists {
		t.Fatal("expected a throttle entry after a progress update")
	}

	tm.remove(id)

	tm.progressMu.Lock()
	_, stillExists := tm.throttles[id]
	tm.progressMu.Unlock()
	if stillExists {
		t.Error("throttle entry leaked after task removal (unbounded memory growth)")
	}
}

// Concurrent ensureRoot calls must all observe the same implicit root. Without
// synchronization two goroutines can each create a root and one's subtree is
// silently lost. (#207 P2)
func TestEnsureRootConcurrentReturnsSameRoot(t *testing.T) {
	const iterations = 200
	const goroutines = 16

	for iter := 0; iter < iterations; iter++ {
		tc := newTestPushConn()
		d := newRequestDelivery(tc.Conn, "req-1")

		var wg sync.WaitGroup
		start := make(chan struct{})
		roots := make([]*taskNode, goroutines)
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				roots[i] = ensureRoot(d)
			}(i)
		}
		close(start)
		wg.Wait()

		for i := 1; i < goroutines; i++ {
			if roots[i] != roots[0] {
				t.Fatalf("iter %d: ensureRoot returned divergent roots under concurrency: %p vs %p", iter, roots[i], roots[0])
			}
		}
	}
}

// Over the REST path there is no request delivery, connection, or task manager
// on the context, but StartTask must still return a usable (no-op) task so the
// documented SetMeta/Progress/Close calls don't nil-panic. (#207 P2)
func TestStartTaskReturnsNonNilWithoutDelivery(t *testing.T) {
	ctx := context.Background()

	_, task := StartTask[string](ctx, "rest task")
	if task == nil {
		t.Fatal("StartTask returned nil over the REST path; SetMeta/Progress/Close would nil-panic")
	}

	// None of these must panic.
	task.SetMeta("meta")
	task.Progress(1, 2)
	task.Output("hello")
	sub := task.SubTask("child")
	sub.Progress(1, 1)
	sub.Close()
	task.Close()
}

// Same guarantee for a Shared() task created without a connection/manager.
func TestStartSharedTaskReturnsNonNilWithoutManager(t *testing.T) {
	ctx := context.Background()

	_, task := StartTask[string](ctx, "rest shared task", Shared())
	if task == nil {
		t.Fatal("StartTask(Shared()) returned nil without a manager; SetMeta would nil-panic")
	}

	task.SetMeta("meta")
	task.Progress(1, 2)
	task.Close()
}

// SharedSubTask must not nest a duplicate node with the same title under the
// task it creates — otherwise the title renders twice in the tree. (#207 P2)
func TestSharedSubTaskNoDuplicateNode(t *testing.T) {
	_, tm := setupTestServer(t)

	ctx := aprot.WithTestConnection(context.Background(), 1)
	ctx = withTaskManager(ctx, tm)

	var ranWithNode bool
	err := SharedSubTask(ctx, "deploy", func(ctx context.Context) error {
		// fn runs with the shared node already on the context.
		if n := taskNodeFromContext(ctx); n != nil && n.title == "deploy" {
			ranWithNode = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SharedSubTask returned error: %v", err)
	}
	if !ranWithNode {
		t.Error("fn did not run with the shared 'deploy' node on its context")
	}

	// The node is completed but lingers ~200ms before removal; snapshot now.
	state, ok := findTaskByTitle(tm.snapshotAll(), "deploy")
	if !ok {
		t.Fatal("shared task 'deploy' not found in snapshot")
	}
	for _, child := range state.Children {
		if child.Title == "deploy" {
			t.Error("SharedSubTask nested a duplicate 'deploy' node (title renders twice)")
		}
	}
}

// findTaskByTitle returns the first SharedTaskState with the given title.
func findTaskByTitle(states []SharedTaskState, title string) (SharedTaskState, bool) {
	for _, s := range states {
		if s.Title == title {
			return s, true
		}
	}
	return SharedTaskState{}, false
}

// Only the connection that created a shared task may cancel it. A non-owner
// cancel attempt must be rejected and leave the task untouched. (#207 P2)
func TestCancelSharedTaskOwnerOnly(t *testing.T) {
	_, tm := setupTestServer(t)

	const ownerID uint64 = 1
	const otherID uint64 = 2

	node := tm.create("owned task", ownerID, true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	// A non-owner must not be able to cancel.
	otherCtx := withTaskManager(aprot.WithTestConnection(context.Background(), otherID), tm)
	if err := CancelSharedTask(otherCtx, node.id); err == nil {
		t.Error("non-owner was allowed to cancel another client's task")
	}
	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusRunning {
		t.Errorf("task status after non-owner cancel: got %v, want Running (untouched)", s.Status)
	}

	// The owner can cancel.
	ownerCtx := withTaskManager(aprot.WithTestConnection(context.Background(), ownerID), tm)
	if err := CancelSharedTask(ownerCtx, node.id); err != nil {
		t.Errorf("owner failed to cancel own task: %v", err)
	}
	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusFailed {
		t.Errorf("task status after owner cancel: got %v, want Failed", s.Status)
	}
}
