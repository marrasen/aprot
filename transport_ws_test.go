package aprot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newWSPair returns a connected server-side and client-side *websocket.Conn
// backed by an httptest server. Both ends are closed via t.Cleanup.
func newWSPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	serverCh := make(chan *websocket.Conn, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		serverCh <- ws
	}))
	t.Cleanup(ts.Close)

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	s := <-serverCh
	t.Cleanup(func() {
		_ = s.Close()
		_ = c.Close()
	})
	return s, c
}

// Frames enqueued before Close must still reach the peer. handleAuth (and the
// auth-timeout path) send auth_error and then immediately close the
// connection; Send only enqueues onto the transport's buffered channel, so
// writePump must drain the queue when it observes done — otherwise the select
// over done/send can exit first and the peer sees an abnormal close (1006)
// without ever receiving the frame.
func TestWSTransport_CloseDeliversQueuedFrames(t *testing.T) {
	for i := 0; i < 30; i++ {
		server, client := newWSPair(t)
		tr := newWSTransport(server, ServerOptions{})

		// Enqueue, then close, before the pump runs — the ordering every
		// send-then-close caller (e.g. handleAuth) produces, with the pump
		// scheduling squeezed to its worst case.
		if err := tr.Send([]byte(`{"type":"auth_error","message":"nope"}`)); err != nil {
			t.Fatalf("iteration %d: send: %v", i, err)
		}
		_ = tr.Close()
		go tr.writePump()

		_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("iteration %d: queued frame dropped at close: %v", i, err)
		}
		if !strings.Contains(string(data), "auth_error") {
			t.Fatalf("iteration %d: unexpected frame: %s", i, data)
		}
	}
}
