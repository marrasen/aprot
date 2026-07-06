package tasks

import (
	"context"
	"testing"

	"github.com/marrasen/aprot"
)

// managerWithOptions builds a task manager wired to a real (test) server but
// with custom enable options, so cancel-authorizer behavior can be exercised.
func managerWithOptions(t *testing.T, o *enableOptions) *taskManager {
	t.Helper()
	registry := aprot.NewRegistry()
	handler := &sharedTestHandler{}
	registry.Register(handler)
	registry.RegisterPushEventFor(handler, TaskStateEvent{})
	registry.RegisterPushEventFor(handler, TaskUpdateEvent{})
	server := aprot.NewServer(registry)
	tm := newTaskManager(server, o)
	t.Cleanup(func() {
		tm.stop()
		_ = server.Stop(context.Background())
	})
	return tm
}

// A cancel authorizer allowing any authenticated user lets a non-owner cancel
// another user's task, while an unauthenticated caller is refused.
func TestCancelAuthorizerAllowsAnyAuthenticatedUser(t *testing.T) {
	authorizer := func(ctx context.Context, _ TaskCancelInfo) error {
		if aprot.Connection(ctx).UserID() == "" {
			return aprot.ErrForbidden("not authenticated")
		}
		return nil
	}
	tm := managerWithOptions(t, &enableOptions{cancelAuthorizer: authorizer})

	node := tm.create("owned by u1", 1, "u1", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	// A different user on a different connection may cancel.
	otherCtx := withTaskManager(aprot.WithTestConnectionUser(context.Background(), 2, "u2"), tm)
	if err := CancelSharedTask(otherCtx, node.id); err != nil {
		t.Fatalf("authorized non-owner was refused: %v", err)
	}
	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusFailed {
		t.Errorf("status after authorized cancel: got %v, want Failed", s.Status)
	}

	// An unauthenticated caller (no user ID) is refused by the authorizer.
	node2 := tm.create("also u1", 1, "u1", true, context.Background())
	node2.mu.Lock()
	node2.status = TaskNodeStatusRunning
	node2.mu.Unlock()
	anonCtx := withTaskManager(aprot.WithTestConnection(context.Background(), 3), tm)
	if err := CancelSharedTask(anonCtx, node2.id); err == nil {
		t.Error("unauthenticated caller was allowed to cancel")
	}
	if s := node2.sharedSnapshot(); s.Status != TaskNodeStatusRunning {
		t.Errorf("status after refused cancel: got %v, want Running (untouched)", s.Status)
	}
}

// Ownership is matched by user ID when the owner was authenticated, so IsOwner
// survives a reconnect that lands on a different connection ID.
func TestSharedTaskOwnershipByUserSurvivesReconnect(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("owned by u1", 10, "u1", true, context.Background())

	// Same user, different connection ID (a reconnect): still the owner.
	if s := node.sharedSnapshotForConn(11, "u1"); !s.IsOwner {
		t.Error("IsOwner should be true for the same user on a new connection")
	}
	// Different user: not the owner, even sharing the original connection ID.
	if s := node.sharedSnapshotForConn(10, "u2"); s.IsOwner {
		t.Error("IsOwner should be false for a different user")
	}
}

// When the owner was unauthenticated (no user ID), ownership falls back to the
// creating connection ID, preserving the pre-existing behavior.
func TestSharedTaskOwnershipFallsBackToConn(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("anon task", 7, "", true, context.Background())

	if s := node.sharedSnapshotForConn(7, ""); !s.IsOwner {
		t.Error("IsOwner should be true for the creating connection when owner is unauthenticated")
	}
	if s := node.sharedSnapshotForConn(8, ""); s.IsOwner {
		t.Error("IsOwner should be false for a different connection")
	}
}

// CancelTaskByID cancels regardless of authorizer or connection, and reports
// whether a live task was found.
func TestCancelTaskByID(t *testing.T) {
	tm := managerWithOptions(t, &enableOptions{
		cancelAuthorizer: func(context.Context, TaskCancelInfo) error {
			return aprot.ErrForbidden("nobody may cancel via RPC")
		},
	})

	node := tm.create("server-cancelable", 1, "u1", true, context.Background())
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	ctx := withTaskManager(context.Background(), tm)
	if !CancelTaskByID(ctx, node.id) {
		t.Fatal("CancelTaskByID returned false for a live task")
	}
	if s := node.sharedSnapshot(); s.Status != TaskNodeStatusFailed {
		t.Errorf("status after CancelTaskByID: got %v, want Failed", s.Status)
	}
	if CancelTaskByID(ctx, "no-such-task") {
		t.Error("CancelTaskByID returned true for a missing task")
	}
}

// FindSharedTask returns the current wire state of a live task, and reports
// absence for unknown IDs.
func TestFindSharedTask(t *testing.T) {
	_, tm := setupTestServer(t)

	node := tm.create("findable", 1, "u1", true, context.Background())
	node.progress(3, 10)

	state, ok := FindSharedTask(withTaskManager(context.Background(), tm), node.id)
	if !ok {
		t.Fatal("FindSharedTask did not find a live task")
	}
	if state.ID != node.id || state.Title != "findable" {
		t.Errorf("unexpected state: %+v", state)
	}
	if state.Current != 3 || state.Total != 10 {
		t.Errorf("progress not reflected: got %d/%d, want 3/10", state.Current, state.Total)
	}

	if _, ok := FindSharedTask(withTaskManager(context.Background(), tm), "missing"); ok {
		t.Error("FindSharedTask reported a missing task as present")
	}
}
