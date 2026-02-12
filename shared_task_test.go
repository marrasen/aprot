package aprot

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
)

func TestSharedTaskBasic(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	if tm == nil {
		t.Fatal("expected taskManager to be initialized")
	}

	task := tm.create("Build project", context.Background())
	if task.ID() == "" {
		t.Fatal("expected non-empty task ID")
	}

	ref := task.Ref()
	if ref.TaskID != task.ID() {
		t.Errorf("expected ref ID %s, got %s", task.ID(), ref.TaskID)
	}

	// Check snapshot
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Title != "Build project" {
		t.Errorf("expected 'Build project', got %s", states[0].Title)
	}
	if states[0].Status != TaskNodeStatusRunning {
		t.Errorf("expected running status, got %s", states[0].Status)
	}

	// Close the task
	task.Close()
	time.Sleep(300 * time.Millisecond) // Wait for deferred removal

	states = tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected 0 tasks after close, got %d", len(states))
	}
}

func TestSharedTaskProgress(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Upload", context.Background())

	task.Progress(50, 100)

	states := tm.snapshotAll()
	if states[0].Current != 50 || states[0].Total != 100 {
		t.Errorf("expected 50/100, got %d/%d", states[0].Current, states[0].Total)
	}

	task.Close()
}

func TestSharedTaskSubTask(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Deploy", context.Background())

	sub := task.SubTask("Build image")
	sub.Progress(1, 3)
	sub.Complete()

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(states[0].Children))
	}
	child := states[0].Children[0]
	if child.Title != "Build image" {
		t.Errorf("expected 'Build image', got %s", child.Title)
	}
	if child.Status != TaskNodeStatusCompleted {
		t.Errorf("expected completed, got %s", child.Status)
	}
	if child.Current != 1 || child.Total != 3 {
		t.Errorf("expected 1/3, got %d/%d", child.Current, child.Total)
	}

	task.Close()
}

func TestSharedTaskCancel(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Long task", context.Background())

	ok := tm.cancelTask(task.ID())
	if !ok {
		t.Fatal("expected cancel to succeed")
	}

	// Task should be marked as failed
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Status != TaskNodeStatusFailed {
		t.Errorf("expected failed status, got %s", states[0].Status)
	}

	// Wait for deferred removal
	time.Sleep(300 * time.Millisecond)
	states = tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected 0 tasks after cancel removal, got %d", len(states))
	}
}

func TestSharedTaskCancelNonExistent(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	ok := server.taskManager.cancelTask("nonexistent")
	if ok {
		t.Error("expected cancel of non-existent task to return false")
	}
}

func TestSharedTaskGo(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Background work", context.Background())

	var ran bool
	var mu sync.Mutex
	done := make(chan struct{})

	task.Go(func(ctx context.Context) {
		mu.Lock()
		ran = true
		mu.Unlock()
		close(done)
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Go function")
	}

	mu.Lock()
	if !ran {
		t.Error("expected Go function to have run")
	}
	mu.Unlock()

	// Wait for auto-close + deferred removal
	time.Sleep(400 * time.Millisecond)
	states := tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected 0 tasks after Go completes, got %d", len(states))
	}
}

func TestSharedTaskBatchBroadcast(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	// Create a task and update progress many times rapidly.
	task := tm.create("Batch test", context.Background())
	for i := 0; i < 100; i++ {
		task.Progress(i, 100)
	}

	// Wait for at least one batch flush.
	time.Sleep(250 * time.Millisecond)

	// The task should exist with the latest progress.
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Current != 99 {
		t.Errorf("expected current=99, got %d", states[0].Current)
	}

	task.Close()
}

func TestSharedTaskFail(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Failing task", context.Background())

	task.Fail()

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Status != TaskNodeStatusFailed {
		t.Errorf("expected failed status, got %s", states[0].Status)
	}
}

// Test that EnableTasks registers the CancelTask handler.
func TestEnableTasksRegistersHandler(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	_, ok := registry.Get("taskCancelHandler.CancelTask")
	if !ok {
		t.Fatal("expected CancelTask handler to be registered")
	}

	// Check push events
	events := registry.PushEvents()
	found := map[string]bool{}
	for _, e := range events {
		found[e.Name] = true
	}
	if !found["TaskStateEvent"] {
		t.Error("expected TaskStateEvent push event")
	}
	if !found["TaskOutputEvent"] {
		t.Error("expected TaskOutputEvent push event")
	}
}

