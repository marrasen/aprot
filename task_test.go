package aprot

import (
	"context"
	"errors"
	"testing"

	"github.com/go-json-experiment/json"
)

func TestSubTaskBasic(t *testing.T) {
	// Without a task tree in context, SubTask should just run the function.
	called := false
	err := SubTask(context.Background(), "test", func(ctx context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("function was not called")
	}
}

func TestSubTaskWithTree(t *testing.T) {
	// Create a mock progress reporter that captures messages.
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
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	err := SubTask(ctx, "Step 1", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have received 2 progress messages: one for "created", one for "completed".
	if len(captured) < 2 {
		t.Fatalf("expected at least 2 progress messages, got %d", len(captured))
	}

	// First message should have a task in "created" state
	if len(captured[0].Tasks) != 1 {
		t.Fatalf("expected 1 task in first message, got %d", len(captured[0].Tasks))
	}
	if captured[0].Tasks[0].Status != TaskNodeStatusCreated {
		t.Errorf("expected created status, got %s", captured[0].Tasks[0].Status)
	}
	if captured[0].Tasks[0].Title != "Step 1" {
		t.Errorf("expected title 'Step 1', got %s", captured[0].Tasks[0].Title)
	}

	// Last message should have the task in "completed" state
	last := captured[len(captured)-1]
	if last.Tasks[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected completed status, got %s", last.Tasks[0].Status)
	}
}

func TestSubTaskError(t *testing.T) {
	conn := &Conn{
		transport: &mockTransport{
			sendFn: func(data []byte) error { return nil },
		},
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	testErr := errors.New("test failure")
	err := SubTask(ctx, "Failing step", func(ctx context.Context) error {
		return testErr
	})
	if err != testErr {
		t.Fatalf("expected testErr, got %v", err)
	}

	// Check that the task was marked as failed with the error message
	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap))
	}
	if snap[0].Status != TaskNodeStatusFailed {
		t.Errorf("expected failed status, got %s", snap[0].Status)
	}
	if snap[0].Error != "test failure" {
		t.Errorf("expected error 'test failure', got %q", snap[0].Error)
	}
}

