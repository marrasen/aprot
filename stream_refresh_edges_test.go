package aprot

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Boundary table for the stream transport's line limit.
func TestServeStreamLimitBoundaryTable(t *testing.T) {
	const limit = 512
	cases := []struct {
		size   int
		term   string
		accept bool
	}{
		{limit - 1, "\n", true},
		{limit, "\n", true},
		{limit + 1, "\n", false},
		{limit + 2, "\n", false},
		{limit - 1, "\r\n", true},
		{limit, "\r\n", true},
		{limit + 1, "\r\n", false},
		{limit + 2, "\r\n", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d%q", tc.size, tc.term), func(t *testing.T) {
			server := newStreamTestServer(t, ServerOptions{MaxMessageSize: limit})
			clientEnd, errCh := startStreamServer(context.Background(), server, ConnInfo{})
			defer clientEnd.Close()
			client := newStreamTestClient(t, clientEnd)
			client.readFrameOfType("config")
			frame := append(echoFrameOfSize(t, "1", tc.size), tc.term...)
			go func() { _, _ = clientEnd.Write(frame) }()
			if tc.accept {
				if resp := client.readFrameOfType("response"); resp["id"] != "1" {
					t.Fatalf("resp %v", resp)
				}
				return
			}
			select {
			case <-errCh:
			case <-time.After(3 * time.Second):
				t.Fatal("not closed")
			}
		})
	}
}

// A client request that happens to share a subscription's ID: a refresh
// that comes due while it runs is deferred onto it, and handleRequest never
// runs the deferred refresh.
func TestRefreshDeferredOntoSameIDRequestRuns(t *testing.T) {
	s, c, h, _ := newRegistrationConn(t)

	c.handleIncomingMessage([]byte(`{"type":"subscribe","id":"s","method":"registrationHandlers.List"}`))
	waitSignal(t, h.started, "start the subscribe")
	h.gate <- struct{}{}
	waitSignal(t, h.finished, "finish the subscribe")
	waitIdle(t, s)

	c.handleIncomingMessage([]byte(`{"type":"request","id":"s","method":"registrationHandlers.Slow"}`))
	waitSignal(t, h.started, "start the request")

	s.TriggerRefresh("k")
	time.Sleep(100 * time.Millisecond)

	h.gate <- struct{}{} // finish the request
	waitSignal(t, h.finished, "finish the request")

	select {
	case who := <-h.started:
		// refresh ran
		h.gate <- struct{}{}
		_ = who
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the refresh deferred onto a same-ID request never ran")
	}
	waitIdle(t, s)
}
