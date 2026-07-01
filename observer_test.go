package aprot

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
)

// recordingObserver captures observer callbacks for assertions. It embeds
// NoopObserver so it satisfies Observer even as events are added, and guards all
// state with a mutex since callbacks fire from many goroutines.
type recordingObserver struct {
	NoopObserver
	mu           sync.Mutex
	opened       int
	closed       int
	requests     []RequestEvent
	subReg       []string // "method/id"
	subUnreg     []string // "method/id"
	fanouts      map[string]int
	bufferFull   int
	writeTimeout int
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{fanouts: make(map[string]int)}
}

func (o *recordingObserver) ConnectionOpened(*Conn) {
	o.mu.Lock()
	o.opened++
	o.mu.Unlock()
}

func (o *recordingObserver) ConnectionClosed(*Conn) {
	o.mu.Lock()
	o.closed++
	o.mu.Unlock()
}

func (o *recordingObserver) RequestCompleted(e RequestEvent) {
	o.mu.Lock()
	o.requests = append(o.requests, e)
	o.mu.Unlock()
}

func (o *recordingObserver) SubscriptionRegistered(_ *Conn, method, id string) {
	o.mu.Lock()
	o.subReg = append(o.subReg, method+"/"+id)
	o.mu.Unlock()
}

func (o *recordingObserver) SubscriptionUnregistered(_ *Conn, method, id string) {
	o.mu.Lock()
	o.subUnreg = append(o.subUnreg, method+"/"+id)
	o.mu.Unlock()
}

func (o *recordingObserver) RefreshFanout(key string, matched int) {
	o.mu.Lock()
	o.fanouts[key] += matched
	o.mu.Unlock()
}

func (o *recordingObserver) SendBufferFull(*Conn) {
	o.mu.Lock()
	o.bufferFull++
	o.mu.Unlock()
}

func (o *recordingObserver) WriteTimedOut(*Conn) {
	o.mu.Lock()
	o.writeTimeout++
	o.mu.Unlock()
}

func (o *recordingObserver) snapshotRequests() []RequestEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]RequestEvent(nil), o.requests...)
}

// eventually polls cond until it is true or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// obsHandlers exercise the request/subscribe/refresh observer paths.
type obsHandlers struct{}

func (obsHandlers) Echo(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: req.Message}, nil
}

func (obsHandlers) Fail(ctx context.Context) (*EchoResponse, error) {
	return nil, NewError(CodeInvalidParams, "nope")
}

// Slow sleeps a known amount so the observed Duration is deterministically
// positive (an instant handler can measure exactly 0 on coarse clocks).
func (obsHandlers) Slow(ctx context.Context) (*EchoResponse, error) {
	time.Sleep(5 * time.Millisecond)
	return &EchoResponse{Message: "slow"}, nil
}

// Watch registers a trigger key so a subscription is created for it.
func (obsHandlers) Watch(ctx context.Context) (*EchoResponse, error) {
	RegisterRefreshTrigger(ctx, "watched")
	return &EchoResponse{Message: "watching"}, nil
}

// Touch fires a refresh for the "watched" key.
func (obsHandlers) Touch(ctx context.Context) (*EchoResponse, error) {
	TriggerRefresh(ctx, "watched")
	return &EchoResponse{Message: "touched"}, nil
}

func setupObserverServer(t *testing.T, obs Observer) (*httptest.Server, *Server) {
	t.Helper()
	registry := NewRegistry()
	registry.Register(&obsHandlers{})
	server := NewServer(registry, ServerOptions{Observer: obs})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, server
}

func TestObserver_ConnectDisconnect(t *testing.T) {
	obs := newRecordingObserver()
	ts, _ := setupObserverServer(t, obs)

	ws := connectWS(t, ts)
	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.opened == 1
	})

	ws.Close()
	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.closed == 1
	})
}

