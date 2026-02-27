package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-json-experiment/json"
	"github.com/marrasen/aprot"
)

// capturedMessage is a test-only superset of all push event fields so that
// we can capture any message type (tree snapshot, output, or progress) in a
// single struct.
type capturedMessage struct {
	// Push envelope
	Type  string `json:"type"`
	Event string `json:"event"`

	// Embedded data — we decode the .data field into these.
	RequestID string      `json:"requestId,omitempty"`
	Tasks     []*TaskNode `json:"tasks,omitempty"`
	TaskID    string      `json:"taskId,omitempty"`
	Output    *string     `json:"output,omitempty"`
	Current   *int        `json:"current,omitempty"`
	Total     *int        `json:"total,omitempty"`
}

// parsePushMessages decodes raw JSON messages from a TestPushConn into
// capturedMessage structs. It only parses push messages (type=push) and
// extracts fields from the data envelope.
func parsePushMessages(raw [][]byte) []capturedMessage {
	var msgs []capturedMessage
	for _, b := range raw {
		// First pass: extract the envelope type and event.
		var envelope struct {
			Type  string `json:"type"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(b, &envelope); err != nil {
			continue
		}
		if envelope.Type != "push" {
			continue
		}
		// Second pass: extract the data object. We marshal the whole push
		// message into a map so we can reach the nested data.
		var full map[string]any
		if err := json.Unmarshal(b, &full); err != nil {
			continue
		}
		dataRaw, ok := full["data"]
		if !ok {
			continue
		}
		// Re-encode data and decode into capturedMessage to get typed fields.
		dataBytes, err := json.Marshal(dataRaw)
		if err != nil {
			continue
		}
		msg := capturedMessage{Type: envelope.Type, Event: envelope.Event}
		json.Unmarshal(dataBytes, &msg)
		msgs = append(msgs, msg)
	}
	return msgs
}

// newTestPushConn creates a TestPushConn with the request task push events
// registered.
func newTestPushConn() *aprot.TestPushConn {
	return aprot.NewTestPushConn(1,
		RequestTaskTreeEvent{},
		RequestTaskOutputEvent{},
		RequestTaskProgressEvent{},
	)
}

// newTestCtx returns a context that has a request delivery backed by a
// TestPushConn.
func newTestCtx(tc *aprot.TestPushConn) context.Context {
	d := newRequestDelivery(tc.Conn, "req-1")
	return withDelivery(context.Background(), d)
}

// extractTasks returns the Tasks field from a captured message.
func extractTasks(t *testing.T, msg capturedMessage) []*TaskNode {
	t.Helper()
	return msg.Tasks
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

	err := SubTask(ctx, "my task", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := parsePushMessages(tc.Messages())
	// Filter for tree events only.
	var treeMsgs []capturedMessage
	for _, m := range msgs {
		if m.Event == "RequestTaskTreeEvent" {
			treeMsgs = append(treeMsgs, m)
		}
	}
	// Expect at least two tree messages: created then completed.
	if len(treeMsgs) < 2 {
		t.Fatalf("expected >= 2 tree messages, got %d", len(treeMsgs))
	}

	// First message should show the task as "created".
	firstNodes := extractTasks(t, treeMsgs[0])
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
	lastNodes := extractTasks(t, treeMsgs[len(treeMsgs)-1])
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

	boom := errors.New("something went wrong")
	err := SubTask(ctx, "failing task", func(ctx context.Context) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected original error returned, got %v", err)
	}

	msgs := parsePushMessages(tc.Messages())
	var treeMsgs []capturedMessage
	for _, m := range msgs {
		if m.Event == "RequestTaskTreeEvent" {
			treeMsgs = append(treeMsgs, m)
		}
	}
	if len(treeMsgs) < 2 {
		t.Fatalf("expected >= 2 tree messages, got %d", len(treeMsgs))
	}

	lastNodes := extractTasks(t, treeMsgs[len(treeMsgs)-1])
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

	err := SubTask(ctx, "parent", func(parentCtx context.Context) error {
		return SubTask(parentCtx, "child", func(childCtx context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find a tree message that has both parent and child nodes.
	var foundNested bool
	for _, msg := range parsePushMessages(tc.Messages()) {
		if msg.Event != "RequestTaskTreeEvent" {
			continue
		}
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

// TestOutput verifies that Output sends a push event with output set and
// the correct TaskID when inside a task node context.
func TestOutput(t *testing.T) {
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

	// Run inside a SubTask so a node is on the context.
	_ = SubTask(ctx, "the task", func(innerCtx context.Context) error {
		Output(innerCtx, "hello world")
		return nil
	})

	var found bool
	for _, msg := range parsePushMessages(tc.Messages()) {
		if msg.Event == "RequestTaskOutputEvent" && msg.Output != nil && *msg.Output == "hello world" {
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

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
	for _, msg := range parsePushMessages(tc.Messages()) {
		if msg.Event == "RequestTaskOutputEvent" && msg.Output != nil && *msg.Output == "line one\n" {
			outputFound = true
		}
	}
	if !outputFound {
		t.Error("expected an output message containing 'line one\\n'")
	}

	// Verify the final snapshot shows the task as completed.
	d := deliveryFromContext(ctx).(*requestDelivery)
	root := d.root
	snapshot := root.snapshotChildren()
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

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

	rd := deliveryFromContext(ctx).(*requestDelivery)
	snapshot := rd.root.snapshotChildren()
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
		tc := newTestPushConn()
		ctx := newTestCtx(tc)

		_, task := StartTask[any](ctx, "op")
		if task == nil {
			t.Fatal("expected non-nil task")
		}
		task.Err(nil)

		rd := deliveryFromContext(ctx).(*requestDelivery)
		snapshot := rd.root.snapshotChildren()
		if len(snapshot) == 0 {
			t.Fatal("expected at least one node in snapshot")
		}
		if snapshot[0].Status != TaskNodeStatusCompleted {
			t.Errorf("expected status %q, got %q", TaskNodeStatusCompleted, snapshot[0].Status)
		}
	})

	t.Run("non-nil error fails task", func(t *testing.T) {
		tc := newTestPushConn()
		ctx := newTestCtx(tc)

		_, task := StartTask[any](ctx, "op")
		if task == nil {
			t.Fatal("expected non-nil task")
		}
		task.Err(errors.New("oh no"))

		rd := deliveryFromContext(ctx).(*requestDelivery)
		snapshot := rd.root.snapshotChildren()
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

	_ = SubTask(ctx, "download", func(innerCtx context.Context) error {
		TaskProgress(innerCtx, 3, 10)
		return nil
	})

	// Confirm a push message with Current=3, Total=10 was sent.
	var found bool
	for _, msg := range parsePushMessages(tc.Messages()) {
		if msg.Event == "RequestTaskProgressEvent" && msg.Current != nil && msg.Total != nil && *msg.Current == 3 && *msg.Total == 10 {
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
	tc := newTestPushConn()
	ctx := newTestCtx(tc)

	_ = SubTask(ctx, "work", func(innerCtx context.Context) error {
		TaskProgress(innerCtx, 0, 5)
		StepTaskProgress(innerCtx, 2)
		StepTaskProgress(innerCtx, 1)
		return nil
	})

	// After two steps the current value should be 3.
	var maxCurrent int
	for _, msg := range parsePushMessages(tc.Messages()) {
		if msg.Event == "RequestTaskProgressEvent" && msg.Current != nil && *msg.Current > maxCurrent {
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
