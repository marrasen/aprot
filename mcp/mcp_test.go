package mcp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marrasen/aprot"
)

// TodoHandlers is the MCP test fixture: a subscribable query and a mutation.
type TodoHandlers struct{}

type TodoList struct {
	Todos []string `json:"todos"`
}

// CreateTodoReq is the CreateTodo payload.
type CreateTodoReq struct {
	// Text is the todo item text.
	Text string `json:"text" validate:"required,min=2"`
}

// ListTodos returns every todo item.
func (TodoHandlers) ListTodos(ctx context.Context) (*TodoList, error) {
	aprot.RegisterRefreshTrigger(ctx, "todos")
	return &TodoList{Todos: []string{"one"}}, nil
}

// CreateTodo adds a todo item.
func (TodoHandlers) CreateTodo(ctx context.Context, req *CreateTodoReq) (*TodoList, error) {
	aprot.TriggerRefresh(ctx, "todos")
	return &TodoList{Todos: []string{"one", req.Text}}, nil
}

// DeleteTodo removes a todo by index.
func (TodoHandlers) DeleteTodo(ctx context.Context, index int) error {
	if index < 0 {
		return errors.New("index out of range")
	}
	return nil
}

func newTestAdapter(t *testing.T, mw ...aprot.Middleware) (*aprot.Server, *Adapter) {
	t.Helper()
	r := aprot.NewRegistry()
	r.Register(&TodoHandlers{})
	r.EnableMCP(&TodoHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"ListTodos":  {ReadOnly: true, Idempotent: true},
		"CreateTodo": {},
		"DeleteTodo": {Destructive: true},
	}})
	s := aprot.NewServer(r)
	s.Use(mw...)
	return s, NewAdapter(s, Options{ServerName: "todos", ServerVersion: "1.2.3"})
}

// rpc posts a JSON-RPC request and decodes the response envelope.
func rpc(t *testing.T, a *Adapter, id, method, params string) (map[string]any, int) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q`, id, method)
	if params != "" {
		body += `,"params":` + params
	}
	body += "}"
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		return map[string]any{"httpBody": w.Body.String()}, w.Code
	}
	var resp map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid response JSON: %v\n%s", err, w.Body.String())
		}
	}
	return resp, w.Code
}

func result(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if e, ok := resp["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", resp)
	}
	return res
}

func TestInitializeAndPing(t *testing.T) {
	_, a := newTestAdapter(t)

	resp, _ := rpc(t, a, "1", "initialize", `{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}`)
	res := result(t, resp)
	if res["protocolVersion"] != "2025-03-26" {
		t.Errorf("negotiation should echo a supported version, got %v", res["protocolVersion"])
	}
	si, _ := res["serverInfo"].(map[string]any)
	if si["name"] != "todos" || si["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", si)
	}

	// Unknown client version → answer with our latest.
	resp, _ = rpc(t, a, "2", "initialize", `{"protocolVersion":"9999-01-01"}`)
	if res := result(t, resp); res["protocolVersion"] != "2025-06-18" {
		t.Errorf("unknown version: got %v", res["protocolVersion"])
	}

	// Ping must carry an (empty object) result.
	resp, _ = rpc(t, a, "3", "ping", "")
	if _, ok := resp["result"]; !ok {
		t.Errorf("ping response missing result: %v", resp)
	}
}

func TestNotificationAccepted(t *testing.T) {
	_, a := newTestAdapter(t)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
		t.Fatalf("notification: got %d %q, want 202 empty", w.Code, w.Body.String())
	}
}

func TestToolsList(t *testing.T) {
	_, a := newTestAdapter(t)
	resp, _ := rpc(t, a, "1", "tools/list", "")
	res := result(t, resp)
	tools, _ := res["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(tools), tools)
	}

	byName := map[string]map[string]any{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		byName[m["name"].(string)] = m
	}

	create := byName["todo_handlers_create_todo"]
	if create == nil {
		t.Fatalf("default tool name missing: %v", byName)
	}
	if d, _ := create["description"].(string); !strings.Contains(d, "adds a todo item") {
		t.Errorf("description not resolved from godoc: %q", d)
	}
	schema := create["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("inputSchema.type = %v", schema["type"])
	}
	props := schema["properties"].(map[string]any)
	text, _ := props["text"].(map[string]any)
	if text == nil || text["type"] != "string" {
		t.Errorf("text property = %v", props)
	}
	if d, _ := text["description"].(string); !strings.Contains(d, "todo item text") {
		t.Errorf("field godoc missing from schema: %q", d)
	}

	list := byName["todo_handlers_list_todos"]
	ann := list["annotations"].(map[string]any)
	if ann["readOnlyHint"] != true || ann["idempotentHint"] != true || ann["destructiveHint"] != false {
		t.Errorf("annotations = %v", ann)
	}
}

func TestToolsCall_SingleStruct(t *testing.T) {
	_, a := newTestAdapter(t)
	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"todo_handlers_create_todo","arguments":{"text":"two"}}`)
	res := result(t, resp)
	if res["isError"] != false {
		t.Fatalf("isError = %v: %v", res["isError"], res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	todos, _ := sc["todos"].([]any)
	if len(todos) != 2 || todos[1] != "two" {
		t.Fatalf("structuredContent = %v", sc)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	c0 := content[0].(map[string]any)
	if c0["type"] != "text" || !strings.Contains(c0["text"].(string), `"two"`) {
		t.Errorf("text content = %v", c0)
	}
}

func TestToolsCall_NamedPrimitiveAndVoid(t *testing.T) {
	_, a := newTestAdapter(t)
	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"todo_handlers_delete_todo","arguments":{"index":1}}`)
	res := result(t, resp)
	if res["isError"] != false {
		t.Fatalf("isError = %v: %v", res["isError"], res)
	}
	if _, has := res["structuredContent"]; has {
		t.Errorf("void handler should not produce structuredContent: %v", res)
	}
}

func TestToolsCall_HandlerErrorIsToolResult(t *testing.T) {
	_, a := newTestAdapter(t)
	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"todo_handlers_delete_todo","arguments":{"index":-1}}`)
	res := result(t, resp)
	if res["isError"] != true {
		t.Fatalf("handler error must set isError: %v", res)
	}
	content, _ := res["content"].([]any)
	c0 := content[0].(map[string]any)
	if !strings.Contains(c0["text"].(string), "index out of range") {
		t.Errorf("error text = %v", c0)
	}
}

