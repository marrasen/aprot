package aprot

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// principalHandlers exercises PrincipalFrom on every dispatch path: unary,
// streaming, subscribe, refresh, and REST.
type principalHandlers struct {
	handlerRan atomic.Bool
}

type whoResult struct {
	Who string `json:"who"`
}

// Who reports the principal the handler sees.
func (h *principalHandlers) Who(ctx context.Context) (*whoResult, error) {
	h.handlerRan.Store(true)
	who, _ := PrincipalFrom(ctx).(string)
	return &whoResult{Who: who}, nil
}

// StreamWho reports the principal from a streaming handler — the dispatch
// path that bypasses Server.invoke.
func (h *principalHandlers) StreamWho(ctx context.Context) (iter.Seq[string], error) {
	who, _ := PrincipalFrom(ctx).(string)
	return func(yield func(string) bool) {
		yield(who)
	}, nil
}

// SubscribeWho is a subscribable query reporting the principal, so refreshes
// re-report whatever the provider resolves at refresh time.
func (h *principalHandlers) SubscribeWho(ctx context.Context) (*whoResult, error) {
	RegisterRefreshTrigger(ctx, "who")
	who, _ := PrincipalFrom(ctx).(string)
	return &whoResult{Who: who}, nil
}

// Touch triggers a refresh of SubscribeWho subscribers.
func (h *principalHandlers) Touch(ctx context.Context) (*whoResult, error) {
	TriggerRefresh(ctx, "who")
	return &whoResult{}, nil
}

// principalProviderState is a mutable identity source for tests: the
// provider resolves to the current value, or fails when failWith is set.
type principalProviderState struct {
	calls    atomic.Int64
	who      atomic.Value // string
	failWith atomic.Value // errBox
}

// errBox wraps an error so atomic.Value accepts a nil payload.
type errBox struct{ err error }

func (s *principalProviderState) provider(ctx context.Context) (any, error) {
	s.calls.Add(1)
	if box, ok := s.failWith.Load().(errBox); ok && box.err != nil {
		return nil, box.err
	}
	return s.who.Load().(string), nil
}

func newPrincipalServer(t *testing.T, h *principalHandlers, state *principalProviderState) (*httptest.Server, *Server) {
	t.Helper()
	registry := NewRegistry()
	registry.Register(h)
	server := NewServer(registry)
	server.OnAuth(func(ctx context.Context, conn *Conn, token string) error {
		u, ok := strings.CutPrefix(token, "good:")
		if !ok {
			return ErrAuthFailed("invalid token")
		}
		state.who.Store(u)
		conn.SetUserID(u)
		conn.SetPrincipalProvider(state.provider)
		return nil
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, server
}

// principalRESTHandlers is the REST-only fixture: no streaming method, so
// RegisterREST accepts it.
type principalRESTHandlers struct{}

// GetWho reports the principal the handler sees.
func (principalRESTHandlers) GetWho(ctx context.Context) (*whoResult, error) {
	who, _ := PrincipalFrom(ctx).(string)
	return &whoResult{Who: who}, nil
}

// principalFrame is a permissive view of server frames for these tests.
type principalFrame struct {
	Type    string     `json:"type"`
	ID      string     `json:"id"`
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Result  *whoResult `json:"result"`
	Item    string     `json:"item"`
}

func TestPrincipal_ContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := PrincipalFrom(ctx); got != nil {
		t.Fatalf("PrincipalFrom(empty ctx) = %v, want nil", got)
	}
	type user struct{ name string }
	u := &user{name: "alice"}
	ctx = WithPrincipal(ctx, u)
	if got := PrincipalFrom(ctx); got != any(u) {
		t.Fatalf("PrincipalFrom = %v, want %v", got, u)
	}
}

// The provider set in OnAuth populates the principal on unary requests.
func TestPrincipal_SocketUnary(t *testing.T) {
	h := &principalHandlers{}
	state := &principalProviderState{}
	ts, _ := newPrincipalServer(t, h, state)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()
	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got %q", f.Type)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "principalHandlers.Who"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var f principalFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != string(TypeResponse) || f.Result == nil || f.Result.Who != "alice" {
		t.Fatalf("got %+v, want response with who=alice", f)
	}
	if state.calls.Load() != 1 {
		t.Errorf("provider calls = %d, want 1", state.calls.Load())
	}
}

// Streaming dispatch bypasses Server.invoke; the principal must still be
// resolved and visible inside the iterator's context.
func TestPrincipal_SocketStreaming(t *testing.T) {
	h := &principalHandlers{}
	state := &principalProviderState{}
	ts, _ := newPrincipalServer(t, h, state)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()
	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got %q", f.Type)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "s1", Method: "principalHandlers.StreamWho"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var f principalFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != string(TypeStreamItem) || f.Item != "alice" {
		t.Fatalf("got %+v, want stream_item with item=alice", f)
	}
}

