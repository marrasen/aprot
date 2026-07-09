package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
)

// TestListTasksReturnsSnapshotForConn verifies the ListTasks RPC answers from
// the live manager with IsOwner evaluated against the calling connection.
func TestListTasksReturnsSnapshotForConn(t *testing.T) {
	_, tm := setupTestServer(t)
	h := &tasksHandler{tm: tm}

	const ownerID uint64 = 7
	const otherID uint64 = 8
	node := tm.create("list task", ownerID, "", true, context.Background())

	ownerCtx := aprot.WithTestConnection(context.Background(), ownerID)
	states, err := h.ListTasks(ownerCtx)
	if err != nil {
		t.Fatalf("ListTasks (owner): %v", err)
	}
	s, ok := findTaskByID(states, node.id)
	if !ok {
		t.Fatal("created task missing from ListTasks snapshot")
	}
	if !s.IsOwner {
		t.Error("IsOwner should be true for the calling (owning) connection")
	}

	otherCtx := aprot.WithTestConnection(context.Background(), otherID)
	otherStates, err := h.ListTasks(otherCtx)
	if err != nil {
		t.Fatalf("ListTasks (other): %v", err)
	}
	so, ok := findTaskByID(otherStates, node.id)
	if !ok {
		t.Fatal("task missing from other connection's ListTasks snapshot")
	}
	if so.IsOwner {
		t.Error("IsOwner should be false for a non-owning connection")
	}
}

// TestListTasksNoConnectionOrManager verifies ListTasks degrades to an empty
// (non-nil) slice when there is no connection or the manager is unset, rather
// than panicking.
func TestListTasksNoConnectionOrManager(t *testing.T) {
	_, tm := setupTestServer(t)

	// No connection on the context.
	h := &tasksHandler{tm: tm}
	states, err := h.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if states == nil {
		t.Error("ListTasks should return a non-nil slice")
	}

	// No manager assigned (server never initialized).
	h2 := &tasksHandler{}
	ctx := aprot.WithTestConnection(context.Background(), 1)
	states2, err := h2.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks (no manager): %v", err)
	}
	if states2 == nil {
		t.Error("ListTasks should return a non-nil slice when manager is unset")
	}
}

// TestConnectPushesEmptySnapshot verifies the server pushes a TaskStateEvent on
// connect even when no tasks exist, so a reconnecting client whose task
// finished while it was away clears its stale state.
func TestConnectPushesEmptySnapshot(t *testing.T) {
	ts := setupEventOrderServer(t)

	url := "ws://" + ts.Listener.Addr().String()
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// First message is the config frame; the next must be the on-connect
	// TaskStateEvent push carrying an empty task list.
	var sawEmptyState bool
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for range 4 {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		var base struct {
			Type  string `json:"type"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(data, &base); err != nil {
			continue
		}
		if base.Type == "push" && base.Event == "TaskStateEvent" {
			var p struct {
				Data taskStatePush `json:"data"`
			}
			if err := json.Unmarshal(data, &p); err != nil {
				t.Fatalf("unmarshal TaskStateEvent: %v", err)
			}
			if len(p.Data.Tasks) != 0 {
				t.Errorf("expected empty task list on connect, got %d", len(p.Data.Tasks))
			}
			sawEmptyState = true
			break
		}
	}
	if !sawEmptyState {
		t.Error("expected a TaskStateEvent push on connect with no tasks")
	}
}
