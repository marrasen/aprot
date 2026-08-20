package tasks

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/mcp"
)

// mcpTaskHandler exposes a tool that starts a shared task, the way a real
// MCP consumer would kick off background work.
type mcpTaskHandler struct{}

// StartShared starts a shared task that outlives the tool call and returns
// its ID, which is all a request-scoped caller gets — no delivery follows it.
func (*mcpTaskHandler) StartShared(ctx context.Context, title string) (string, error) {
	_, task := StartTask[struct{}](context.WithoutCancel(ctx), title, Shared())
	return task.ID(), nil
}

// mcpTaskEnv wires one server behind both a WebSocket endpoint and an MCP
// adapter, so a task started over MCP can be observed on a socket.
type mcpTaskEnv struct {
	ts      *httptest.Server
	adapter *mcp.Adapter
}

func setupMCPTaskEnv(t *testing.T, opts ...EnableOption) *mcpTaskEnv {
	t.Helper()
	registry := aprot.NewRegistry()
	h := &mcpTaskHandler{}
	registry.Register(h)
	Enable(registry, opts...)
	registry.EnableMCP(h, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"StartShared": {Description: "Start a shared background task."},
	}})
	// The task RPCs are reachable over MCP too, so a tool caller can poll and
	// cancel the task it started.
	registry.EnableMCP(&tasksHandler{}, aprot.MCPOptions{Tools: map[string]aprot.MCPTool{
		"ListTasks":  {ReadOnly: true},
		"CancelTask": {},
	}})

	server := aprot.NewServer(registry)
	// Socket clients authenticate as u1; the MCP wrapper below supplies the
	// same address, so both are the same user by different doors.
	server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		conn.SetUserID("u1")
		return nil
	})

	ts := httptest.NewServer(server)
	t.Cleanup(func() {
		ts.Close()
		server.Stop(context.Background()) //nolint:errcheck
	})
	return &mcpTaskEnv{ts: ts, adapter: mcp.NewAdapter(server, mcp.Options{ServerName: "tasks-test"})}
}

// callTool posts a tools/call request, simulating a wrapping http.Handler
// that authenticated the caller as userID (empty for anonymous).
func (e *mcpTaskEnv) callTool(t *testing.T, tool, args, userID string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(aprot.WithUserID(req.Context(), userID))
	}
	w := httptest.NewRecorder()
	e.adapter.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call %s: HTTP %d: %s", tool, w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("tools/call %s: invalid JSON: %v\n%s", tool, err, w.Body.String())
	}
	if e, ok := resp["error"]; ok {
		t.Fatalf("tools/call %s: JSON-RPC error: %v", tool, e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: missing result: %v", tool, resp)
	}
	return res
}

// toolText returns a tool result's text content, and whether it is an error
// result.
func toolText(t *testing.T, res map[string]any) (string, bool) {
	t.Helper()
	isErr, _ := res["isError"].(bool)
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tool result has no content: %v", res)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, isErr
}

// connectTaskWS opens a socket and discards the config frame and the
// on-connect TaskStateEvent snapshot.
func connectTaskWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	for range 2 {
		_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := ws.ReadMessage(); err != nil {
			t.Fatalf("drain connect frames: %v", err)
		}
	}
	return ws
}

// awaitTaskState reads pushes until a TaskStateEvent containing taskID
// arrives, and returns that task's state.
func awaitTaskState(t *testing.T, ws *websocket.Conn, taskID string) SharedTaskState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read push waiting for task %s: %v", taskID, err)
		}
		var msg struct {
			Type  string `json:"type"`
			Event string `json:"event"`
			Data  struct {
				Tasks []SharedTaskState `json:"tasks"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type != "push" || msg.Event != "TaskStateEvent" {
			continue
		}
		if s, ok := findTaskByID(msg.Data.Tasks, taskID); ok {
			return s
		}
	}
	t.Fatalf("no TaskStateEvent carrying task %s arrived", taskID)
	return SharedTaskState{}
}

// The regression this issue was filed for: a shared task started by an MCP
// tool registers with the server's manager and reaches socket watchers. On
// master it degraded to a detached no-op, so FindSharedTask found nothing and
// WS clients saw nothing.
func TestSharedTaskOverMCPReachesSocketClients(t *testing.T) {
	env := setupMCPTaskEnv(t)
	ws := connectTaskWS(t, env.ts)

	res := env.callTool(t, "mcp_task_handler_start_shared", `{"title":"indexing"}`, "u1")
	taskID, isErr := toolText(t, res)
	if isErr {
		t.Fatalf("tool reported an error: %s", taskID)
	}
	taskID = strings.Trim(taskID, `"`)
	if taskID == "" {
		t.Fatal("tool returned no task ID")
	}

	state := awaitTaskState(t, ws, taskID)
	if state.Title != "indexing" {
		t.Errorf("title = %q, want indexing", state.Title)
	}
	// The socket client is the same user, so the task is theirs — this is what
	// makes the cancel button render.
	if !state.IsOwner {
		t.Error("IsOwner should be true for the same user on a socket")
	}
}

// A task started over MCP can be canceled from the socket UI by the same
// user. Before #335 the snapshot said IsOwner true and the cancel was
// refused, because the default policy compared connection IDs.
func TestMCPStartedTaskCancelableFromSocket(t *testing.T) {
	env := setupMCPTaskEnv(t)
	ws := connectTaskWS(t, env.ts)

	res := env.callTool(t, "mcp_task_handler_start_shared", `{"title":"cancel-me"}`, "u1")
	text, _ := toolText(t, res)
	taskID := strings.Trim(text, `"`)
	awaitTaskState(t, ws, taskID)

	if err := ws.WriteJSON(map[string]any{
		"type":   "request",
		"id":     "c1",
		"method": "tasksHandler.CancelTask",
		"params": []any{taskID},
	}); err != nil {
		t.Fatalf("write cancel: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read cancel response: %v", err)
		}
		var f struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(data, &f); err != nil || f.ID != "c1" {
			continue
		}
		if f.Type == "error" {
			t.Fatalf("cancel refused: code=%d %s", f.Code, f.Message)
		}
		return
	}
	t.Fatal("no response to the cancel request")
}

