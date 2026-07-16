package aprot

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
)

// Handlers used by the `format:` tag tests. The codegen requires a json
// format option on time.Duration fields (see collectInterfaceFields), so the
// runtime must accept them: json/v2 snapshots since 2026-06 reject
// format-tagged fields unless ExperimentalSupportFormatTag is set.
type FormatTagHandlers struct{}

type DurationPayload struct {
	D time.Duration `json:"d,format:nano"`
}

func (h *FormatTagHandlers) EchoDuration(ctx context.Context, req *DurationPayload) (*DurationPayload, error) {
	return &DurationPayload{D: req.D}, nil
}

// marshalJSON / unmarshalJSON must honor `format:` struct tags: nano-tagged
// durations encode as int64 nanoseconds and decode back.
func TestMarshalJSONFormatTagDuration(t *testing.T) {
	data, err := marshalJSON(DurationPayload{D: 1500 * time.Millisecond})
	if err != nil {
		t.Fatalf("marshalJSON failed: %v", err)
	}
	if string(data) != `{"d":1500000000}` {
		t.Fatalf("marshaled %s, want {\"d\":1500000000}", data)
	}

	var back DurationPayload
	if err := unmarshalJSON(data, &back); err != nil {
		t.Fatalf("unmarshalJSON failed: %v", err)
	}
	if back.D != 1500*time.Millisecond {
		t.Fatalf("round-tripped %v, want 1.5s", back.D)
	}
}

// A handler whose params and result carry a format-tagged time.Duration must
// round-trip over the wire: params decode through unmarshalJSON, the result
// encodes through marshalJSON, and the client sees int64 nanoseconds.
func TestFormatTagDurationRoundTrip(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&FormatTagHandlers{})
	server := NewServer(registry)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "FormatTagHandlers.EchoDuration", Params: jsontext.Value(`[{"d":1500000000}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp struct {
		Type   MessageType `json:"type"`
		ID     string      `json:"id"`
		Result struct {
			D int64 `json:"d"`
		} `json:"result"`
	}
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if resp.Type != TypeResponse || resp.ID != "1" {
		t.Fatalf("unexpected envelope: %+v", resp)
	}
	if resp.Result.D != int64(1500*time.Millisecond) {
		t.Fatalf("result.d = %d, want %d nanoseconds", resp.Result.D, int64(1500*time.Millisecond))
	}
}