// Integration test: SharedTask broadcast over WebSocket.
func TestSharedTaskBroadcastOverWebSocket(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)

	// Create a shared task directly on the server's task manager
	task := server.taskManager.create("Test task", context.Background())
	task.Progress(1, 10)

	// Wait for batch flush
	time.Sleep(300 * time.Millisecond)

	// We should receive a TaskStateEvent push
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	found := false
	for i := 0; i < 10; i++ {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if strings.Contains(string(data), "TaskStateEvent") {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to receive TaskStateEvent push")
	}

	task.Close()
}

// Integration test: New client gets current shared tasks on connect.
func TestSharedTaskPushOnConnect(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Create a shared task before any client connects
	task := server.taskManager.create("Existing task", context.Background())
	task.Progress(5, 10)

	// Now connect a client
	ws := connectWS(t, ts)
	defer ws.Close()

	// The client should receive the task state immediately after connect
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	found := false
	for i := 0; i < 10; i++ {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		s := string(data)
		if strings.Contains(s, "TaskStateEvent") && strings.Contains(s, "Existing task") {
			// Verify the data contains our task
			var msg PushMessage
			json.Unmarshal(data, &msg)
			found = true
			break
		}
	}

	if !found {
		t.Error("expected new client to receive existing shared tasks on connect")
	}

	task.Close()
}

func TestSharedTaskSetMeta(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Meta task", context.Background())

	type Meta struct {
		UserName string `json:"userName"`
	}

	task.SetMeta(Meta{UserName: "alice"})

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	meta, ok := states[0].Meta.(Meta)
	if !ok {
		t.Fatalf("expected Meta type, got %T", states[0].Meta)
	}
	if meta.UserName != "alice" {
		t.Errorf("expected userName 'alice', got %s", meta.UserName)
	}

	task.Close()
}

func TestSharedTaskSubSetMeta(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Parent", context.Background())

	sub := task.SubTask("Child")
	sub.SetMeta(map[string]string{"key": "value"})

	states := tm.snapshotAll()
	child := states[0].Children[0]
	meta, ok := child.Meta.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", child.Meta)
	}
	if meta["key"] != "value" {
		t.Errorf("expected key='value', got %s", meta["key"])
	}

	task.Close()
}

func TestSharedTaskSubSubTask(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	task := tm.create("Root", context.Background())

	sub := task.SubTask("Level 1")
	grandchild := sub.SubTask("Level 2")
	grandchild.Complete()
	sub.Complete()

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(states[0].Children))
	}
	child := states[0].Children[0]
	if child.Title != "Level 1" {
		t.Errorf("expected 'Level 1', got %s", child.Title)
	}
	if len(child.Children) != 1 {
		t.Fatalf("expected 1 grandchild, got %d", len(child.Children))
	}
	gc := child.Children[0]
	if gc.Title != "Level 2" {
		t.Errorf("expected 'Level 2', got %s", gc.Title)
	}
	if gc.Status != TaskNodeStatusCompleted {
		t.Errorf("expected completed, got %s", gc.Status)
	}

	task.Close()
}

func TestEnableTasksWithMeta(t *testing.T) {
	type TaskMeta struct {
		UserName string `json:"userName,omitempty"`
		Error    string `json:"error,omitempty"`
	}

	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEvent(NotificationEvent{})
	registry.EnableTasksWithMeta(TaskMeta{})

	if !registry.TasksEnabled() {
		t.Fatal("expected tasks to be enabled")
	}

	metaType := registry.TaskMetaType()
	if metaType == nil {
		t.Fatal("expected non-nil meta type")
	}
	if metaType.Name() != "TaskMeta" {
		t.Errorf("expected meta type name 'TaskMeta', got %s", metaType.Name())
	}
	if metaType.NumField() != 2 {
		t.Errorf("expected 2 fields, got %d", metaType.NumField())
	}
}
