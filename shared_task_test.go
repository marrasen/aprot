package aprot

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
)

func TestSharedTaskBasic(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	if tm == nil {
		t.Fatal("expected taskManager to be initialized")
	}

	core := tm.create("Build project", context.Background())
	task := &SharedTask[struct{}]{core: core}
	if task.ID() == "" {
		t.Fatal("expected non-empty task ID")
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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Upload", context.Background())
	task := &SharedTask[struct{}]{core: core}

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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Deploy", context.Background())
	task := &SharedTask[struct{}]{core: core}

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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Long task", context.Background())

	ok := tm.cancelTask(core.id)
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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	ok := server.taskManager.cancelTask("nonexistent")
	if ok {
		t.Error("expected cancel of non-existent task to return false")
	}
}

func TestSharedTaskBatchBroadcast(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	// Create a task and update progress many times rapidly.
	core := tm.create("Batch test", context.Background())
	task := &SharedTask[struct{}]{core: core}
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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Failing task", context.Background())
	task := &SharedTask[struct{}]{core: core}

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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
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
	core := server.taskManager.create("Test task", context.Background())
	core.progress(1, 10)

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

	core.closeTask()
}

// Integration test: New client gets current shared tasks on connect.
func TestSharedTaskPushOnConnect(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Create a shared task before any client connects
	core := server.taskManager.create("Existing task", context.Background())
	core.progress(5, 10)

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

	core.closeTask()
}

func TestSharedTaskSetMeta(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Meta task", context.Background())

	type Meta struct {
		UserName string `json:"userName"`
	}

	task := &SharedTask[Meta]{core: core}
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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Parent", context.Background())

	type SubMeta struct {
		Key string `json:"key"`
	}

	task := &SharedTask[SubMeta]{core: core}
	sub := task.SubTask("Child")
	sub.SetMeta(SubMeta{Key: "value"})

	states := tm.snapshotAll()
	child := states[0].Children[0]
	meta, ok := child.Meta.(SubMeta)
	if !ok {
		t.Fatalf("expected SubMeta, got %T", child.Meta)
	}
	if meta.Key != "value" {
		t.Errorf("expected key='value', got %s", meta.Key)
	}

	task.Close()
}

func TestSharedTaskSubSubTask(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Root", context.Background())
	task := &SharedTask[struct{}]{core: core}

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

// --- SharedSubTask / dual-send tests ---

// setupSharedSubTaskEnv creates a test environment with both a task tree (request-scoped)
// and a taskManager (shared), returning the context, tree, and taskManager.
func setupSharedSubTaskEnv(t *testing.T) (context.Context, *taskTree, *taskManager, *Server) {
	t.Helper()
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	t.Cleanup(func() { server.Stop(context.Background()) })

	tm := server.taskManager
	if tm == nil {
		t.Fatal("expected taskManager")
	}

	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
		server:    server,
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)

	ctx := withTaskTree(context.Background(), tree)
	ctx = withConnection(ctx, conn)

	return ctx, tree, tm, server
}

func TestSubTaskWithSharedContext(t *testing.T) {
	ctx, tree, tm, _ := setupSharedSubTaskEnv(t)

	// Create a shared task and attach its context.
	core := tm.create("Shared parent", context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	err := SubTask(ctx, "Step A", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request-scoped tree should have the node.
	snap := tree.snapshot()
	if len(snap) != 1 || snap[0].Title != "Step A" {
		t.Fatalf("expected request-scoped node 'Step A', got %+v", snap)
	}
	if snap[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected completed, got %s", snap[0].Status)
	}

	// Shared task should also have a child node.
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 shared task, got %d", len(states))
	}
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 shared child, got %d", len(states[0].Children))
	}
	if states[0].Children[0].Title != "Step A" {
		t.Errorf("expected shared child 'Step A', got %s", states[0].Children[0].Title)
	}
	if states[0].Children[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected shared child completed, got %s", states[0].Children[0].Status)
	}

	core.closeTask()
}

func TestTaskProgressDualSend(t *testing.T) {
	ctx, tree, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Dual progress", context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	err := SubTask(ctx, "Step X", func(ctx context.Context) error {
		TaskProgress(ctx, 7, 20)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request-scoped snapshot should have progress.
	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap))
	}
	if snap[0].Current != 7 || snap[0].Total != 20 {
		t.Errorf("request-scoped: expected 7/20, got %d/%d", snap[0].Current, snap[0].Total)
	}

	// Shared snapshot should also have progress.
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 shared task, got %d", len(states))
	}
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 shared child, got %d", len(states[0].Children))
	}
	child := states[0].Children[0]
	if child.Current != 7 || child.Total != 20 {
		t.Errorf("shared: expected 7/20, got %d/%d", child.Current, child.Total)
	}

	core.closeTask()
}

