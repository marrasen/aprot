package tasks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-json-experiment/json"
	"github.com/marrasen/aprot"
)

// capturedMessage is a test-only superset of all progress-like fields so that
// the mock sender can capture any message type (tree snapshot, output, or
// node-level progress) in a single struct.
type capturedMessage struct {
	Type    string      `json:"type"`
	ID      string      `json:"id"`
	Tasks   []*TaskNode `json:"tasks,omitempty"`
	TaskID  string      `json:"taskId,omitempty"`
	Output  *string     `json:"output,omitempty"`
	Current *int        `json:"current,omitempty"`
	Total   *int        `json:"total,omitempty"`
	Message string      `json:"message,omitempty"`
}

// mockSender is a test double for aprot.RequestSender that records every
// message passed to SendJSON.
type mockSender struct {
	captured []capturedMessage
	mu       sync.Mutex
}

func (m *mockSender) SendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var msg capturedMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	m.mu.Lock()
	m.captured = append(m.captured, msg)
	m.mu.Unlock()
	return nil
}

func (m *mockSender) RequestID() string { return "req-1" }

func (m *mockSender) messages() []capturedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]capturedMessage, len(m.captured))
	copy(out, m.captured)
	return out
}

// extractTasks returns the Tasks field from a captured message.
func extractTasks(t *testing.T, msg capturedMessage) []*TaskNode {
	t.Helper()
	return msg.Tasks
}

// newTestCtx returns a context that has a task tree backed by sender.
func newTestCtx(sender aprot.RequestSender) context.Context {
	tree := newTaskTree(sender)
	return withTaskTree(context.Background(), tree)
}

// --- SubTask ---

// TestSubTaskBasic verifies that SubTask without a task tree simply calls fn
// and returns its result, without panicking or sending any messages.
func TestSubTaskBasic(t *testing.T) {
	ran := false
	err := SubTask(context.Background(), "plain", func(ctx context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !ran {
		t.Error("expected fn to run")
	}
}

// TestSubTaskWithTree verifies that SubTask with a tree emits two progress
// messages: one with the task in "created" status and one in "completed".
func TestSubTaskWithTree(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	err := SubTask(ctx, "my task", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := sender.messages()
	// Expect at least two messages: created then completed.
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(msgs))
	}

	// First message should show the task as "created".
	firstNodes := extractTasks(t, msgs[0])
	if len(firstNodes) == 0 {
		t.Fatal("first message has no task nodes")
	}
	if firstNodes[0].Status != TaskNodeStatusCreated {
		t.Errorf("first message: expected status %q, got %q", TaskNodeStatusCreated, firstNodes[0].Status)
	}
	if firstNodes[0].Title != "my task" {
		t.Errorf("first message: expected title %q, got %q", "my task", firstNodes[0].Title)
	}

	// Last message should show the task as "completed".
	lastNodes := extractTasks(t, msgs[len(msgs)-1])
	if len(lastNodes) == 0 {
		t.Fatal("last message has no task nodes")
	}
	if lastNodes[0].Status != TaskNodeStatusCompleted {
		t.Errorf("last message: expected status %q, got %q", TaskNodeStatusCompleted, lastNodes[0].Status)
	}
}

// TestSubTaskError verifies that when fn returns an error, the task is marked
// "failed" and the error message is propagated.
func TestSubTaskError(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	boom := errors.New("something went wrong")
	err := SubTask(ctx, "failing task", func(ctx context.Context) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected original error returned, got %v", err)
	}

	msgs := sender.messages()
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(msgs))
	}

	lastNodes := extractTasks(t, msgs[len(msgs)-1])
	if len(lastNodes) == 0 {
		t.Fatal("last message has no task nodes")
	}
	if lastNodes[0].Status != TaskNodeStatusFailed {
		t.Errorf("expected status %q, got %q", TaskNodeStatusFailed, lastNodes[0].Status)
	}
	if !strings.Contains(lastNodes[0].Error, "something went wrong") {
		t.Errorf("expected error field to contain %q, got %q", "something went wrong", lastNodes[0].Error)
	}
}

