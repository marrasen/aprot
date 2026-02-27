package tasks

import (
	"io"
	"time"
)

// outputWriter implements io.WriteCloser and sends written data as output
// attached to a specific task node via its delivery.
type outputWriter struct {
	node *taskNode
}

func (w *outputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.node.output(string(p))
	}
	return len(p), nil
}

func (w *outputWriter) Close() error {
	w.node.closeNode()
	return nil
}

// progressWriter implements io.WriteCloser and tracks bytes written as progress
// via the node's delivery.
type progressWriter struct {
	node     *taskNode
	written  int
	total    int
	lastSend time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	w.node.setProgress(w.written, w.total)

	if time.Since(w.lastSend) >= 100*time.Millisecond {
		w.node.delivery.sendProgress(w.node.id, w.written, w.total)
		w.lastSend = time.Now()
	}

	return len(p), nil
}

func (w *progressWriter) Close() error {
	w.node.setProgress(w.written, w.total)
	w.node.closeNode()
	return nil
}

// discardWriteCloser is a no-op WriteCloser for when no delivery is present.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                 { return nil }

// Ensure interface compliance at compile time.
var (
	_ io.WriteCloser = (*outputWriter)(nil)
	_ io.WriteCloser = (*progressWriter)(nil)
	_ io.WriteCloser = discardWriteCloser{}
)
