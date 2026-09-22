package aprot

import (
	"context"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// inflightHandlers park on channels the test controls, so a request can be held
// in flight for as long as the assertions need and then released.
type inflightHandlers struct {
	// block is closed to release every parked handler. Handlers select on it
	// rather than on a per-call channel so one close frees all of them.
	block chan struct{}
	// entered receives one value per handler entry, so a test can wait until
	// the handler is actually running instead of guessing.
	entered chan string
}

func (h *inflightHandlers) Park(ctx context.Context) (*EchoResponse, error) {
	h.entered <- "Park"
	select {
	case <-h.block:
	case <-ctx.Done():
	}
	return &EchoResponse{Message: "parked"}, nil
}

// Stuck models the bug the visibility is for: a handler that ignores its
// context and blocks forever. Only closing block frees it.
func (h *inflightHandlers) Stuck(ctx context.Context) (*EchoResponse, error) {
	h.entered <- "Stuck"
	<-h.block
	return &EchoResponse{Message: "unstuck"}, nil
}

func (h *inflightHandlers) Fast(ctx context.Context) (*EchoResponse, error) {
	return &EchoResponse{Message: "fast"}, nil
}

// ParkWatch is a subscribe target: it registers a trigger key and parks, so a
// subscribe first-run and a server-driven refresh both show up in flight.
func (h *inflightHandlers) ParkWatch(ctx context.Context) (*EchoResponse, error) {
	RegisterRefreshTrigger(ctx, "inflight-watched")
	h.entered <- "ParkWatch"
	select {
	case <-h.block:
	case <-ctx.Done():
	}
	return &EchoResponse{Message: "watched"}, nil
}

func setupInflightServer(t *testing.T) (*httptest.Server, *Server, *inflightHandlers) {
	t.Helper()
	h := &inflightHandlers{
		block:   make(chan struct{}),
		entered: make(chan string, 64),
	}
	registry := NewRegistry()
	registry.Register(h)
	server := NewServer(registry, ServerOptions{})
	ts := httptest.NewServer(server)
	// Release every parked handler before the httptest server is torn down,
	// otherwise Close blocks on the handler goroutines.
	t.Cleanup(func() {
		select {
		case <-h.block:
		default:
			close(h.block)
		}
		ts.Close()
	})
	return ts, server, h
}

// waitEntered blocks until the named handler reports that it started running.
func waitEntered(t *testing.T, h *inflightHandlers, want string) {
	t.Helper()
	select {
	case got := <-h.entered:
		if got != want {
			t.Fatalf("handler entered = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("handler %q never started", want)
	}
}

// A running request is counted while it runs and gone once it returns. Without
// this, ServerStats reports connections and subscriptions but says nothing
// about the one structure whose size depends on application behaviour. (#374)
func TestInFlightRequests_CountedWhileRunning(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if st := server.Stats(); st.InFlightRequests != 0 || st.OldestRequestAge != 0 {
		t.Fatalf("Stats before any request = %+v, want InFlightRequests=0 OldestRequestAge=0", st)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "p1", Method: "inflightHandlers.Park"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Park")

	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 1 })

	// The age is measured from dispatch, so it grows while the handler runs.
	eventually(t, 3*time.Second, func() bool { return server.Stats().OldestRequestAge > 0 })

	close(h.block)
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)

	eventually(t, 3*time.Second, func() bool {
		st := server.Stats()
		return st.InFlightRequests == 0 && st.OldestRequestAge == 0
	})
}

// The snapshot names the method, which a bare count cannot. This is what turns
// "something is stuck" into "inflightHandlers.Stuck is stuck". (#374)
func TestInFlightRequests_SnapshotNamesTheMethod(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if got := server.InFlightRequests(); len(got) != 0 {
		t.Fatalf("InFlightRequests before any request = %+v, want empty", got)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "s1", Method: "inflightHandlers.Stuck"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Stuck")
	eventually(t, 3*time.Second, func() bool { return len(server.InFlightRequests()) == 1 })

	got := server.InFlightRequests()[0]
	if got.Method != "inflightHandlers.Stuck" {
		t.Errorf("Method = %q, want %q", got.Method, "inflightHandlers.Stuck")
	}
	if got.RequestID != "s1" {
		t.Errorf("RequestID = %q, want %q", got.RequestID, "s1")
	}
	if got.Subscribe {
		t.Error("Subscribe = true, want false for a one-shot request")
	}
	if got.Age <= 0 {
		t.Errorf("Age = %v, want > 0", got.Age)
	}
	if got.ConnID == 0 {
		t.Error("ConnID = 0, want the ID of the connection the request arrived on")
	}
}

// The case from the issue: a handler that never returns and never watches its
// context. unregisterRequest runs from a defer as the handler unwinds, so a
// handler that never unwinds keeps its entry — which is exactly why the count
// is the signal. Its goroutine stays parked and the count stays up. (#374)
func TestInFlightRequests_StuckHandlerStaysVisible(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "s1", Method: "inflightHandlers.Stuck"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Stuck")
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 1 })

	first := server.Stats().OldestRequestAge

	// Other requests come and go without clearing the stuck one.
	for _, id := range []string{"f1", "f2", "f3"} {
		if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: id, Method: "inflightHandlers.Fast"}); err != nil {
			t.Fatalf("write: %v", err)
		}
		readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	}

	eventually(t, 3*time.Second, func() bool { return server.Stats().OldestRequestAge > first })
	if st := server.Stats(); st.InFlightRequests != 1 {
		t.Errorf("InFlightRequests after three completed requests = %d, want 1 (the stuck one)", st.InFlightRequests)
	}
	if got := server.InFlightRequests(); len(got) != 1 || got[0].Method != "inflightHandlers.Stuck" {
		t.Errorf("InFlightRequests = %+v, want just inflightHandlers.Stuck", got)
	}
}