func TestToolsCall_ProtocolErrors(t *testing.T) {
	_, a := newTestAdapter(t)

	// Unknown tool → JSON-RPC invalid params.
	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"nope","arguments":{}}`)
	e, _ := resp["error"].(map[string]any)
	if e == nil || e["code"] != float64(-32602) {
		t.Fatalf("unknown tool: %v", resp)
	}

	// Missing required argument → JSON-RPC invalid params.
	resp, _ = rpc(t, a, "2", "tools/call", `{"name":"todo_handlers_delete_todo","arguments":{}}`)
	e, _ = resp["error"].(map[string]any)
	if e == nil || e["code"] != float64(-32602) {
		t.Fatalf("missing argument: %v", resp)
	}

	// Validation failure inside the handler pipeline → JSON-RPC invalid params.
	resp, _ = rpc(t, a, "3", "tools/call", `{"name":"todo_handlers_create_todo","arguments":{"text":123}}`)
	e, _ = resp["error"].(map[string]any)
	if e == nil || e["code"] != float64(-32602) {
		t.Fatalf("bad argument type: %v", resp)
	}

	// Unknown method → -32601.
	resp, _ = rpc(t, a, "4", "resources/list", "")
	e, _ = resp["error"].(map[string]any)
	if e == nil || e["code"] != float64(-32601) {
		t.Fatalf("unknown method: %v", resp)
	}
}

func TestGetNotAllowed(t *testing.T) {
	_, a := newTestAdapter(t)
	req := httptest.NewRequest("GET", "/mcp", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: got %d, want 405", w.Code)
	}
}

// TestNoConnectionByDefault: tool calls carry no connection, exactly as on
// REST — Connection(ctx) == nil is how middleware sees an anonymous
// request-scoped call, and the adapter must not fake one (#329).
func TestNoConnectionByDefault(t *testing.T) {
	ran := false
	var sawConn *aprot.Conn
	var sawHTTP bool
	mw := func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			ran = true
			sawConn = aprot.Connection(ctx)
			sawHTTP = aprot.HTTPRequestFromContext(ctx) != nil
			return next(ctx, req)
		}
	}
	_, a := newTestAdapter(t, mw)

	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"todo_handlers_list_todos","arguments":{}}`)
	result(t, resp)
	if !ran {
		t.Fatal("middleware did not run")
	}
	if sawConn != nil {
		t.Errorf("middleware saw a connection on an anonymous tool call: %v", sawConn)
	}
	if !sawHTTP {
		t.Error("middleware could not reach the HTTP request")
	}
}

