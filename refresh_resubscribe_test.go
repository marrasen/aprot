package aprot

import (
	"context"
	"testing"
	"time"
)

// Reverse of TestRefreshUnwindDoesNotLeakResubscribe: the client's re-subscribe
// R is in flight when a server-driven refresh F of the same subscription comes
// due. F must not cancel R (the client would get a CodeCanceled error for a
// subscription it never cancelled, and R's params would be lost). Instead R
// runs one refresh when it ends, so the change behind F still reaches the
// client.
func TestRefreshDoesNotCancelInflightResubscribe(t *testing.T) {
	s, c, h, rt := newRegistrationConn(t)

	c.handleIncomingMessage([]byte(`{"type":"subscribe","id":"s","method":"registrationHandlers.List"}`))
	waitSignal(t, h.started, "start the subscribe")
	h.gate <- struct{}{}
	waitSignal(t, h.finished, "finish the subscribe")
	waitIdle(t, s)

	// Re-subscribe R with the same ID blocks in the handler.
	c.handleIncomingMessage([]byte(`{"type":"subscribe","id":"s","method":"registrationHandlers.List"}`))
	waitSignal(t, h.started, "start the re-subscribe")

	// A refresh F of the same subscription comes due. It is deferred, not
	// run alongside R, and R is not canceled.
	s.TriggerRefresh("k")
	time.Sleep(100 * time.Millisecond)
	select {
	case <-h.started:
		t.Fatal("the refresh ran alongside the in-flight re-subscribe")
	case <-h.finished:
		t.Fatal("the in-flight re-subscribe was canceled by the refresh")
	default:
	}

	// R completes, then runs the deferred refresh.
	close(h.gate)
	waitSignal(t, h.finished, "finish the re-subscribe")
	waitSignal(t, h.started, "run the deferred refresh")
	waitIdle(t, s)

	for _, f := range errorFrames(t, rt) {
		t.Errorf("client got error frame %+v for a subscription it never cancelled", f)
	}
	responses := 0
	for _, m := range rt.Messages() {
		var f struct {
			Type string `json:"type"`
		}
		if err := unmarshalJSON(m, &f); err == nil && f.Type == string(TypeResponse) {
			responses++
		}
	}
	if responses != 3 {
		t.Fatalf("got %d responses, want 3 (subscribe, re-subscribe, deferred refresh)", responses)
	}
}

// A byte-stream peer that ends lines with CRLF (common on Windows pipes) must
// be able to send a frame of exactly MaxMessageSize.
func TestServeStreamCRLFFrameAtLimit(t *testing.T) {
	const limit = 512
	server := newStreamTestServer(t, ServerOptions{MaxMessageSize: limit})
	clientEnd, errCh := startStreamServer(context.Background(), server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)
	client.readFrameOfType("config")

	fits := append(echoFrameOfSize(t, "1", limit), '\r', '\n')
	go func() { _, _ = clientEnd.Write(fits) }()
	select {
	case <-errCh:
		t.Fatal("a CRLF-terminated frame of exactly MaxMessageSize closed the connection")
	case <-time.After(500 * time.Millisecond):
	}
}
