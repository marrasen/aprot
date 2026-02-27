package tasks

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
)

// eventOrderHandler is a handler used in event-order integration tests.
type eventOrderHandler struct {
	server *aprot.Server
}

func (h *eventOrderHandler) StartShared(ctx context.Context, title string) error {
	ctx, task := StartTask[struct{}](ctx, title, Shared())
	if task == nil {
		return aprot.NewError(aprot.CodeInternalError, "task not created")
	}
	Output(ctx, "hello")
	TaskProgress(ctx, 1, 2)
	task.Close()
	return nil
}

// setupEventOrderServer creates a real HTTP server with tasks enabled and
// returns the httptest.Server and a cleanup function.
func setupEventOrderServer(t *testing.T) *httptest.Server {
	t.Helper()

	registry := aprot.NewRegistry()
	handler := &eventOrderHandler{}
	registry.Register(handler)
	Enable(registry)

	server := aprot.NewServer(registry)
	handler.server = server

	ts := httptest.NewServer(server)
	t.Cleanup(func() {
		ts.Close()
		server.Stop(context.Background()) //nolint:errcheck
	})
	return ts
}

// connectEventOrderWS opens a WS connection and discards the initial config message.
func connectEventOrderWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	// Discard the config message sent on connect.
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read config message: %v", err)
	}
	return ws
}

// rawPush is a minimal struct to parse just the type and event fields of any
// message off the wire.
type rawPush struct {
	Type  string `json:"type"`
	Event string `json:"event"`
}

// taskStatePush is the expected shape of a TaskStateEvent push message's data.
type taskStatePush struct {
	Tasks []SharedTaskState `json:"tasks"`
}

// readAllPushMessages sends a request and reads all messages until a response
// or error comes back for that request ID, collecting push messages.
func readAllPushMessages(t *testing.T, ws *websocket.Conn, reqID string) (pushes []rawPushWithData) {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage failed: %v", err)
		}

		var base struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(data, &base); err != nil {
			t.Fatalf("Unmarshal base message failed: %v", err)
		}

		switch base.Type {
		case "push":
			var p rawPushWithData
			if err := json.Unmarshal(data, &p); err != nil {
				t.Fatalf("Unmarshal push failed: %v", err)
			}
			pushes = append(pushes, p)
		case "response", "error":
			if base.ID == reqID {
				return pushes
			}
		case "progress":
			// request-scoped progress — ignore
		}
	}
}

// rawPushWithData captures the full push message including raw data.
type rawPushWithData struct {
	Type  string          `json:"type"`
	Event string          `json:"event"`
	Data  jsontext.Value `json:"data"`
}

// TestSharedTaskFirstMessageIsCreated verifies that the first TaskStateEvent
// push for a newly created shared task has status "created".
func TestSharedTaskFirstMessageIsCreated(t *testing.T) {
	ts := setupEventOrderServer(t)
	ws := connectEventOrderWS(t, ts)
	defer ws.Close()

	// Send StartShared request.
	req := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "eventOrderHandler.StartShared",
		"params": []any{"test-task"},
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	pushes := readAllPushMessages(t, ws, "1")

	// Find the first TaskStateEvent push.
	var firstState *taskStatePush
	for _, p := range pushes {
		if p.Event == "TaskStateEvent" {
			var ts taskStatePush
			if err := json.Unmarshal(p.Data, &ts); err != nil {
				t.Fatalf("Unmarshal TaskStateEvent data failed: %v", err)
			}
			firstState = &ts
			break
		}
	}
	if firstState == nil {
		t.Fatal("no TaskStateEvent push received")
	}
	if len(firstState.Tasks) == 0 {
		t.Fatal("first TaskStateEvent has no tasks")
	}
	if firstState.Tasks[0].Status != TaskNodeStatusCreated {
		t.Errorf("first TaskStateEvent task status: got %q, want %q",
			firstState.Tasks[0].Status, TaskNodeStatusCreated)
	}
}

// TestSharedTaskNoUpdateBeforeCreated verifies that no TaskUpdateEvent appears
// before the first TaskStateEvent (which should show "created").
func TestSharedTaskNoUpdateBeforeCreated(t *testing.T) {
	ts := setupEventOrderServer(t)
	ws := connectEventOrderWS(t, ts)
	defer ws.Close()

	// Send StartShared request — the handler calls Output and Progress immediately.
	req := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "eventOrderHandler.StartShared",
		"params": []any{"task-with-output"},
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	pushes := readAllPushMessages(t, ws, "1")

	// Walk pushes and assert no TaskUpdateEvent before the first TaskStateEvent.
	for _, p := range pushes {
		if p.Event == "TaskStateEvent" {
			// Good — the state event came first.
			break
		}
		if p.Event == "TaskUpdateEvent" {
			t.Fatal("TaskUpdateEvent appeared before the first TaskStateEvent")
		}
	}
}