func TestStepTaskProgressDualSend(t *testing.T) {
	ctx, tree, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Dual step", context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	err := SubTask(ctx, "Step Y", func(ctx context.Context) error {
		TaskProgress(ctx, 0, 5)
		StepTaskProgress(ctx, 1)
		StepTaskProgress(ctx, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request-scoped.
	snap := tree.snapshot()
	if snap[0].Current != 2 || snap[0].Total != 5 {
		t.Errorf("request-scoped: expected 2/5, got %d/%d", snap[0].Current, snap[0].Total)
	}

	// Shared.
	states := tm.snapshotAll()
	child := states[0].Children[0]
	if child.Current != 2 || child.Total != 5 {
		t.Errorf("shared: expected 2/5, got %d/%d", child.Current, child.Total)
	}

	core.closeTask()
}

func TestSubTaskWithSharedContextNested(t *testing.T) {
	ctx, tree, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Root shared", context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	err := SubTask(ctx, "Parent", func(ctx context.Context) error {
		return SubTask(ctx, "Child", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify request-scoped hierarchy.
	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 top-level, got %d", len(snap))
	}
	if len(snap[0].Children) != 1 || snap[0].Children[0].Title != "Child" {
		t.Fatalf("expected nested child 'Child', got %+v", snap[0].Children)
	}

	// Verify shared hierarchy mirrors it.
	states := tm.snapshotAll()
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 shared child, got %d", len(states[0].Children))
	}
	sharedParent := states[0].Children[0]
	if sharedParent.Title != "Parent" {
		t.Errorf("expected 'Parent', got %s", sharedParent.Title)
	}
	if len(sharedParent.Children) != 1 {
		t.Fatalf("expected 1 shared grandchild, got %d", len(sharedParent.Children))
	}
	if sharedParent.Children[0].Title != "Child" {
		t.Errorf("expected 'Child', got %s", sharedParent.Children[0].Title)
	}

	core.closeTask()
}

func TestSubTaskWithSharedContextError(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Error test", context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	testErr := errors.New("boom")
	err := SubTask(ctx, "Failing", func(ctx context.Context) error {
		return testErr
	})
	if err != testErr {
		t.Fatalf("expected testErr, got %v", err)
	}

	states := tm.snapshotAll()
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 shared child, got %d", len(states[0].Children))
	}
	if states[0].Children[0].Status != TaskNodeStatusFailed {
		t.Errorf("expected shared child failed, got %s", states[0].Children[0].Status)
	}

	core.closeTask()
}