func TestSubTaskNested(t *testing.T) {
	conn := &Conn{
		transport: &mockTransport{
			sendFn: func(data []byte) error { return nil },
		},
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	err := SubTask(ctx, "Parent", func(ctx context.Context) error {
		return SubTask(ctx, "Child", func(ctx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 top-level task, got %d", len(snap))
	}
	if snap[0].Title != "Parent" {
		t.Errorf("expected 'Parent', got %s", snap[0].Title)
	}
	if len(snap[0].Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(snap[0].Children))
	}
	if snap[0].Children[0].Title != "Child" {
		t.Errorf("expected 'Child', got %s", snap[0].Children[0].Title)
	}
	if snap[0].Children[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected child completed, got %s", snap[0].Children[0].Status)
	}
}

func TestOutput(t *testing.T) {
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
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	Output(ctx, "hello output")

	if len(captured) != 1 {
		t.Fatalf("expected 1 message, got %d", len(captured))
	}
	if captured[0].Output == nil || *captured[0].Output != "hello output" {
		t.Errorf("expected 'hello output', got %v", captured[0].Output)
	}
	// Output without a task node should have empty TaskID
	if captured[0].TaskID != "" {
		t.Errorf("expected empty taskID, got %s", captured[0].TaskID)
	}
}

func TestOutputWithoutTree(t *testing.T) {
	// Should not panic when there's no tree in context.
	Output(context.Background(), "no tree")
}

func TestOutputWriter(t *testing.T) {
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
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	w := OutputWriter(ctx, "Log output")
	w.Write([]byte("line 1\n"))
	w.Write([]byte("line 2\n"))
	w.Close()

	// Should have task updates + output messages with task IDs
	hasOutput := false
	hasCompleted := false
	var outputTaskID string
	for _, msg := range captured {
		if msg.Output != nil {
			hasOutput = true
			outputTaskID = msg.TaskID
		}
		if len(msg.Tasks) > 0 && msg.Tasks[0].Status == TaskNodeStatusCompleted {
			hasCompleted = true
		}
	}
	if !hasOutput {
		t.Error("expected output messages")
	}
	if !hasCompleted {
		t.Error("expected completed task status")
	}
	// Output messages should carry the task node ID
	if outputTaskID == "" {
		t.Error("expected non-empty taskID on output messages")
	}
}

func TestOutputWriterWithoutTree(t *testing.T) {
	w := OutputWriter(context.Background(), "no tree")
	n, err := w.Write([]byte("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 bytes written, got %d", n)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
}

func TestWriterProgress(t *testing.T) {
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
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	w := WriterProgress(ctx, "Downloading", 100)
	w.Write([]byte("12345")) // 5 bytes
	w.Close()

	// After close, we should see the task with current=5, total=100, completed
	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap))
	}
	if snap[0].Current != 5 {
		t.Errorf("expected current=5, got %d", snap[0].Current)
	}
	if snap[0].Total != 100 {
		t.Errorf("expected total=100, got %d", snap[0].Total)
	}
	if snap[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected completed status, got %s", snap[0].Status)
	}
}

func TestWriterProgressWithoutTree(t *testing.T) {
	w := WriterProgress(context.Background(), "no tree", 100)
	n, err := w.Write([]byte("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 bytes written, got %d", n)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
}

func TestTaskErr(t *testing.T) {
	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	// Err(nil) should complete the task.
	ctx1, task1 := StartTask[struct{}](ctx, "Task OK")
	_ = ctx1
	task1.Err(nil)

	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap))
	}
	if snap[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected completed, got %s", snap[0].Status)
	}
	if snap[0].Error != "" {
		t.Errorf("expected no error, got %q", snap[0].Error)
	}

	// Err(error) should fail the task with the error message.
	ctx2, task2 := StartTask[struct{}](ctx, "Task Fail")
	_ = ctx2
	task2.Err(errors.New("something broke"))

	snap = tree.snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(snap))
	}
	// Find the failed task.
	var failed *TaskNode
	for _, n := range snap {
		if n.Title == "Task Fail" {
			failed = n
		}
	}
	if failed == nil {
		t.Fatal("expected to find 'Task Fail' node")
	}
	if failed.Status != TaskNodeStatusFailed {
		t.Errorf("expected failed, got %s", failed.Status)
	}
	if failed.Error != "something broke" {
		t.Errorf("expected error 'something broke', got %q", failed.Error)
	}
}

func TestTaskProgressBasic(t *testing.T) {
	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	err := SubTask(ctx, "Processing", func(ctx context.Context) error {
		TaskProgress(ctx, 5, 10)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap))
	}
	if snap[0].Current != 5 || snap[0].Total != 10 {
		t.Errorf("expected 5/10, got %d/%d", snap[0].Current, snap[0].Total)
	}
}

func TestStepTaskProgressBasic(t *testing.T) {
	conn := &Conn{
		transport: &mockTransport{sendFn: func(data []byte) error { return nil }},
	}
	reporter := newProgressReporter(conn, "req-1")
	tree := newTaskTree(reporter)
	ctx := withTaskTree(context.Background(), tree)

	err := SubTask(ctx, "Importing", func(ctx context.Context) error {
		TaskProgress(ctx, 0, 3)
		StepTaskProgress(ctx, 1)
		StepTaskProgress(ctx, 1)
		StepTaskProgress(ctx, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := tree.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap))
	}
	if snap[0].Current != 3 || snap[0].Total != 3 {
		t.Errorf("expected 3/3, got %d/%d", snap[0].Current, snap[0].Total)
	}
}

func TestTaskProgressNoContext(t *testing.T) {
	// Should not panic when there's no tree or shared context.
	TaskProgress(context.Background(), 5, 10)
	StepTaskProgress(context.Background(), 1)
}

// mockTransport for testing task tree without a real WebSocket.
type mockTransport struct {
	sendFn func(data []byte) error
}

func (m *mockTransport) Send(data []byte) error {
	if m.sendFn != nil {
		return m.sendFn(data)
	}
	return nil
}

func (m *mockTransport) Close() error           { return nil }
func (m *mockTransport) CloseGracefully() error { return nil }

// unmarshalJSON is a test helper to unmarshal JSON.
func unmarshalJSON(data []byte, v any) {
	json.Unmarshal(data, v)
}
