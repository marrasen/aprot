package tasks

import (
	"io"
	"time"

	"github.com/marrasen/aprot"
)

// taskOutputWriter implements io.WriteCloser and sends written data as output
// attached to a specific task node via the request-scoped progress channel.
type taskOutputWriter struct {
	tree *taskTree
	node *taskNode
}

func (w *taskOutputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s := string(p)
		w.tree.sender.SendJSON(aprot.ProgressMessage{
			Type:   aprot.TypeProgress,
			ID:     w.tree.sender.RequestID(),
			TaskID: w.node.id,
			Output: &s,
		})
	}
	return len(p), nil
}

func (w *taskOutputWriter) Close() error {
	w.node.setStatus(TaskNodeStatusCompleted)
	w.tree.send()
	return nil
}

// sharedOutputWriter implements io.WriteCloser for output within the shared
// task system.
type sharedOutputWriter struct {
	core *sharedTaskCore
	node *sharedTaskNode
}

func (w *sharedOutputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s := string(p)
		w.core.manager.sendUpdate(w.node.id, &s, nil, nil)
	}
	return len(p), nil
}

func (w *sharedOutputWriter) Close() error {
	w.node.mu.Lock()
	w.node.status = TaskNodeStatusCompleted
	w.node.mu.Unlock()
	w.core.manager.broadcastNow()
	return nil
}

// taskProgressWriter implements io.WriteCloser and tracks bytes as progress
// via the request-scoped progress channel.
type taskProgressWriter struct {
	tree     *taskTree
	node     *taskNode
	written  int
	total    int
	lastSend time.Time
}

func (w *taskProgressWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	w.node.setProgress(w.written, w.total)

	if time.Since(w.lastSend) >= 100*time.Millisecond {
		current := w.written
		total := w.total
		w.tree.sender.SendJSON(aprot.ProgressMessage{
			Type:    aprot.TypeProgress,
			ID:      w.tree.sender.RequestID(),
			TaskID:  w.node.id,
			Current: &current,
			Total:   &total,
		})
		w.lastSend = time.Now()
	}

	return len(p), nil
}

func (w *taskProgressWriter) Close() error {
	w.node.setProgress(w.written, w.total)
	w.node.setStatus(TaskNodeStatusCompleted)
	w.tree.send()
	return nil
}

// sharedProgressWriter implements io.WriteCloser for byte-tracking progress
// within the shared task system.
type sharedProgressWriter struct {
	core     *sharedTaskCore
	node     *sharedTaskNode
	written  int
	total    int
	lastSend time.Time
}

func (w *sharedProgressWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	w.node.mu.Lock()
	w.node.current = w.written
	w.node.total = w.total
	w.node.mu.Unlock()

	if time.Since(w.lastSend) >= 100*time.Millisecond {
		current := w.written
		total := w.total
		w.core.manager.sendUpdate(w.node.id, nil, &current, &total)
		w.lastSend = time.Now()
	}

	return len(p), nil
}

func (w *sharedProgressWriter) Close() error {
	w.node.mu.Lock()
	w.node.current = w.written
	w.node.total = w.total
	w.node.status = TaskNodeStatusCompleted
	w.node.mu.Unlock()
	w.core.manager.broadcastNow()
	return nil
}

// discardWriteCloser is a no-op WriteCloser for when no task tree is present.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                 { return nil }

// Ensure interface compliance at compile time.
var (
	_ io.WriteCloser = (*taskOutputWriter)(nil)
	_ io.WriteCloser = (*sharedOutputWriter)(nil)
	_ io.WriteCloser = (*taskProgressWriter)(nil)
	_ io.WriteCloser = (*sharedProgressWriter)(nil)
	_ io.WriteCloser = discardWriteCloser{}
)