// A subscribe first-run is in flight like any other execution, and is marked
// Subscribe so it can be told apart from a one-shot request. (#374)
func TestInFlightRequests_SubscribeIsMarked(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "sub1", Method: "inflightHandlers.ParkWatch"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "ParkWatch")
	eventually(t, 3*time.Second, func() bool { return len(server.InFlightRequests()) == 1 })

	got := server.InFlightRequests()[0]
	if !got.Subscribe {
		t.Error("Subscribe = false, want true for a subscribe first-run")
	}
	if got.Method != "inflightHandlers.ParkWatch" {
		t.Errorf("Method = %q, want %q", got.Method, "inflightHandlers.ParkWatch")
	}
}

// OldestRequestAge reports the longest-running request, not the newest or an
// average, so it answers "is something stuck" on its own. (#374)
func TestInFlightRequests_OldestRequestAgeTracksLongestRunning(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "old", Method: "inflightHandlers.Park"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Park")
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 1 })

	// Give the first request a measurable head start over the second.
	time.Sleep(20 * time.Millisecond)

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "new", Method: "inflightHandlers.Park"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Park")
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 2 })

	snapshot := server.InFlightRequests()
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Age > snapshot[j].Age })
	if snapshot[0].RequestID != "old" {
		t.Errorf("oldest entry RequestID = %q, want %q", snapshot[0].RequestID, "old")
	}

	// The reported age is the oldest one, so it is at least the head start.
	if age := server.Stats().OldestRequestAge; age < 20*time.Millisecond {
		t.Errorf("OldestRequestAge = %v, want >= 20ms (the head start of the older request)", age)
	}
	if age, oldest := server.Stats().OldestRequestAge, snapshot[0].Age; age < oldest-50*time.Millisecond {
		t.Errorf("OldestRequestAge = %v, want to track the oldest snapshot entry (%v)", age, oldest)
	}
}

// Two connections each running a request are both counted, and each entry
// carries the connection it arrived on. (#374)
func TestInFlightRequests_SpansConnections(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws1 := connectWS(t, ts)
	defer ws1.Close()
	ws2 := connectWS(t, ts)
	defer ws2.Close()

	for _, ws := range []interface{ WriteJSON(any) error }{ws1, ws2} {
		if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "p", Method: "inflightHandlers.Park"}); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	waitEntered(t, h, "Park")
	waitEntered(t, h, "Park")
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 2 })

	got := server.InFlightRequests()
	if len(got) != 2 {
		t.Fatalf("InFlightRequests = %+v, want 2 entries", got)
	}
	if got[0].ConnID == got[1].ConnID {
		t.Errorf("both entries report ConnID %d, want one per connection", got[0].ConnID)
	}
}

// Closing a connection cancels its requests, so a handler that does watch its
// context drops out of the count. The connection-scoped bound is real — it is
// just weak when connections live for days, which is what the count is for.
func TestInFlightRequests_ClearedOnDisconnect(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "p1", Method: "inflightHandlers.Park"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Park")
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 1 })

	ws.Close()
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 0 })
}

// Conn.InFlightRequests reports the per-connection count, so a single noisy
// connection can be found without scanning the whole snapshot. (#374)
func TestConnInFlightRequests(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "p1", Method: "inflightHandlers.Park"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "Park")
	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 1 })

	conns := server.connsSnapshot()
	if len(conns) != 1 {
		t.Fatalf("Connections = %d, want 1", len(conns))
	}
	if got := conns[0].InFlightRequests(); got != 1 {
		t.Errorf("Conn.InFlightRequests() = %d, want 1", got)
	}

	close(h.block)
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	eventually(t, 3*time.Second, func() bool { return conns[0].InFlightRequests() == 0 })
}

// A client reusing an in-flight request ID cancels the shadowed request and
// leaves exactly one entry, so the count cannot be inflated by ID reuse. (#374,
// alongside #225)
func TestInFlightRequests_IDReuseKeepsOneEntry(t *testing.T) {
	tc := NewTestPushConn(7)
	c := tc.Conn

	c.registerRequest("dup", "Test.First", false, func(cause error) {})
	c.registerRequest("dup", "Test.Second", false, func(cause error) {})

	if got := c.InFlightRequests(); got != 1 {
		t.Errorf("InFlightRequests() = %d, want 1 after ID reuse", got)
	}
	got := c.appendInFlight(nil, time.Now())
	if len(got) != 1 || got[0].Method != "Test.Second" {
		t.Errorf("snapshot = %+v, want the replacement (Test.Second)", got)
	}
}

// The count is transport bookkeeping, so it must survive a handler that panics
// — the recovery defer unwinds the handler, which runs the deferred unregister.
func TestInFlightRequests_PanickingHandlerIsUnregistered(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&obsHandlers{})
	server := NewServer(registry, ServerOptions{})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "boom", Method: "obsHandlers.Panic"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeError, 3*time.Second)

	eventually(t, 3*time.Second, func() bool { return server.Stats().InFlightRequests == 0 })
}

// Stats keeps reporting connections and subscriptions correctly now that it
// also walks the request maps.
func TestStats_ExistingFieldsUnchanged(t *testing.T) {
	ts, server, h := setupInflightServer(t)
	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "sub1", Method: "inflightHandlers.ParkWatch"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEntered(t, h, "ParkWatch")
	close(h.block)
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)

	eventually(t, 3*time.Second, func() bool {
		st := server.Stats()
		return st.Connections == 1 && st.Subscriptions == 1 && st.InFlightRequests == 0
	})
}
