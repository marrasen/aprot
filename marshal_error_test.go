package aprot

import (
	"context"
	"math"
	"net/http/httptest"
	"strings"
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
