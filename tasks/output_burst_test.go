package tasks

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
)

type outputBurstHandler struct {
	cause chan error
}

// Run starts a shared task and writes more output lines than a connection's
// send buffer holds, without pausing.
func (h *outputBurstHandler) Run(ctx context.Context) error {
	ctx, task := StartTask[any](ctx, "burst", Shared())
	for i := 0; i < 5000; i++ {
		task.Output("one line of output")
	}
	h.cause <- context.Cause(ctx)
	task.Close()
	return nil
}

// A shared task's output is broadcast to every client. A burst that outruns
// the socket fills the caller's send buffer; the broadcast waits for room and
// the caller stays connected and gets its response.
func TestSharedTaskOutputBurstKeepsCallerConnected(t *testing.T) {
	registry := aprot.NewRegistry()
	handler := &outputBurstHandler{cause: make(chan error, 1)}
	registry.Register(handler)
	Enable(registry)
	server := aprot.NewServer(registry)
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	readErr := make(chan error, 1)
	gotResponse := make(chan struct{}, 1)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			if strings.Contains(string(data), `"type":"response"`) {
				gotResponse <- struct{}{}
				return
			}
		}
	}()

	request := `{"type":"request","id":"1","method":"outputBurstHandler.Run","params":[]}`
	if err := ws.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case cause := <-handler.cause:
		if cause != nil {
			t.Fatalf("the request was canceled during the output burst: %v", cause)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the handler did not finish")
	}
	select {
	case <-gotResponse:
	case err := <-readErr:
		t.Fatalf("the caller's socket failed before the response: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the caller got no response")
	}
}
