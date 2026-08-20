package aprot

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// addressHandlers reports the address UserID(ctx) resolves on each dispatch
// path.
type addressHandlers struct{}

type addrResult struct {
	Addr string `json:"addr"`
}

// Where reports the address the handler sees on a unary request.
func (addressHandlers) Where(ctx context.Context) (*addrResult, error) {
	return &addrResult{Addr: UserID(ctx)}, nil
}

// SubscribeWhere is a subscribable query reporting the address, so a
// server-driven refresh re-reports whatever the connection carries at
// refresh time.
func (addressHandlers) SubscribeWhere(ctx context.Context) (*addrResult, error) {
	RegisterRefreshTrigger(ctx, "where")
	return &addrResult{Addr: UserID(ctx)}, nil
}

// Rebind simulates a mid-session re-authentication: it changes the
// connection's address and refreshes SubscribeWhere subscribers.
func (addressHandlers) Rebind(ctx context.Context, to string) (*addrResult, error) {
	if c := Connection(ctx); c != nil {
		c.SetUserID(to)
	}
	TriggerRefresh(ctx, "where")
	return &addrResult{Addr: UserID(ctx)}, nil
}

// addressRESTHandlers is the REST-only fixture (no streaming method, so
// RegisterREST accepts it).
type addressRESTHandlers struct{}

// GetWhere reports the address the handler sees.
func (addressRESTHandlers) GetWhere(ctx context.Context) (*addrResult, error) {
	return &addrResult{Addr: UserID(ctx)}, nil
}

// addressFrame is a permissive view of server frames for these tests.
type addressFrame struct {
	Type   string      `json:"type"`
	ID     string      `json:"id"`
	Result *addrResult `json:"result"`
}

func TestUserID_ContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := UserID(ctx); got != "" {
		t.Fatalf("UserID(empty ctx) = %q, want \"\"", got)
	}
	if got := UserID(WithUserID(ctx, "alice")); got != "alice" {
		t.Fatalf("UserID = %q, want alice", got)
	}
}

// With no context value, the address reads through to the connection.
func TestUserID_ReadsThroughToConnection(t *testing.T) {
	ctx := WithTestConnectionUser(context.Background(), 1, "alice")
	if got := UserID(ctx); got != "alice" {
		t.Fatalf("UserID = %q, want alice", got)
	}
}

// A non-empty context value wins over the connection: a wrapper that
// authenticated the request is the authority on that execution's address.
func TestUserID_ContextValueWinsOverConnection(t *testing.T) {
	ctx := WithTestConnectionUser(context.Background(), 1, "alice")
	if got := UserID(WithUserID(ctx, "bob")); got != "bob" {
		t.Fatalf("UserID = %q, want bob", got)
	}
}

// WithUserID(ctx, "") must not blank an attached connection's address: a
// wrapper that forwards a header unconditionally passes "" when the header
// is absent, and that is not a statement about the connection.
func TestUserID_EmptyContextValueFallsThroughToConnection(t *testing.T) {
	ctx := WithTestConnectionUser(context.Background(), 1, "alice")
	if got := UserID(WithUserID(ctx, "")); got != "alice" {
		t.Fatalf("UserID = %q, want alice", got)
	}
	// With no connection either, the result is simply empty.
	if got := UserID(WithUserID(context.Background(), "")); got != "" {
		t.Fatalf("UserID = %q, want \"\"", got)
	}
}

// The address is read through, not snapshotted at dispatch: middleware that
// authenticates the request sets it, and the handler in that same request
// sees it. A dispatch-time snapshot would read "" here.
func TestUserID_MidRequestSetUserIDVisibleToHandler(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&addressHandlers{})
	server := NewServer(registry)
	server.Use(func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			if c := Connection(ctx); c != nil {
				c.SetUserID("alice")
			}
			return next(ctx, req)
		}
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWSPath(t, ts, "")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "addressHandlers.Where"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var f addressFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Type != string(TypeResponse) || f.Result == nil || f.Result.Addr != "alice" {
		t.Fatalf("got %+v, want response with addr=alice", f)
	}
}

// A server-driven subscription refresh observes the connection's *updated*
// address after a mid-session re-authentication. This is the behavior
// read-through exists for: identity is a per-execution snapshot, but the
// address is a live routing fact, so fan-out follows the user.
func TestUserID_RefreshObservesUpdatedAddress(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&addressHandlers{})
	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWSPath(t, ts, "")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "sub-1", Method: "addressHandlers.SubscribeWhere"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var f addressFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read subscribe response: %v", err)
	}
	if f.ID != "sub-1" || f.Result == nil || f.Result.Addr != "" {
		t.Fatalf("subscribe response = %+v, want empty addr", f)
	}

	if err := ws.WriteJSON(IncomingMessage{
		Type:   TypeRequest,
		ID:     "2",
		Method: "addressHandlers.Rebind",
		Params: jsontext.Value(`["alice-rebound"]`),
	}); err != nil {
		t.Fatalf("rebind: %v", err)
	}

	// Expect the Rebind response and the refresh for sub-1, in either order.
	sawRefresh := false
	for i := 0; i < 2; i++ {
		var fr addressFrame
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := ws.ReadJSON(&fr); err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if fr.ID == "sub-1" {
			sawRefresh = true
			if fr.Result == nil || fr.Result.Addr != "alice-rebound" {
				t.Fatalf("refresh delivered %+v, want addr=alice-rebound", fr)
			}
		}
	}
	if !sawRefresh {
		t.Fatal("no refresh frame for sub-1 arrived")
	}
}

// On a request-scoped transport the wrapper supplies the address with
// WithUserID; an anonymous request carries none.
func TestUserID_RESTWrapper(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&addressRESTHandlers{})
	adapter := NewRESTAdapter(registry)

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good" {
			r = r.WithContext(WithUserID(r.Context(), "alice"))
		}
		adapter.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/address-rest-handlers/get-where", nil)
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

	resp2, err := http.Get(ts.URL + "/address-rest-handlers/get-where")
	if err != nil {
		t.Fatalf("GET anon: %v", err)
	}
	defer resp2.Body.Close()
	n, _ = resp2.Body.Read(buf)
	if body := string(buf[:n]); !strings.Contains(body, `""`) {
		t.Fatalf("anon body = %s, want empty addr", body)
	}
}

// A detached connection installed by a wrapper supplies the address on
// request-scoped paths too, and an explicit WithUserID still wins.
func TestUserID_DetachedConnection(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&addressHandlers{})
	server := NewServer(registry)

	conn := server.NewDetachedConn()
	conn.SetUserID("alice")
	ctx := WithConnection(context.Background(), conn)
	if got := UserID(ctx); got != "alice" {
		t.Fatalf("UserID = %q, want alice", got)
	}
	if got := UserID(WithUserID(ctx, "bob")); got != "bob" {
		t.Fatalf("UserID = %q, want bob", got)
	}
}
