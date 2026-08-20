package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/marrasen/aprot"
)

// runThroughMiddleware executes fn through the task middleware with the given
// context, the way a dispatch path would.
func runThroughMiddleware(t *testing.T, tm *taskManager, ctx context.Context, fn aprot.Handler) (any, error) {
	t.Helper()
	return taskMiddleware(tm)(fn)(ctx, &aprot.Request{ID: "r1", Method: "test"})
}

// The task manager is installed on every execution, connection or not, so a
// handler reached over a request-scoped transport can start a shared task.
// Before #335 the middleware skipped all setup without a connection and
// StartTask degraded to a detached no-op.
func TestSharedTaskRegistersWithoutConnection(t *testing.T) {
	_, tm := setupTestServer(t)

	var id string
	_, err := runThroughMiddleware(t, tm, context.Background(),
		func(ctx context.Context, _ *aprot.Request) (any, error) {
			if taskManagerFromContext(ctx) == nil {
				t.Error("task manager missing from a connectionless execution")
			}
			_, task := StartTask[struct{}](context.WithoutCancel(ctx), "bg-job", Shared())
			id = task.ID()
			return nil, nil
		})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if _, ok := findTaskByID(tm.snapshotAll(), id); !ok {
		t.Fatal("shared task started without a connection is missing from the manager")
	}
}

// The owner address comes from the execution context on a connectionless
// path, so socket clients belonging to that user see the task as theirs.
func TestSharedTaskOwnerAddressFromContext(t *testing.T) {
	_, tm := setupTestServer(t)

	ctx := aprot.WithUserID(context.Background(), "u1")
	var id string
	_, err := runThroughMiddleware(t, tm, ctx,
		func(ctx context.Context, _ *aprot.Request) (any, error) {
			_, task := StartTask[struct{}](context.WithoutCancel(ctx), "owned", Shared())
			id = task.ID()
			return nil, nil
		})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	tm.mu.Lock()
	node := tm.tasks[id]
	tm.mu.Unlock()
	if node == nil {
		t.Fatal("task not registered")
	}
	if node.ownerUserID != "u1" {
		t.Errorf("ownerUserID = %q, want u1", node.ownerUserID)
	}
	if node.ownerConnID != 0 {
		t.Errorf("ownerConnID = %d, want 0 on a connectionless path", node.ownerConnID)
	}
	// The same user on a socket sees it as theirs.
	if s := node.sharedSnapshotForConn(42, "u1"); !s.IsOwner {
		t.Error("IsOwner should be true for the same user on a socket")
	}
	if s := node.sharedSnapshotForConn(42, "u2"); s.IsOwner {
		t.Error("IsOwner should be false for a different user")
	}
}

// Without a connection there is no delivery, and nothing fakes one: a
// request-scoped task degrades to the detached no-op, while the shared task
// above still registers. Connection presence decides delivery only.
func TestNoDeliveryWithoutConnection(t *testing.T) {
	_, tm := setupTestServer(t)

	_, err := runThroughMiddleware(t, tm, context.Background(),
		func(ctx context.Context, _ *aprot.Request) (any, error) {
			if d := deliveryFromContext(ctx); d != nil {
				t.Errorf("delivery installed on a connectionless execution: %T", d)
			}
			if aprot.Connection(ctx) != nil {
				t.Error("a connection was faked on a connectionless execution")
			}
			// A request-scoped task has nowhere to go; it must still be usable.
			_, task := StartTask[struct{}](ctx, "req-scoped")
			task.Progress(1, 2)
			task.Output("still safe")
			task.Close()
			return nil, nil
		})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
}