// The provider runs again on every server-driven subscription refresh, so
// an identity change takes effect without a reconnect.
func TestPrincipal_RefreshReResolves(t *testing.T) {
	h := &principalHandlers{}
	state := &principalProviderState{}
	ts, _ := newPrincipalServer(t, h, state)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()
	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got %q", f.Type)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "sub-1", Method: "principalHandlers.SubscribeWho"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var f principalFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read subscribe response: %v", err)
	}
	if f.ID != "sub-1" || f.Result == nil || f.Result.Who != "alice" {
		t.Fatalf("subscribe response = %+v, want who=alice", f)
	}

	// The identity the provider resolves changes — no reconnect, no re-auth.
	state.who.Store("alice-promoted")

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "principalHandlers.Touch"}); err != nil {
		t.Fatalf("touch: %v", err)
	}

	// Expect the Touch response and the refresh for sub-1, in either order.
	sawRefresh := false
	for i := 0; i < 2; i++ {
		var fr principalFrame
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := ws.ReadJSON(&fr); err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if fr.ID == "sub-1" {
			sawRefresh = true
			if fr.Result == nil || fr.Result.Who != "alice-promoted" {
				t.Fatalf("refresh delivered %+v, want who=alice-promoted", fr)
			}
		}
	}
	if !sawRefresh {
		t.Fatal("no refresh frame for sub-1 arrived")
	}
	// Subscribe + Touch + refresh: one resolution per execution.
	if got := state.calls.Load(); got != 3 {
		t.Errorf("provider calls = %d, want 3 (subscribe, touch, refresh)", got)
	}
}

// A provider error fails the request before middleware, with the error's
// own wire code, and the handler never runs.
func TestPrincipal_ProviderErrorRejectsRequest(t *testing.T) {
	h := &principalHandlers{}
	state := &principalProviderState{}
	ts, _ := newPrincipalServer(t, h, state)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()
	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got %q", f.Type)
	}

	state.failWith.Store(errBox{ErrUnauthorized("session revoked")})

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "principalHandlers.Who"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var f principalFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != string(TypeError) || f.Code != CodeUnauthorized || f.Message != "session revoked" {
		t.Fatalf("got %+v, want error code=%d message=%q", f, CodeUnauthorized, "session revoked")
	}
	if h.handlerRan.Load() {
		t.Error("handler ran despite provider error")
	}
}

// A provider error during refresh sends an error frame and keeps the
// subscription registered, so a later refresh re-resolves and recovers.
func TestPrincipal_ProviderErrorOnRefreshKeepsSubscription(t *testing.T) {
	h := &principalHandlers{}
	state := &principalProviderState{}
	ts, server := newPrincipalServer(t, h, state)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()
	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got %q", f.Type)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "sub-1", Method: "principalHandlers.SubscribeWho"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var f principalFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read subscribe response: %v", err)
	}

	// Provider starts failing; a server-driven refresh must surface the
	// error, not silently drop or kill the subscription.
	state.failWith.Store(errBox{ErrUnauthorized("session revoked")})
	server.TriggerRefresh("who")

	sawError := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !sawError {
		var fr principalFrame
		_ = ws.SetReadDeadline(time.Now().Add(time.Second))
		if err := ws.ReadJSON(&fr); err != nil {
			continue
		}
		if fr.ID == "sub-1" && fr.Type == string(TypeError) {
			sawError = true
			if fr.Code != CodeUnauthorized {
				t.Fatalf("refresh error code = %d, want %d", fr.Code, CodeUnauthorized)
			}
		}
	}
	if !sawError {
		t.Fatal("no error frame for the failed refresh")
	}
	if subs := server.subscriptions.getSubscriptionsForKey("who"); len(subs) != 1 {
		t.Fatalf("subscription count after failed refresh = %d, want 1", len(subs))
	}

	// The provider recovers; the next refresh delivers again.
	state.failWith.Store(errBox{})
	state.who.Store("alice")
	server.TriggerRefresh("who")

	sawRefresh := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !sawRefresh {
		var fr principalFrame
		_ = ws.SetReadDeadline(time.Now().Add(time.Second))
		if err := ws.ReadJSON(&fr); err != nil {
			continue
		}
		if fr.ID == "sub-1" && fr.Type == string(TypeResponse) && fr.Result != nil && fr.Result.Who == "alice" {
			sawRefresh = true
		}
	}
	if !sawRefresh {
		t.Fatal("subscription did not recover after the provider recovered")
	}
}

// On REST, a wrapping http.Handler attaches the principal with
// WithPrincipal; the handler reads it with PrincipalFrom, no connection
// involved.
func TestPrincipal_RESTWrapper(t *testing.T) {
	registry := NewRegistry()
	h := &principalRESTHandlers{}
	registry.RegisterREST(h)
	adapter := NewRESTAdapter(registry)

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good" {
			r = r.WithContext(WithPrincipal(r.Context(), "alice"))
		}
		adapter.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/principal-rest-handlers/get-who", nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if body := string(buf[:n]); !strings.Contains(body, `"alice"`) {
		t.Fatalf("body = %s, want it to contain \"alice\"", body)
	}

	// Anonymous request: no principal, handler sees nil.
	resp2, err := http.Get(ts.URL + "/principal-rest-handlers/get-who")
	if err != nil {
		t.Fatalf("GET anon: %v", err)
	}
	defer resp2.Body.Close()
	n, _ = resp2.Body.Read(buf)
	if body := string(buf[:n]); !strings.Contains(body, `""`) {
		t.Fatalf("anon body = %s, want empty who", body)
	}
}
