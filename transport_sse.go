package aprot

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/go-json-experiment/json"
)

// sseTransport wraps an http.ResponseWriter for SSE output.
type sseTransport struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
	closed  bool
	done    chan struct{} // closed when the SSE stream ends
}

func newSSETransport(w http.ResponseWriter, flusher http.Flusher) *sseTransport {
	return &sseTransport{
		w:       w,
		flusher: flusher,
		done:    make(chan struct{}),
	}
}

func (t *sseTransport) Send(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}

	// Extract message type for SSE event field
	eventType := extractMessageType(data)

	if eventType != "" {
		fmt.Fprintf(t.w, "event: %s\n", eventType)
	}
	fmt.Fprintf(t.w, "data: %s\n\n", data)
	t.flusher.Flush()
	return nil
}

func (t *sseTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.closed = true
		close(t.done)
	}
	return nil
}

// sendEvent sends a named SSE event with JSON data directly (not through the transport interface).
func (t *sseTransport) sendEvent(event string, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	fmt.Fprintf(t.w, "event: %s\n", event)
	fmt.Fprintf(t.w, "data: %s\n\n", data)
	t.flusher.Flush()
}

// sendComment sends an SSE comment (used for keep-alive).
func (t *sseTransport) sendComment(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	fmt.Fprintf(t.w, ": %s\n\n", text)
	t.flusher.Flush()
}

// extractMessageType extracts the "type" field from a JSON message.
func extractMessageType(data []byte) string {
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return ""
	}
	return peek.Type
}
