package tasks

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
)

type outputBurstHandler struct {
	cause chan error
}

// bufferFullObserver counts the sends that found the send buffer full.
type bufferFullObserver struct {
	aprot.NoopObserver
	full atomic.Int64
}

func (o *bufferFullObserver) SendBufferFull(*aprot.Conn) { o.full.Add(1) }

// Run starts a shared task and writes more output than the socket buffers and
// a connection's send buffer hold together, without pausing.
func (h *outputBurstHandler) Run(ctx context.Context) error {
	ctx, task := StartTask[any](ctx, "burst", Shared())
	line := strings.Repeat("x", 4096)
	for i := 0; i < 5000; i++ {
		task.Output(line)
	}
	h.cause <- context.Cause(ctx)
	task.Close()
	return nil
}

// A shared task's output is broadcast to every client. A burst that outruns
// the socket fills the caller's send buffer; the broadcast waits for room and
// the caller stays connected and gets its response. The client starts reading
// only once the buffer is full, so the test does not depend on the server
// outrunning the socket.
func TestSharedTaskOutputBurstKeepsCallerConnected(t *testing.T) {
	registry := aprot.NewRegistry()
	handler := &outputBurstHandler{cause: make(chan error, 1)}
	registry.Register(handler)
	Enable(registry)
	obs := &bufferFullObserver{}
	// A long PushTimeout keeps a slow test machine from closing the connection
	// in the moment between the buffer filling and the client starting to read.
	server := aprot.NewServer(registry, aprot.ServerOptions{Observer: obs, PushTimeout: 30 * time.Second})
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
		// Read nothing until the burst has filled the send buffer.
		for deadline := time.Now().Add(30 * time.Second); obs.full.Load() == 0 && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
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
	if obs.full.Load() == 0 {
		t.Error("the send buffer never filled")
	}
}
