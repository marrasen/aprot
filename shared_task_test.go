package aprot

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/marrasen/aprot/tasks"
)

func TestSharedTaskBasic(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	if tm == nil {
		t.Fatal("expected taskManager to be initialized")
	}

	core := tm.create("Build project", 0, true, context.Background())
	task := &SharedTask[struct{}]{core: core}
	if task.ID() == "" {
		t.Fatal("expected non-empty task ID")
	}

	// Check snapshot — tm.create() leaves the task in "created" status
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Title != "Build project" {
		t.Errorf("expected 'Build project', got %s", states[0].Title)
	}
	if states[0].Status != tasks.TaskNodeStatusCreated {
		t.Errorf("expected created status, got %s", states[0].Status)
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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Upload", 0, true, context.Background())
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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Deploy", 0, true, context.Background())
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
	if child.Status != tasks.TaskNodeStatusCompleted {
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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Long task", 0, true, context.Background())

	ok := tm.cancelTask(core.id)
	if !ok {
		t.Fatal("expected cancel to succeed")
	}

	// Task should be marked as failed
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Status != tasks.TaskNodeStatusFailed {
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
	EnableTasks(registry)

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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	// Create a task and update progress many times rapidly.
	core := tm.create("Batch test", 0, true, context.Background())
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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Failing task", 0, true, context.Background())
	task := &SharedTask[struct{}]{core: core}

	task.Fail("something went wrong")

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Status != tasks.TaskNodeStatusFailed {
		t.Errorf("expected failed status, got %s", states[0].Status)
	}
	if states[0].Error != "something went wrong" {
		t.Errorf("expected error 'something went wrong', got %q", states[0].Error)
	}
}

func TestSharedTaskErr(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	// Err(nil) should complete the task.
	core1 := tm.create("Task OK", 0, true, context.Background())
	task1 := &SharedTask[struct{}]{core: core1}
	task1.Err(nil)

	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Status != tasks.TaskNodeStatusCompleted {
		t.Errorf("expected completed, got %s", states[0].Status)
	}
	if states[0].Error != "" {
		t.Errorf("expected no error, got %q", states[0].Error)
	}

	// Wait for deferred removal.
	time.Sleep(300 * time.Millisecond)

	// Err(error) should fail the task with the error message.
	core2 := tm.create("Task Fail", 0, true, context.Background())
	task2 := &SharedTask[struct{}]{core: core2}
	task2.Err(errors.New("deploy failed"))

	states = tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 task, got %d", len(states))
	}
	if states[0].Status != tasks.TaskNodeStatusFailed {
		t.Errorf("expected failed, got %s", states[0].Status)
	}
	if states[0].Error != "deploy failed" {
		t.Errorf("expected error 'deploy failed', got %q", states[0].Error)
	}
}

func TestSharedTaskSubErr(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Parent", 0, true, context.Background())
	task := &SharedTask[struct{}]{core: core}

	// Err(nil) should complete the sub-task.
	sub1 := task.SubTask("Sub OK")
	sub1.Err(nil)

	states := tm.snapshotAll()
	if len(states[0].Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(states[0].Children))
	}
	if states[0].Children[0].Status != tasks.TaskNodeStatusCompleted {
		t.Errorf("expected completed, got %s", states[0].Children[0].Status)
	}
	if states[0].Children[0].Error != "" {
		t.Errorf("expected no error, got %q", states[0].Children[0].Error)
	}

	// Err(error) should fail the sub-task with the error message.
	sub2 := task.SubTask("Sub Fail")
	sub2.Err(errors.New("step failed"))

	states = tm.snapshotAll()
	if len(states[0].Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(states[0].Children))
	}
	// Find the failed child.
	var failed *tasks.TaskNode
	for _, child := range states[0].Children {
		if child.Title == "Sub Fail" {
			failed = child
		}
	}
	if failed == nil {
		t.Fatal("expected to find 'Sub Fail' child")
	}
	if failed.Status != tasks.TaskNodeStatusFailed {
		t.Errorf("expected failed, got %s", failed.Status)
	}
	if failed.Error != "step failed" {
		t.Errorf("expected error 'step failed', got %q", failed.Error)
	}

	task.Close()
}

// Test that EnableTasks registers the CancelTask handler.
func TestEnableTasksRegistersHandler(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

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
	if !found["TaskUpdateEvent"] {
		t.Error("expected TaskUpdateEvent push event")
	}
}

