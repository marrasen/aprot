package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	json "github.com/go-json-experiment/json"
	"github.com/marrasen/aprot"
)

// whoHandlers reports the principal a tool call sees.
type whoHandlers struct{}

// Who returns the caller's identity as the handler sees it.
func (whoHandlers) Who(ctx context.Context) (string, error) {
	who, _ := aprot.PrincipalFrom(ctx).(string)
	return who, nil
}

// newWhoAdapter wires a Who tool behind an MCP adapter.
func newWhoAdapter(t *testing.T) (*aprot.Server, *Adapter) {
	t.Helper()
	r := aprot.NewRegistry()
	r.Register(&whoHandlers{})
	r.EnableMCP(&whoHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"Who": {ReadOnly: true},
	}})
	s := aprot.NewServer(r)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s, NewAdapter(s, Options{ServerName: "who"})
}

// callWho posts a tools/call for the Who tool through wrapper, which stands
// in for the consumer's authenticating http.Handler.
func callWho(t *testing.T, wrapper http.Handler) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"who_handlers_who","arguments":{}}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	wrapper.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
	}
	return resp
}

// whoText returns the tool result's text and whether it is an error result.
func whoText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	if e, ok := resp["error"]; ok {
		t.Fatalf("JSON-RPC error: %v", e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", resp)
	}
	isErr, _ := res["isError"].(bool)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content: %v", res)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return strings.Trim(text, `"`), isErr
}

// The scenario this package's doc invites: a wrapper that authenticates the
// request installs a detached connection so connection-reading middleware
// runs unchanged, and reuses its socket PrincipalProvider on it. Before #337
// the provider never ran over MCP and the tool saw a nil principal, while
// identical middleware saw identity over WebSocket.
func TestPrincipal_DetachedConnProviderOverMCP(t *testing.T) {
	server, adapter := newWhoAdapter(t)

	var calls atomic.Int64
	wrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := server.NewDetachedConn()
		conn.SetPrincipalProvider(func(ctx context.Context) (any, error) {
			calls.Add(1)
			return "alice", nil
		})
		adapter.ServeHTTP(w, r.WithContext(aprot.WithConnection(r.Context(), conn)))
	})

	text, isErr := whoText(t, callWho(t, wrapper))
	if isErr {
		t.Fatalf("tool reported an error: %s", text)
	}
	if text != "alice" {
		t.Errorf("tool saw principal %q, want alice", text)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
}

// A provider error rejects the tool call rather than running the handler
// anonymously.
func TestPrincipal_DetachedConnProviderErrorOverMCP(t *testing.T) {
	server, adapter := newWhoAdapter(t)

	wrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := server.NewDetachedConn()
		conn.SetPrincipalProvider(func(ctx context.Context) (any, error) {
			return nil, aprot.ErrUnauthorized("token expired")
		})
		adapter.ServeHTTP(w, r.WithContext(aprot.WithConnection(r.Context(), conn)))
	})

	text, isErr := whoText(t, callWho(t, wrapper))
	if !isErr {
		t.Fatalf("tool call succeeded despite a provider error: %q", text)
	}
	if !strings.Contains(text, "token expired") {
		t.Errorf("error text = %q, want it to mention the provider's error", text)
	}
}

// A wrapper that authenticates the request itself wins over a provider on the
// connection, and the provider does not run.
func TestPrincipal_ExplicitPrincipalWinsOverMCP(t *testing.T) {
	server, adapter := newWhoAdapter(t)

	var calls atomic.Int64
	wrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := server.NewDetachedConn()
		conn.SetPrincipalProvider(func(ctx context.Context) (any, error) {
			calls.Add(1)
			return "from-provider", nil
		})
		ctx := aprot.WithConnection(r.Context(), conn)
		ctx = aprot.WithPrincipal(ctx, "from-wrapper")
		adapter.ServeHTTP(w, r.WithContext(ctx))
	})

	text, _ := whoText(t, callWho(t, wrapper))
	if text != "from-wrapper" {
		t.Errorf("tool saw principal %q, want from-wrapper", text)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}
}

// With no connection installed, a tool call stays anonymous — connection
// presence is still never faked.
func TestPrincipal_NoConnectionOverMCPIsAnonymous(t *testing.T) {
	_, adapter := newWhoAdapter(t)

	text, isErr := whoText(t, callWho(t, adapter))
	if isErr {
		t.Fatalf("tool reported an error: %s", text)
	}
	if text != "" {
		t.Errorf("tool saw principal %q, want anonymous", text)
	}
}