func TestObserver_RequestCompleted(t *testing.T) {
	obs := newRecordingObserver()
	ts, _ := setupObserverServer(t, obs)
	ws := connectWS(t, ts)
	defer ws.Close()

	// Successful request.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "obsHandlers.Echo", Params: jsontext.Value(`[{"message":"hi"}]`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)

	// Failing request (returns a ProtocolError with CodeInvalidParams).
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "obsHandlers.Fail"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeError, 3*time.Second)

	// Slow request — its handler sleeps, so the Duration is deterministically
	// positive.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "3", Method: "obsHandlers.Slow"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)

	eventually(t, 3*time.Second, func() bool { return len(obs.snapshotRequests()) == 3 })

	byMethod := make(map[string]RequestEvent)
	for _, e := range obs.snapshotRequests() {
		byMethod[e.Method] = e
	}
	if e := byMethod["obsHandlers.Echo"]; e.Code != 0 {
		t.Errorf("Echo code = %d, want 0 (success)", e.Code)
	}
	if e, ok := byMethod["obsHandlers.Fail"]; !ok || e.Code != CodeInvalidParams {
		t.Errorf("Fail event = %+v, want Code=%d", e, CodeInvalidParams)
	}
	if e := byMethod["obsHandlers.Slow"]; e.Duration < time.Millisecond {
		t.Errorf("Slow Duration = %v, want >= 1ms (handler sleeps 5ms)", e.Duration)
	}
}

func TestObserver_SubscriptionLifecycleAndFanout(t *testing.T) {
	obs := newRecordingObserver()
	ts, server := setupObserverServer(t, obs)
	ws := connectWS(t, ts)
	defer ws.Close()

	// Subscribe to Watch — registers a subscription on key "watched".
	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "s1", Method: "obsHandlers.Watch"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)

	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return len(obs.subReg) == 1 && obs.subReg[0] == "obsHandlers.Watch/s1"
	})

	// Stats reflects the active connection and subscription.
	if st := server.Stats(); st.Connections != 1 || st.Subscriptions != 1 {
		t.Errorf("Stats = %+v, want Connections=1 Subscriptions=1", st)
	}

	// A request that triggers "watched" fans out to the one subscription.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "t1", Method: "obsHandlers.Touch"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)

	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.fanouts["watched"] >= 1
	})

	// Unsubscribe fires the unregister event.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeUnsubscribe, ID: "s1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return len(obs.subUnreg) == 1 && obs.subUnreg[0] == "obsHandlers.Watch/s1"
	})
}

// Disconnecting with an active subscription fires SubscriptionUnregistered for
// the remaining subscription (via subscriptionManager.unregisterConn).
func TestObserver_SubscriptionUnregisteredOnDisconnect(t *testing.T) {
	obs := newRecordingObserver()
	ts, _ := setupObserverServer(t, obs)
	ws := connectWS(t, ts)

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "s1", Method: "obsHandlers.Watch"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return len(obs.subReg) == 1
	})

	ws.Close()
	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return len(obs.subUnreg) == 1 && obs.subUnreg[0] == "obsHandlers.Watch/s1"
	})
}

// SendBufferFull fires when the outbound buffer has no free slot at enqueue
// time. Exercised at the transport level by pre-filling the 256-slot buffer.
func TestObserver_SendBufferFull(t *testing.T) {
	obs := newRecordingObserver()
	registry := NewRegistry()
	server := NewServer(registry, ServerOptions{Observer: obs})
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	tr := &wsTransport{
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}
	tr.conn = &Conn{server: server, transport: tr, id: 1}

	// Fill the buffer so the next enqueue must report backpressure.
	for i := 0; i < 256; i++ {
		tr.send <- nil
	}

	sent := make(chan error, 1)
	go func() { sent <- tr.Send([]byte("x")) }()

	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.bufferFull == 1
	})

	// Drain one slot so the blocked Send can complete.
	<-tr.send
	if err := <-sent; err != nil {
		t.Fatalf("Send returned %v after a slot freed, want nil", err)
	}
}

func TestIsTimeoutError(t *testing.T) {
	if !isTimeoutError(os.ErrDeadlineExceeded) {
		t.Error("os.ErrDeadlineExceeded should be classified as a timeout")
	}
	if !isTimeoutError(&net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}) {
		t.Error("a net.OpError wrapping a deadline should be a timeout")
	}
	if isTimeoutError(context.Canceled) {
		t.Error("context.Canceled is not a write timeout")
	}
	if isTimeoutError(nil) {
		t.Error("nil is not a timeout")
	}
}

// A server with no observer must not panic on any of the instrumented paths.
func TestObserver_NilObserverIsSafe(t *testing.T) {
	ts, server := setupObserverServer(t, nil)
	if server.observer != nil {
		t.Fatal("expected nil observer")
	}
	ws := connectWS(t, ts)
	defer ws.Close()
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "obsHandlers.Echo", Params: jsontext.Value(`[{"message":"hi"}]`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	if st := server.Stats(); st.Connections != 1 {
		t.Errorf("Stats.Connections = %d, want 1", st.Connections)
	}
}