// TestWrapperInstalledConnIsKept: a wrapping http.Handler that authenticates
// and installs a detached connection via WithConnection still hands it to
// middleware — the documented pattern for connection-shaped auth over MCP.
func TestWrapperInstalledConnIsKept(t *testing.T) {
	var sawConn *aprot.Conn
	mw := func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			sawConn = aprot.Connection(ctx)
			return next(ctx, req)
		}
	}
	server, a := newTestAdapter(t, mw)

	conn := server.NewDetachedConn()
	conn.SetUserID("mcp-user")
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.ServeHTTP(w, r.WithContext(aprot.WithConnection(r.Context(), conn)))
	})

	body := `{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"todo_handlers_list_todos","arguments":{}}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	if sawConn != conn {
		t.Fatalf("middleware saw %v, want the wrapper-installed connection", sawConn)
	}
	if sawConn.UserID() != "mcp-user" {
		t.Errorf("UserID = %q", sawConn.UserID())
	}
}

// TestRefreshReachesSubscribers closes the #316 loop end to end: an MCP tool
// call mutates, and a WebSocket subscriber of the same data gets refreshed.
func TestRefreshReachesSubscribers(t *testing.T) {
	s, a := newTestAdapter(t)

	sub := aprot.NewTestSubscriber(t, s, "TodoHandlers.ListTodos")

	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"todo_handlers_create_todo","arguments":{"text":"two"}}`)
	result(t, resp)

	frames := sub.WaitFrames(t)
	if len(frames) != 1 || frames[0]["type"] != "response" {
		t.Fatalf("expected 1 refresh frame for the WS subscriber, got %v", frames)
	}
}

// RestOnlyHandlers is registered via RegisterREST only — absent from the WS
// dispatch map. Regression fixture for #322.
type RestOnlyHandlers struct{}

// Ping returns a greeting.
func (RestOnlyHandlers) Ping(ctx context.Context) (string, error) { return "pong", nil }

