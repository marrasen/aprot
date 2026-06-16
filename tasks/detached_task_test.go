package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/marrasen/aprot"
)

// A shared task started on a detached context (context.WithoutCancel) is the
// documented fire-and-forget pattern: it must NOT be auto-completed by the
// task middleware when the handler returns, and its context must stay alive
// so a background goroutine can keep working.
func TestStartSharedTaskDetachedOutlivesHandler(t *testing.T) {
	_, tm := setupTestServer(t)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	tc := newTestPushConn()
	ctx := tc.WithContext(reqCtx)

	var taskCtx context.Context
	var task *Task[struct{}]

	mw := taskMiddleware(tm)
	handler := mw(func(ctx context.Context, req *aprot.Request) (any, error) {
		taskCtx, task = StartTask[struct{}](context.WithoutCancel(ctx), "bg-job", Shared())
		if task == nil {
			t.Fatal("StartTask returned nil")
		}
		return "ok", nil
	})

	if _, err := handler(ctx, &aprot.Request{ID: "r1", Method: "test"}); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	// Simulate connection.go defer cancel() after the handler returns.
	reqCancel()

	// The background task must still be running with a live context.
	if err := taskCtx.Err(); err != nil {
		t.Fatalf("detached task context was canceled by middleware finalize: %v", err)
	}

	found := false
	for _, s := range tm.snapshotAll() {
		if s.Title == "bg-job" {
			found = true
			if s.Status != TaskNodeStatusRunning {
				t.Errorf("detached task status = %q, want running", s.Status)
			}
		}
	}
	if !found {
		t.Fatal("detached task was removed from the manager after the handler returned")
	}

	// The background goroutine remains responsible for finishing it.
	task.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stillThere := false
		for _, s := range tm.snapshotAll() {
			if s.Title == "bg-job" && s.Status == TaskNodeStatusRunning {
				stillThere = true
			}
		}
		if !stillThere {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task.Close() did not complete the detached task")
}

// A disconnect while the handler is still running must not fail a detached
// shared task either.
func TestStartSharedTaskDetachedSurvivesDisconnect(t *testing.T) {
	_, tm := setupTestServer(t)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	tc := newTestPushConn()
	ctx := tc.WithContext(reqCtx)

	var taskCtx context.Context

	mw := taskMiddleware(tm)
	handler := mw(func(ctx context.Context, req *aprot.Request) (any, error) {
		taskCtx, _ = StartTask[struct{}](context.WithoutCancel(ctx), "bg-survive", Shared())
		// Client disconnects mid-handler.
		reqCancel()
		return nil, nil
	})

	_, _ = handler(ctx, &aprot.Request{ID: "r1", Method: "test"})

	if err := taskCtx.Err(); err != nil {
		t.Fatalf("detached task context canceled on disconnect: %v", err)
	}
	for _, s := range tm.snapshotAll() {
		if s.Title == "bg-survive" && s.Status == TaskNodeStatusFailed {
			t.Fatalf("detached task was failed with %q on disconnect", s.Error)
		}
	}
}