// The same authenticated caller can cancel over MCP too: ownership is matched
// on the address, which a request-scoped transport carries.
func TestMCPStartedTaskCancelableByMCPCaller(t *testing.T) {
	env := setupMCPTaskEnv(t)

	res := env.callTool(t, "mcp_task_handler_start_shared", `{"title":"mcp-cancel"}`, "u1")
	text, _ := toolText(t, res)
	taskID := strings.Trim(text, `"`)

	cancelRes := env.callTool(t, "tasks_handler_cancel_task", fmt.Sprintf(`{"taskId":%q}`, taskID), "u1")
	if msg, isErr := toolText(t, cancelRes); isErr {
		t.Fatalf("MCP owner was refused its own task: %s", msg)
	}
}

// A different user is refused, over MCP as anywhere else.
func TestMCPTaskCancelRefusesOtherUser(t *testing.T) {
	env := setupMCPTaskEnv(t)

	res := env.callTool(t, "mcp_task_handler_start_shared", `{"title":"not-yours"}`, "u1")
	text, _ := toolText(t, res)
	taskID := strings.Trim(text, `"`)

	cancelRes := env.callTool(t, "tasks_handler_cancel_task", fmt.Sprintf(`{"taskId":%q}`, taskID), "u2")
	msg, isErr := toolText(t, cancelRes)
	if !isErr {
		t.Fatalf("a different user was allowed to cancel over MCP: %s", msg)
	}
	if !strings.Contains(msg, "task not found") {
		t.Errorf("refusal message = %q, want the anti-probing 'task not found'", msg)
	}
}

// A cancel authorizer runs on the MCP path against the request-scoped
// identity. Before #335 the call failed with CodeInternalError
// "tasks not enabled" before any authorizer was consulted.
func TestMCPTaskCancelRunsAuthorizer(t *testing.T) {
	var ran bool
	env := setupMCPTaskEnv(t, WithCancelAuthorizer(func(ctx context.Context, _ TaskCancelInfo) error {
		ran = true
		if aprot.UserID(ctx) == "" {
			return aprot.ErrForbidden("not authenticated")
		}
		return nil
	}))

	res := env.callTool(t, "mcp_task_handler_start_shared", `{"title":"authorized"}`, "u1")
	text, _ := toolText(t, res)
	taskID := strings.Trim(text, `"`)

	// An anonymous MCP caller is refused *by the authorizer*, not by a missing
	// task manager.
	anonRes := env.callTool(t, "tasks_handler_cancel_task", fmt.Sprintf(`{"taskId":%q}`, taskID), "")
	anonMsg, isErr := toolText(t, anonRes)
	if !isErr {
		t.Errorf("anonymous MCP caller was allowed to cancel: %s", anonMsg)
	}
	if strings.Contains(anonMsg, "tasks not enabled") {
		t.Errorf("cancel failed before the authorizer ran: %s", anonMsg)
	}
	if !ran {
		t.Fatal("cancel authorizer never ran on the MCP path")
	}
}

// An MCP caller polls the task it started through ListTasks — the only way a
// request-scoped caller can follow a task, since no delivery reaches it.
func TestMCPCallerPollsOwnTask(t *testing.T) {
	env := setupMCPTaskEnv(t)

	res := env.callTool(t, "mcp_task_handler_start_shared", `{"title":"pollable"}`, "u1")
	text, _ := toolText(t, res)
	taskID := strings.Trim(text, `"`)

	listRes := env.callTool(t, "tasks_handler_list_tasks", `{}`, "u1")
	body, isErr := toolText(t, listRes)
	if isErr {
		t.Fatalf("ListTasks over MCP failed: %s", body)
	}
	if !strings.Contains(body, taskID) {
		t.Errorf("ListTasks over MCP did not report task %s: %s", taskID, body)
	}
}