// Regression test for #322: tools/call on a RegisterREST-only group must
// dispatch (it used to fail with "method not found"), and its group
// middleware must run (#321).
func TestToolsCall_RESTOnlyGroup(t *testing.T) {
	var mwCalls int
	mw := func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			mwCalls++
			return next(ctx, req)
		}
	}
	r := aprot.NewRegistry()
	r.RegisterREST(&RestOnlyHandlers{}, mw)
	r.EnableMCP(&RestOnlyHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"Ping": {Name: "ping_tool", ReadOnly: true},
	}})
	s := aprot.NewServer(r)
	a := NewAdapter(s, Options{ServerName: "check"})

	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"ping_tool","arguments":{}}`)
	res := result(t, resp)
	if res["isError"] != false {
		t.Fatalf("tools/call on REST-only group failed: %v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	c0 := content[0].(map[string]any)
	if !strings.Contains(c0["text"].(string), "pong") {
		t.Errorf("text content = %v", c0)
	}
	if mwCalls != 1 {
		t.Errorf("group middleware ran %d times, want 1", mwCalls)
	}
}

// McpOnlyHandlers is registered via RegisterMCP — MCP is its only surface.
type McpOnlyHandlers struct{}

// SearchOrders finds orders matching a query.
func (McpOnlyHandlers) SearchOrders(ctx context.Context, query string) (string, error) {
	return "orders for " + query, nil
}

func TestToolsCall_RegisterMCPGroup(t *testing.T) {
	var mwCalls int
	mw := func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			mwCalls++
			return next(ctx, req)
		}
	}
	r := aprot.NewRegistry()
	r.RegisterMCP(&McpOnlyHandlers{}, mw)
	r.EnableMCP(&McpOnlyHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"SearchOrders": {Name: "search_orders", ReadOnly: true},
	}})
	s := aprot.NewServer(r)
	a := NewAdapter(s, Options{ServerName: "check"})

	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"search_orders","arguments":{"query":"boots"}}`)
	res := result(t, resp)
	if res["isError"] != false {
		t.Fatalf("tools/call on RegisterMCP group failed: %v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	c0 := content[0].(map[string]any)
	if !strings.Contains(c0["text"].(string), "orders for boots") {
		t.Errorf("text content = %v", c0)
	}
	if mwCalls != 1 {
		t.Errorf("group middleware ran %d times, want 1", mwCalls)
	}
}

// A RegisterMCP group with no EnableMCP tools is reachable nowhere; the
// adapter refuses to be built over it.
func TestNewAdapter_PanicsOnToollessRegisterMCPGroup(t *testing.T) {
	r := aprot.NewRegistry()
	r.RegisterMCP(&McpOnlyHandlers{})
	s := aprot.NewServer(r)
	defer func() {
		msg, _ := recover().(string)
		if msg == "" {
			t.Fatal("NewAdapter must panic for a tool-less RegisterMCP group")
		}
		if !strings.Contains(msg, "McpOnlyHandlers") {
			t.Errorf("panic should name the group: %q", msg)
		}
	}()
	NewAdapter(s, Options{ServerName: "check"})
}

// PanicHandlers is the fixture for #325.
type PanicHandlers struct{}

// Boom panics, as a buggy handler would.
func (PanicHandlers) Boom(ctx context.Context) (string, error) {
	panic("nil map write")
}

// TestToolsCall_HandlerPanicIsToolResult: a panicking tool must produce a
// tool result with isError and the panic message, not a dropped connection
// (#325). Before the fix the panic unwound through Server.Invoke into
// net/http and the client saw an EOF with nothing correlated to the id.
func TestToolsCall_HandlerPanicIsToolResult(t *testing.T) {
	r := aprot.NewRegistry()
	r.RegisterMCP(&PanicHandlers{})
	r.EnableMCP(&PanicHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"Boom": {Name: "boom"},
	}})
	s := aprot.NewServer(r, aprot.ServerOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	a := NewAdapter(s, Options{ServerName: "panic-test"})

	resp, _ := rpc(t, a, "1", "tools/call", `{"name":"boom","arguments":{}}`)
	res := result(t, resp)
	if res["isError"] != true {
		t.Fatalf("panic must set isError: %v", res)
	}
	content, _ := res["content"].([]any)
	c0 := content[0].(map[string]any)
	text := c0["text"].(string)
	if !strings.Contains(text, "handler panicked") {
		t.Errorf("error text = %v", c0)
	}
	// Generic message only: the panic value stays in the server log.
	if strings.Contains(text, "nil map write") {
		t.Errorf("panic value leaked to the client: %q", text)
	}
}

// panicMarshalResult panics in its custom marshaler; MarshalWire runs
// outside Server.Invoke's recover, so the adapter needs its own (#325
// review finding 2).
type panicMarshalResult struct{}

func (panicMarshalResult) MarshalJSON() ([]byte, error) {
	panic("marshal boom: secret-dsn")
}

// Weird returns a result whose marshaling panics.
func (PanicHandlers) Weird(ctx context.Context) (*panicMarshalResult, error) {
	return &panicMarshalResult{}, nil
}

func TestToolsCall_ResponseMarshalPanicIsRPCError(t *testing.T) {
	r := aprot.NewRegistry()
	r.RegisterMCP(&PanicHandlers{})
	r.EnableMCP(&PanicHandlers{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"Weird": {Name: "weird"},
	}})
	s := aprot.NewServer(r, aprot.ServerOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	a := NewAdapter(s, Options{ServerName: "panic-test"})

	resp, code := rpc(t, a, "1", "tools/call", `{"name":"weird","arguments":{}}`)
	if code != http.StatusOK {
		t.Fatalf("expected a JSON-RPC response, got HTTP %d: %v", code, resp)
	}
	e, _ := resp["error"].(map[string]any)
	if e == nil || e["code"] != float64(-32603) {
		t.Fatalf("expected internal JSON-RPC error: %v", resp)
	}
	if resp["id"] != float64(1) {
		t.Errorf("error must correlate to the request id: %v", resp)
	}
	if strings.Contains(fmt.Sprint(e["message"]), "secret-dsn") {
		t.Errorf("panic value leaked to the client: %v", e)
	}
}
