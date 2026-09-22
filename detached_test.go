package aprot

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http/httptest"
	"testing"
)

// DetachedProbeHandler reports what the handler sees about its own
// connection, so the socket path can be checked end to end.
type DetachedProbeHandler struct{}

type ProbeRequest struct{}

type ProbeResponse struct {
	Detached bool `json:"detached"`
}

func (h *DetachedProbeHandler) Probe(ctx context.Context, _ *ProbeRequest) (*ProbeResponse, error) {
	return &ProbeResponse{Detached: Connection(ctx).Detached()}, nil
}

// TestConnDetached covers the accessor on both kinds of connection: one
// built by NewDetachedConn, and a real socket.
func TestConnDetached(t *testing.T) {
	t.Run("detached conn reports true", func(t *testing.T) {
		server := NewServer(NewRegistry())
		if got := server.NewDetachedConn().Detached(); !got {
			t.Errorf("NewDetachedConn().Detached() = %v, want true", got)
		}
	})

	t.Run("transport-backed conn reports false", func(t *testing.T) {
		if got := NewTestPushConn(1).Conn.Detached(); got {
			t.Errorf("transport-backed conn Detached() = %v, want false", got)
		}
	})

	t.Run("socket conn reports false to its handler", func(t *testing.T) {
		registry := NewRegistry()
		registry.Register(&DetachedProbeHandler{})
		ts := httptest.NewServer(NewServer(registry))
		defer ts.Close()

		ws := connectWS(t, ts)
		defer ws.Close()

		req := IncomingMessage{
			Type:   TypeRequest,
			ID:     "1",
			Method: "DetachedProbeHandler.Probe",
			Params: jsontext.Value(`[{}]`),
		}
		if err := ws.WriteJSON(req); err != nil {
			t.Fatalf("write failed: %v", err)
		}

		var resp ResponseMessage
		if err := ws.ReadJSON(&resp); err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if resp.Type != TypeResponse {
			t.Fatalf("got %s frame, want a response: %+v", resp.Type, resp)
		}

		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var result ProbeResponse
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if result.Detached {
			t.Errorf("socket conn Detached() = true, want false")
		}
	})
}