func TestSharedSubTaskStandalone(t *testing.T) {
	ctx, tree, tm, _ := setupSharedSubTaskEnv(t)

	err := SharedSubTask(ctx, "Deploy", func(ctx context.Context) error {
		// Inside, SubTask should dual-send.
		return SubTask(ctx, "Build", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request-scoped tree should have Deploy > Build.
	snap := tree.snapshot()
	if len(snap) != 1 || snap[0].Title != "Deploy" {
		t.Fatalf("expected 'Deploy', got %+v", snap)
	}
	if len(snap[0].Children) != 1 || snap[0].Children[0].Title != "Build" {
		t.Fatalf("expected child 'Build', got %+v", snap[0].Children)
	}

	// The shared task core should have been created and auto-closed.
	// Wait for deferred removal.
	time.Sleep(300 * time.Millisecond)
	states := tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected shared task to be removed after close, got %d", len(states))
	}
}

func TestSharedSubTaskNested(t *testing.T) {
	ctx, tree, tm, _ := setupSharedSubTaskEnv(t)

	err := SharedSubTask(ctx, "Outer", func(ctx context.Context) error {
		// Nested SharedSubTask should detect existing sharedContext
		// and delegate to SubTask (not create a new core).
		return SharedSubTask(ctx, "Inner", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request-scoped tree: Outer > Inner.
	snap := tree.snapshot()
	if len(snap) != 1 || snap[0].Title != "Outer" {
		t.Fatalf("expected 'Outer', got %+v", snap)
	}
	if len(snap[0].Children) != 1 || snap[0].Children[0].Title != "Inner" {
		t.Fatalf("expected child 'Inner', got %+v", snap[0].Children)
	}

	// Wait for core removal.
	time.Sleep(300 * time.Millisecond)
	states := tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected 0 shared tasks after nested SharedSubTask, got %d", len(states))
	}
}

func TestSharedSubTaskErrorPropagation(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	testErr := errors.New("deploy failed")
	err := SharedSubTask(ctx, "Deploy", func(ctx context.Context) error {
		return testErr
	})
	if err != testErr {
		t.Fatalf("expected testErr, got %v", err)
	}

	// The shared task core should be marked as failed before removal.
	// Check immediately — before the 200ms removal delay.
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 shared task, got %d", len(states))
	}
	if states[0].Status != TaskNodeStatusFailed {
		t.Errorf("expected failed, got %s", states[0].Status)
	}

	// Wait for deferred removal.
	time.Sleep(300 * time.Millisecond)
	states = tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected 0 shared tasks after failure removal, got %d", len(states))
	}
}

func TestSharedSubTaskWithoutTaskTree(t *testing.T) {
	// Context has a connection + taskManager but NO task tree.
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
		server:    server,
	}
	ctx := withConnection(context.Background(), conn)

	called := false
	err := SharedSubTask(ctx, "Shared only", func(ctx context.Context) error {
		called = true
		// SubTask inside should still work for the shared side.
		return SubTask(ctx, "Sub step", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("function not called")
	}

	// The shared task should have been created and auto-closed.
	time.Sleep(300 * time.Millisecond)
	states := server.taskManager.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected 0 tasks after close, got %d", len(states))
	}
}

func TestSharedSubTaskWithoutConnection(t *testing.T) {
	// No connection, no task tree — should just run fn.
	called := false
	err := SharedSubTask(context.Background(), "Bare", func(ctx context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("function not called")
	}
}

func TestSharedTaskWithContextAndSubTask(t *testing.T) {
	// Test SharedTask.WithContext() + SubTask inside a goroutine.
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
		server:    server,
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)

	baseCtx := withTaskTree(context.Background(), tree)
	baseCtx = withConnection(baseCtx, conn)

	core := tm.create("Background job", baseCtx)
	task := &SharedTask[struct{}]{core: core}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Attach shared context for SubTask dual-send.
		ctx := task.WithContext(context.Background())
		// Also attach the task tree from the original request.
		ctx = withTaskTree(ctx, tree)

		err := SubTask(ctx, "Step in goroutine", func(ctx context.Context) error {
			return nil
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	snap := tree.snapshot()
	found := false
	for _, n := range snap {
		if n.Title == "Step in goroutine" {
			found = true
			if n.Status != TaskNodeStatusCompleted {
				t.Errorf("expected completed, got %s", n.Status)
			}
		}
	}
	if !found {
		t.Error("expected 'Step in goroutine' in request-scoped tree")
	}

	core.closeTask()
}

func TestOutputDualSend(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	var captured []ProgressMessage
	conn := &Conn{
		transport: &mockTransport{
			sendFn: func(data []byte) error {
				var msg ProgressMessage
				unmarshalJSON(data, &msg)
				captured = append(captured, msg)
				return nil
			},
		},
		server: server,
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)

	ctx := withTaskTree(context.Background(), tree)

	core := tm.create("Output test", context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	Output(ctx, "hello dual")

	// Request-scoped: should have received the output.
	if len(captured) != 1 || captured[0].Output != "hello dual" {
		t.Errorf("expected request-scoped output 'hello dual', got %+v", captured)
	}

	// Shared: output is sent via sendOutput which calls Broadcast.
	// We can't easily capture that without a connected client, but we verified
	// it doesn't panic and the code path is exercised.

	core.closeTask()
}

func TestSharedTaskNodeSubTaskRefactor(t *testing.T) {
	// Verify the refactored sharedTaskNode.subTask() works correctly.
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	registry.EnableTasks()

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Root", context.Background())
	task := &SharedTask[struct{}]{core: core}

	// Create sub-task via the refactored SharedTaskSub.SubTask()
	sub1 := task.SubTask("L1")
	sub2 := sub1.SubTask("L2")
	sub3 := sub2.SubTask("L3")
	sub3.Complete()
	sub2.Complete()
	sub1.Complete()

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	// Verify 3-level nesting.
	l1 := states[0].Children
	if len(l1) != 1 || l1[0].Title != "L1" {
		t.Fatalf("expected L1, got %+v", l1)
	}
	l2 := l1[0].Children
	if len(l2) != 1 || l2[0].Title != "L2" {
		t.Fatalf("expected L2, got %+v", l2)
	}
	l3 := l2[0].Children
	if len(l3) != 1 || l3[0].Title != "L3" {
		t.Fatalf("expected L3, got %+v", l3)
	}
	if l3[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected L3 completed, got %s", l3[0].Status)
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
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasksWithMeta[TaskMeta](registry)

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