// TestSubTaskNested verifies that nested SubTask calls are reflected as child
// nodes in the snapshot.
func TestSubTaskNested(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	err := SubTask(ctx, "parent", func(parentCtx context.Context) error {
		return SubTask(parentCtx, "child", func(childCtx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find a message that has both parent and child nodes.
	var foundNested bool
	for _, msg := range sender.messages() {
		nodes := extractTasks(t, msg)
		if len(nodes) == 0 {
			continue
		}
		if len(nodes[0].Children) > 0 {
			foundNested = true
			child := nodes[0].Children[0]
			if child.Title != "child" {
				t.Errorf("expected child title %q, got %q", "child", child.Title)
			}
		}
	}
	if !foundNested {
		t.Error("expected at least one message to contain a nested child node")
	}
}

// --- Output ---

// TestOutput verifies that Output sends a progress message with Output set and
// the correct TaskID when inside a task node context.
func TestOutput(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	// Run inside a SubTask so a node is on the context.
	_ = SubTask(ctx, "the task", func(innerCtx context.Context) error {
		Output(innerCtx, "hello world")
		return nil
	})

	var found bool
	for _, msg := range sender.messages() {
		if msg.Output != nil && *msg.Output == "hello world" {
			found = true
			if msg.TaskID == "" {
				t.Error("expected TaskID to be set on output message")
			}
		}
	}
	if !found {
		t.Error("expected an output message with text 'hello world'")
	}
}

// TestOutputWithoutTree verifies that Output is a no-op when there is no task
// tree on the context (no panic, no messages sent).
func TestOutputWithoutTree(t *testing.T) {
	// Output on a bare context (no tree, no shared context) should be a
	// silent no-op — verify it does not panic.
	Output(context.Background(), "silent")
}

// --- OutputWriter ---

// TestOutputWriter verifies that writes are forwarded as output messages and
// that Close marks the task node completed.
func TestOutputWriter(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	w := OutputWriter(ctx, "log writer")

	n, err := w.Write([]byte("line one\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 9 {
		t.Errorf("expected 9 bytes written, got %d", n)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Verify an output message was sent.
	var outputFound bool
	for _, msg := range sender.messages() {
		if msg.Output != nil && *msg.Output == "line one\n" {
			outputFound = true
		}
	}
	if !outputFound {
		t.Error("expected an output message containing 'line one\\n'")
	}

	// Verify the final snapshot shows the task as completed.
	tree := taskTreeFromContext(ctx)
	snapshot := tree.snapshot()
	if len(snapshot) == 0 {
		t.Fatal("expected at least one node in snapshot after Close")
	}
	if snapshot[0].Status != TaskNodeStatusCompleted {
		t.Errorf("expected node status %q after Close, got %q", TaskNodeStatusCompleted, snapshot[0].Status)
	}
}

// TestOutputWriterWithoutTree verifies that OutputWriter returns a functional
// discard writer when no task tree is present.
func TestOutputWriterWithoutTree(t *testing.T) {
	w := OutputWriter(context.Background(), "discard")
	n, err := w.Write([]byte("data"))
	if err != nil {
		t.Errorf("Write on discard writer: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4, got %d", n)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close on discard writer: %v", err)
	}
}

// --- WriterProgress ---

// TestWriterProgress verifies that closing the writer marks the task completed
// and that the final snapshot reflects the total bytes as current progress.
func TestWriterProgress(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	const content = "hello bytes"
	const size = len(content)

	w := WriterProgress(ctx, "download", size)

	n, err := w.Write([]byte(content))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != size {
		t.Errorf("expected %d bytes written, got %d", size, n)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	tree := taskTreeFromContext(ctx)
	snapshot := tree.snapshot()
	if len(snapshot) == 0 {
		t.Fatal("expected at least one node in snapshot after Close")
	}
	node := snapshot[0]
	if node.Status != TaskNodeStatusCompleted {
		t.Errorf("expected status %q after Close, got %q", TaskNodeStatusCompleted, node.Status)
	}
	if node.Current != size {
		t.Errorf("expected Current=%d, got %d", size, node.Current)
	}
	if node.Total != size {
		t.Errorf("expected Total=%d, got %d", size, node.Total)
	}
}

// TestWriterProgressWithoutTree verifies that WriterProgress returns a
// functional discard writer when no task tree is present.
func TestWriterProgressWithoutTree(t *testing.T) {
	w := WriterProgress(context.Background(), "discard", 100)
	n, err := w.Write([]byte("data"))
	if err != nil {
		t.Errorf("Write on discard writer: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4, got %d", n)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close on discard writer: %v", err)
	}
}

// --- Task / StartTask ---

// TestTaskErr verifies Task.Err behaviour: nil error completes the task;
// non-nil error fails the task with the error message.
func TestTaskErr(t *testing.T) {
	t.Run("nil error completes task", func(t *testing.T) {
		sender := &mockSender{}
		ctx := newTestCtx(sender)

		_, task := StartTask[any](ctx, "op")
		if task == nil {
			t.Fatal("expected non-nil task")
		}
		task.Err(nil)

		tree := taskTreeFromContext(ctx)
		snapshot := tree.snapshot()
		if len(snapshot) == 0 {
			t.Fatal("expected at least one node in snapshot")
		}
		if snapshot[0].Status != TaskNodeStatusCompleted {
			t.Errorf("expected status %q, got %q", TaskNodeStatusCompleted, snapshot[0].Status)
		}
	})

	t.Run("non-nil error fails task", func(t *testing.T) {
		sender := &mockSender{}
		ctx := newTestCtx(sender)

		_, task := StartTask[any](ctx, "op")
		if task == nil {
			t.Fatal("expected non-nil task")
		}
		task.Err(errors.New("oh no"))

		tree := taskTreeFromContext(ctx)
		snapshot := tree.snapshot()
		if len(snapshot) == 0 {
			t.Fatal("expected at least one node in snapshot")
		}
		if snapshot[0].Status != TaskNodeStatusFailed {
			t.Errorf("expected status %q, got %q", TaskNodeStatusFailed, snapshot[0].Status)
		}
		if !strings.Contains(snapshot[0].Error, "oh no") {
			t.Errorf("expected error field to contain %q, got %q", "oh no", snapshot[0].Error)
		}
	})
}

// --- TaskProgress ---

// TestTaskProgressBasic verifies that TaskProgress sets Current and Total on
// the node and sends a targeted progress message.
func TestTaskProgressBasic(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	_ = SubTask(ctx, "download", func(innerCtx context.Context) error {
		TaskProgress(innerCtx, 3, 10)
		return nil
	})

	// Confirm a message with Current=3, Total=10 was sent.
	var found bool
	for _, msg := range sender.messages() {
		if msg.Current != nil && msg.Total != nil && *msg.Current == 3 && *msg.Total == 10 {
			found = true
			if msg.TaskID == "" {
				t.Error("expected TaskID to be set on progress message")
			}
		}
	}
	if !found {
		t.Error("expected a progress message with Current=3, Total=10")
	}
}

// TestStepTaskProgressBasic verifies that StepTaskProgress increments the
// current progress counter and sends an update.
func TestStepTaskProgressBasic(t *testing.T) {
	sender := &mockSender{}
	ctx := newTestCtx(sender)

	_ = SubTask(ctx, "work", func(innerCtx context.Context) error {
		TaskProgress(innerCtx, 0, 5)
		StepTaskProgress(innerCtx, 2)
		StepTaskProgress(innerCtx, 1)
		return nil
	})

	// After two steps the current value should be 3.
	var maxCurrent int
	for _, msg := range sender.messages() {
		if msg.Current != nil && *msg.Current > maxCurrent {
			maxCurrent = *msg.Current
		}
	}
	if maxCurrent != 3 {
		t.Errorf("expected max Current=3 after stepping, got %d", maxCurrent)
	}
}

// TestTaskProgressNoContext verifies that TaskProgress and StepTaskProgress do
// not panic when there is no task node on the context.
func TestTaskProgressNoContext(t *testing.T) {
	// Should not panic.
	TaskProgress(context.Background(), 1, 10)
	StepTaskProgress(context.Background(), 1)
}