// The default cancel policy matches ownership by address, so cancel rights
// survive a reconnect onto a new connection. This was broken independently of
// MCP: the snapshot reported IsOwner true and the cancel was refused.
func TestDefaultCancelPolicySurvivesReconnect(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("owned by u1", 10, "u1", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	// Same user, new connection ID — a reconnect.
	ctx := withTaskManager(aprot.WithTestConnectionUser(context.Background(), 11, "u1"), tm)
	if s := node.sharedSnapshotForConn(11, "u1"); !s.IsOwner {
		t.Fatal("precondition: reconnected owner should see IsOwner true")
	}
	if err := CancelSharedTask(ctx, node.id); err != nil {
		t.Fatalf("reconnected owner was refused: %v", err)
	}
	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusFailed {
		t.Errorf("status = %v, want Failed", s.Status)
	}
}

// An authenticated request-scoped caller can cancel its own task: ownership is
// matched on the address, which REST and MCP carry.
func TestDefaultCancelPolicyRequestScopedOwner(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("started over mcp", 0, "u1", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	ctx := withTaskManager(aprot.WithUserID(context.Background(), "u1"), tm)
	if err := CancelSharedTask(ctx, node.id); err != nil {
		t.Fatalf("request-scoped owner was refused: %v", err)
	}
	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusFailed {
		t.Errorf("status = %v, want Failed", s.Status)
	}
}

// A different authenticated user is still refused, and with the same error a
// missing task produces, so ownership can't be probed.
func TestDefaultCancelPolicyRefusesOtherUser(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("owned by u1", 10, "u1", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	ctx := withTaskManager(aprot.WithUserID(context.Background(), "u2"), tm)
	refused := CancelSharedTask(ctx, node.id)
	missing := CancelSharedTask(ctx, "no-such-task")

	// Both must be indistinguishable in code and message shape — only the
	// echoed ID, which the caller supplied, may differ. Otherwise a caller
	// can probe for the existence of other users' tasks.
	for _, tc := range []struct {
		name string
		err  error
		id   string
	}{
		{"owned by someone else", refused, node.id},
		{"missing", missing, "no-such-task"},
	} {
		var perr *aprot.ProtocolError
		if !errors.As(tc.err, &perr) {
			t.Fatalf("%s: error %v is not a ProtocolError", tc.name, tc.err)
		}
		if perr.Code != aprot.CodeForbidden {
			t.Errorf("%s: code = %d, want CodeForbidden", tc.name, perr.Code)
		}
		if want := "task not found: " + tc.id; perr.Message != want {
			t.Errorf("%s: message = %q, want %q", tc.name, perr.Message, want)
		}
	}

	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusRunning {
		t.Errorf("status = %v, want Running (untouched)", s.Status)
	}
}

// An anonymous owner's task stays gated on the creating connection: there is
// no address to match, so nothing else can stand in for one.
func TestDefaultCancelPolicyAnonymousOwnerStaysConnGated(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("anon", 7, "", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	// The creating connection may cancel.
	ownerCtx := withTaskManager(aprot.WithTestConnection(context.Background(), 7), tm)
	if s := node.sharedSnapshotForConn(7, ""); !s.IsOwner {
		t.Fatal("precondition: creating connection should see IsOwner true")
	}

	// Another connection may not.
	otherCtx := withTaskManager(aprot.WithTestConnection(context.Background(), 8), tm)
	if err := CancelSharedTask(otherCtx, node.id); err == nil {
		t.Error("a different connection was allowed to cancel an anonymous task")
	}
	// Neither may a connectionless caller with no address.
	anonCtx := withTaskManager(context.Background(), tm)
	if err := CancelSharedTask(anonCtx, node.id); err == nil {
		t.Error("a connectionless anonymous caller was allowed to cancel")
	}
	if err := CancelSharedTask(ownerCtx, node.id); err != nil {
		t.Fatalf("creating connection was refused: %v", err)
	}
}

// A cancel authorizer runs on connectionless paths, replacing the old
// CodeInternalError "tasks not enabled" that fired before any authorizer
// could be consulted.
func TestCancelAuthorizerRunsWithoutConnection(t *testing.T) {
	ran := false
	authorizer := func(ctx context.Context, _ TaskCancelInfo) error {
		ran = true
		if aprot.UserID(ctx) == "" {
			return aprot.ErrForbidden("not authenticated")
		}
		return nil
	}
	tm := managerWithOptions(t, &enableOptions{cancelAuthorizer: authorizer})

	node := tm.create("t", 0, "u1", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	ctx := withTaskManager(aprot.WithUserID(context.Background(), "u1"), tm)
	if err := CancelSharedTask(ctx, node.id); err != nil {
		t.Fatalf("authorized connectionless cancel refused: %v", err)
	}
	if !ran {
		t.Fatal("cancel authorizer never ran on a connectionless path")
	}
}

// ListTasks answers on connectionless paths, matching ownership on the
// address alone — how a caller that started a task over MCP polls it.
func TestListTasksWithoutConnection(t *testing.T) {
	_, tm := setupTestServer(t)
	h := &tasksHandler{tm: tm}

	node := tm.create("started over mcp", 0, "u1", true, context.Background())

	states, err := h.ListTasks(aprot.WithUserID(context.Background(), "u1"))
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	s, ok := findTaskByID(states, node.id)
	if !ok {
		t.Fatal("task missing from a connectionless ListTasks snapshot")
	}
	if !s.IsOwner {
		t.Error("IsOwner should be true for the owning address")
	}

	otherStates, _ := h.ListTasks(aprot.WithUserID(context.Background(), "u2"))
	so, ok := findTaskByID(otherStates, node.id)
	if !ok {
		t.Fatal("task missing from another caller's snapshot")
	}
	if so.IsOwner {
		t.Error("IsOwner should be false for a different address")
	}
}

// The request context dies when a request-scoped response is written, so a
// shared task meant to outlive the call must be started on a detached
// context — the same fire-and-forget contract as on a socket, but it bites
// harder here because the request ends at once.
func TestSharedTaskLifetimeOnRequestScopedPath(t *testing.T) {
	_, tm := setupTestServer(t)

	reqCtx, reqCancel := context.WithCancel(context.Background())

	var detached, attached context.Context
	_, err := runThroughMiddleware(t, tm, reqCtx,
		func(ctx context.Context, _ *aprot.Request) (any, error) {
			detached, _ = StartTask[struct{}](context.WithoutCancel(ctx), "outlives", Shared())
			attached, _ = StartTask[struct{}](ctx, "dies-with-request", Shared())
			return nil, nil
		})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// The transport finishes the request.
	reqCancel()

	if err := detached.Err(); err != nil {
		t.Errorf("detached shared task was canceled with the request: %v", err)
	}
	if attached.Err() == nil {
		t.Error("a shared task on the request context should die with the request")
	}
}
