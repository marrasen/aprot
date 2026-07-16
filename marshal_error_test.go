package aprot

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
)

// Handlers used by the marshal-failure tests.
type MarshalHandlers struct{}

type NaNResponse struct {
	Value float64 `json:"value"`
}

func (h *MarshalHandlers) NaN(ctx context.Context, req *EchoRequest) (*NaNResponse, error) {
	return &NaNResponse{Value: math.NaN()}, nil
}

func (h *MarshalHandlers) Echo(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: req.Message}, nil
}

func (h *MarshalHandlers) SubscribeNaN(ctx context.Context) (*NaNResponse, error) {
	return &NaNResponse{Value: math.NaN()}, nil
}

func setupMarshalServer(t *testing.T) *httptest.Server {
	t.Helper()
	registry := NewRegistry()
	registry.Register(&MarshalHandlers{})
	server := NewServer(registry)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

// A result that cannot be marshaled (NaN is rejected by JSON) must surface as
// an internal error response instead of silently dropping the frame and
// leaving the client's request pending forever.
func TestResponseMarshalFailureSendsError(t *testing.T) {
	ts := setupMarshalServer(t)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "MarshalHandlers.NaN", Params: jsontext.Value(`[{"message":"hi"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	errMsg := readMessageOfType(t, ws, TypeError, 3*time.Second)
	if errMsg.ID != "1" {
		t.Errorf("expected error for request 1, got %q", errMsg.ID)
	}
	if errMsg.Code != CodeInternalError {
		t.Errorf("expected CodeInternalError, got %d", errMsg.Code)
	}
	if !strings.Contains(errMsg.Message, "encode") {
		t.Errorf("expected encode mention in message, got %q", errMsg.Message)
	}

	// The same connection must still work.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "MarshalHandlers.Echo", Params: jsontext.Value(`[{"message":"still alive"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp := readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	if resp.ID != "2" {
		t.Errorf("expected response for request 2, got %q", resp.ID)
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer: the server logs from request
// goroutines while the test reads from its own.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// An encode failure must be logged server-side (with the method name) in
// addition to the CodeInternalError sent to the client — the client-facing
// error alone leaves operators with no trace in the logs.
func TestResponseMarshalFailureIsLogged(t *testing.T) {
	var logs syncBuffer
	registry := NewRegistry()
	registry.Register(&MarshalHandlers{})
	server := NewServer(registry, ServerOptions{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "MarshalHandlers.NaN", Params: jsontext.Value(`[{"message":"hi"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	errMsg := readMessageOfType(t, ws, TypeError, 3*time.Second)
	if errMsg.Code != CodeInternalError {
		t.Errorf("expected CodeInternalError, got %d", errMsg.Code)
	}

	got := logs.String()
	if !strings.Contains(got, "failed to encode response") {
		t.Errorf("expected encode failure in server log, got %q", got)
	}
	if !strings.Contains(got, "MarshalHandlers.NaN") {
		t.Errorf("expected method name in server log, got %q", got)
	}
}

// The subscription initial response goes through the same send path and must
// fail the same way.
func TestSubscribeMarshalFailureSendsError(t *testing.T) {
	ts := setupMarshalServer(t)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "s1", Method: "MarshalHandlers.SubscribeNaN"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	errMsg := readMessageOfType(t, ws, TypeError, 3*time.Second)
	if errMsg.ID != "s1" {
		t.Errorf("expected error for subscription s1, got %q", errMsg.ID)
	}
	if errMsg.Code != CodeInternalError {
		t.Errorf("expected CodeInternalError, got %d", errMsg.Code)
	}
}