// Integration test: SharedTask broadcast over WebSocket.
func TestSharedTaskBroadcastOverWebSocket(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)

	// Create a shared task directly on the server's task manager
	core := server.taskManager.create("Test task", 0, true, context.Background())
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
	EnableTasks(registry)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Create a shared task before any client connects
	core := server.taskManager.create("Existing task", 0, true, context.Background())
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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Meta task", 0, true, context.Background())

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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Parent", 0, true, context.Background())

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
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Root", 0, true, context.Background())
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
	if gc.Status != tasks.TaskNodeStatusCompleted {
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
	EnableTasks(registry)

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
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	// Create a shared task and attach its context.
	core := tm.create("Shared parent", 0, true, context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	err := SubTask(ctx, "Step A", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With shared context present, SubTask routes exclusively through the shared
	// task system. Only verify the shared task child node.
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
	if states[0].Children[0].Status != tasks.TaskNodeStatusCompleted {
		t.Errorf("expected shared child completed, got %s", states[0].Children[0].Status)
	}

	core.closeTask()
}

func TestTaskProgressSharedContext(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Shared progress", 0, true, context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	err := SubTask(ctx, "Step X", func(ctx context.Context) error {
		TaskProgress(ctx, 7, 20)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With shared context present, all updates route exclusively through the
	// shared task system. Verify the shared child has progress 7/20.
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

func TestStepTaskProgressSharedContext(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Shared step", 0, true, context.Background())
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

	// With shared context present, all updates route exclusively through the
	// shared task system. Verify the shared child has 2/5.
	states := tm.snapshotAll()
	child := states[0].Children[0]
	if child.Current != 2 || child.Total != 5 {
		t.Errorf("shared: expected 2/5, got %d/%d", child.Current, child.Total)
	}

	core.closeTask()
}

func TestSubTaskWithSharedContextNested(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	core := tm.create("Root shared", 0, true, context.Background())
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

	// With shared context present, all nodes route exclusively through the
	// shared task system. Verify the shared hierarchy: core > "Parent" > "Child".
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

	core := tm.create("Error test", 0, true, context.Background())
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
	if states[0].Children[0].Status != tasks.TaskNodeStatusFailed {
		t.Errorf("expected shared child failed, got %s", states[0].Children[0].Status)
	}

	core.closeTask()
}

func TestSharedSubTaskStandalone(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	err := SharedSubTask(ctx, "Deploy", func(ctx context.Context) error {
		// Inside, SubTask exclusively uses the shared path (shared context present).
		return SubTask(ctx, "Build", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The shared task core should have been created and auto-closed.
	// The request-scoped tree is empty because shared context was present.
	// Wait for deferred removal.
	time.Sleep(300 * time.Millisecond)
	states := tm.snapshotAll()
	if len(states) != 0 {
		t.Errorf("expected shared task to be removed after close, got %d", len(states))
	}
}

func TestSharedSubTaskNested(t *testing.T) {
	ctx, _, tm, _ := setupSharedSubTaskEnv(t)

	err := SharedSubTask(ctx, "Outer", func(ctx context.Context) error {
		// Nested SharedSubTask detects existing sharedContext and delegates to
		// SubTask, which routes exclusively through the shared task system.
		return SharedSubTask(ctx, "Inner", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The request-scoped tree is empty because shared context was present.
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
	if states[0].Status != tasks.TaskNodeStatusFailed {
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
	EnableTasks(registry)

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
	// WithContext attaches a sharedContext, so SubTask routes exclusively
	// through the shared task system (no request-scoped tree entry).
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager

	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
		server:    server,
	}

	baseCtx := withConnection(context.Background(), conn)

	core := tm.create("Background job", 0, true, baseCtx)
	task := &SharedTask[struct{}]{core: core}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// WithContext attaches a sharedContext; SubTask routes exclusively through shared.
		ctx := task.WithContext(context.Background())

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

	// Verify the shared task child was created and completed.
	states := tm.snapshotAll()
	if len(states) != 1 {
		t.Fatalf("expected 1 shared task, got %d", len(states))
	}
	found := false
	for _, child := range states[0].Children {
		if child.Title == "Step in goroutine" {
			found = true
			if child.Status != tasks.TaskNodeStatusCompleted {
				t.Errorf("expected completed, got %s", child.Status)
			}
		}
	}
	if !found {
		t.Error("expected 'Step in goroutine' in shared task children")
	}

	core.closeTask()
}

func TestOutputSharedContextRoutesExclusively(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

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

	core := tm.create("Output test", 0, true, context.Background())
	sc := &sharedContext{core: core}
	ctx = withSharedContext(ctx, sc)

	Output(ctx, "hello shared")

	// When shared context is present, output should NOT go through
	// the request-scoped progress channel — only through the shared
	// task system (Broadcast). So captured should be empty.
	if len(captured) != 0 {
		t.Errorf("expected no request-scoped messages when shared context present, got %d", len(captured))
	}

	core.closeTask()
}

func TestSharedTaskNodeSubTaskRefactor(t *testing.T) {
	// Verify the refactored sharedTaskNode.subTask() works correctly.
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Root", 0, true, context.Background())
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
	if l3[0].Status != tasks.TaskNodeStatusCompleted {
		t.Errorf("expected L3 completed, got %s", l3[0].Status)
	}

	task.Close()
}

func TestSharedTaskSnapshotForConn(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core := tm.create("Owned task", 42, true, context.Background())

	// Snapshot for the owner connection.
	state := core.snapshotForConn(42)
	if !state.IsOwner {
		t.Error("expected IsOwner=true for owner connection")
	}

	// Snapshot for a different connection.
	state = core.snapshotForConn(99)
	if state.IsOwner {
		t.Error("expected IsOwner=false for non-owner connection")
	}

	// Snapshot for connID 0 (no owner).
	state = core.snapshotForConn(0)
	if state.IsOwner {
		t.Error("expected IsOwner=false for connID 0")
	}

	core.closeTask()
}

func TestSharedTaskSnapshotAllForConn(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	defer server.Stop(context.Background())

	tm := server.taskManager
	core1 := tm.create("Task A", 10, true, context.Background())
	core2 := tm.create("Task B", 20, true, context.Background())

	// From conn 10's perspective: Task A is owned, Task B is not.
	states := tm.snapshotAllForConn(10)
	if len(states) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(states))
	}
	for _, s := range states {
		switch s.Title {
		case "Task A":
			if !s.IsOwner {
				t.Error("expected Task A IsOwner=true for conn 10")
			}
		case "Task B":
			if s.IsOwner {
				t.Error("expected Task B IsOwner=false for conn 10")
			}
		}
	}

	// From conn 20's perspective: Task B is owned, Task A is not.
	states = tm.snapshotAllForConn(20)
	for _, s := range states {
		switch s.Title {
		case "Task A":
			if s.IsOwner {
				t.Error("expected Task A IsOwner=false for conn 20")
			}
		case "Task B":
			if !s.IsOwner {
				t.Error("expected Task B IsOwner=true for conn 20")
			}
		}
	}

	core1.closeTask()
	core2.closeTask()
}

func TestSharedTaskIsOwnerBroadcast(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws1 := connectWS(t, ts)
	defer ws1.Close()

	ws2 := connectWS(t, ts)
	defer ws2.Close()

	// Wait for both connections to be registered.
	time.Sleep(50 * time.Millisecond)

	// Capture connected connection IDs via the server.
	var connIDs []uint64
	server.mu.RLock()
	for conn := range server.conns {
		connIDs = append(connIDs, conn.ID())
	}
	server.mu.RUnlock()

	if len(connIDs) < 2 {
		t.Fatalf("expected at least 2 connections, got %d", len(connIDs))
	}

	// Create a task owned by the first connection ID.
	ownerID := connIDs[0]
	core := server.taskManager.create("Owner test", ownerID, true, context.Background())
	core.progress(1, 10)

	// Wait for batch flush.
	time.Sleep(300 * time.Millisecond)

	// Helper to read TaskStateEvent and extract isOwner from first task.
	readIsOwner := func(ws interface{ ReadMessage() (int, []byte, error) }) *bool {
		for i := 0; i < 10; i++ {
			_, data, err := ws.ReadMessage()
			if err != nil {
				break
			}
			s := string(data)
			if !strings.Contains(s, "TaskStateEvent") {
				continue
			}
			// Parse to get isOwner value.
			var msg struct {
				Data struct {
					Tasks []struct {
						IsOwner bool `json:"isOwner"`
					} `json:"tasks"`
				} `json:"data"`
			}
			json.Unmarshal(data, &msg)
			if len(msg.Data.Tasks) > 0 {
				v := msg.Data.Tasks[0].IsOwner
				return &v
			}
		}
		return nil
	}

	ws1.SetReadDeadline(time.Now().Add(2 * time.Second))
	ws2.SetReadDeadline(time.Now().Add(2 * time.Second))

	isOwner1 := readIsOwner(ws1)
	isOwner2 := readIsOwner(ws2)

	// One should be true and one should be false.
	if isOwner1 == nil || isOwner2 == nil {
		t.Fatal("expected both clients to receive TaskStateEvent")
	}

	// The owner connection should see isOwner=true, the other should see false.
	if *isOwner1 == *isOwner2 {
		t.Errorf("expected different isOwner values, both got %v", *isOwner1)
	}

	core.closeTask()
}

func TestSharedTaskPushOnConnectIsOwner(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Create a task with a specific owner before any client connects.
	core := server.taskManager.create("Existing task", 42, true, context.Background())
	core.progress(5, 10)

	// Now connect a client (its conn ID will NOT be 42).
	ws := connectWS(t, ts)
	defer ws.Close()

	// The client should receive the task state on connect with isOwner=false.
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	found := false
	for i := 0; i < 10; i++ {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		s := string(data)
		if strings.Contains(s, "TaskStateEvent") && strings.Contains(s, "Existing task") {
			var msg struct {
				Data struct {
					Tasks []struct {
						IsOwner bool `json:"isOwner"`
					} `json:"tasks"`
				} `json:"data"`
			}
			json.Unmarshal(data, &msg)
			if len(msg.Data.Tasks) > 0 {
				if msg.Data.Tasks[0].IsOwner {
					t.Error("expected isOwner=false for non-owner connection on connect")
				}
				found = true
				break
			}
		}
	}

	if !found {
		t.Error("expected new client to receive TaskStateEvent on connect")
	}

	core.closeTask()
}

// readFirstTaskIsOwner reads messages from a WebSocket until it finds a TaskStateEvent
// with at least one task, and returns that task's isOwner value.
func readFirstTaskIsOwner(ws interface{ ReadMessage() (int, []byte, error) }) *bool {
	for i := 0; i < 20; i++ {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if !strings.Contains(string(data), "TaskStateEvent") {
			continue
		}
		var msg struct {
			Data struct {
				Tasks []struct {
					IsOwner bool `json:"isOwner"`
				} `json:"tasks"`
			} `json:"data"`
		}
		json.Unmarshal(data, &msg)
		if len(msg.Data.Tasks) > 0 {
			v := msg.Data.Tasks[0].IsOwner
			return &v
		}
	}
	return nil
}

func TestSharedSubTaskIsOwnerAlwaysFalse(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws1 := connectWS(t, ts)
	defer ws1.Close()

	ws2 := connectWS(t, ts)
	defer ws2.Close()

	// Wait for both connections to be registered.
	time.Sleep(50 * time.Millisecond)

	// Client 1 calls SharedSubTask via RPC — the handler sleeps 500ms,
	// giving the flush loop time to broadcast TaskStateEvent.
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.RunSharedSubTask",
		Params: jsontext.Value(`[{"title":"Deploy"}]`),
	}
	if err := ws1.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	ws1.SetReadDeadline(time.Now().Add(2 * time.Second))
	ws2.SetReadDeadline(time.Now().Add(2 * time.Second))

	isOwner1 := readFirstTaskIsOwner(ws1)
	isOwner2 := readFirstTaskIsOwner(ws2)

	if isOwner1 == nil || isOwner2 == nil {
		t.Fatal("expected both clients to receive TaskStateEvent")
	}

	// Both should be false — SharedSubTask tasks are not top-level.
	if *isOwner1 {
		t.Error("expected isOwner=false for caller (SharedSubTask is not top-level)")
	}
	if *isOwner2 {
		t.Error("expected isOwner=false for observer (SharedSubTask is not top-level)")
	}
}

func TestStartSharedTaskIsOwnerTrue(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(&IntegrationHandlers{}, NotificationEvent{})
	EnableTasks(registry)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws1 := connectWS(t, ts)
	defer ws1.Close()

	ws2 := connectWS(t, ts)
	defer ws2.Close()

	// Wait for both connections to be registered.
	time.Sleep(50 * time.Millisecond)

	// Client 1 calls StartSharedTask via RPC — the handler sleeps 500ms,
	// giving the flush loop time to broadcast TaskStateEvent.
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.RunStartSharedTask",
		Params: jsontext.Value(`[{"title":"Build"}]`),
	}
	if err := ws1.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	ws1.SetReadDeadline(time.Now().Add(2 * time.Second))
	ws2.SetReadDeadline(time.Now().Add(2 * time.Second))

	isOwner1 := readFirstTaskIsOwner(ws1)
	isOwner2 := readFirstTaskIsOwner(ws2)

	if isOwner1 == nil || isOwner2 == nil {
		t.Fatal("expected both clients to receive TaskStateEvent")
	}

	// Client 1 (caller) should see isOwner=true, client 2 should see false.
	if !*isOwner1 {
		t.Error("expected isOwner=true for caller (StartSharedTask is top-level)")
	}
	if *isOwner2 {
		t.Error("expected isOwner=false for observer")
	}
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
