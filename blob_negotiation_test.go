package aprot

import (
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

type BlobNegotiationHandler struct{}

func (h *BlobNegotiationHandler) GetAvatar(ctx context.Context) (Blob, error) {
	return Blob{ContentType: "text/plain", Data: []byte("hello")}, nil
}

// dialBlobNegotiation starts a server, dials it with the given query string
// (empty for none), and returns the connection plus the config frame.
func dialBlobNegotiation(t *testing.T, query string) (*websocket.Conn, ConfigMessage) {
	t.Helper()
	registry := NewRegistry()
	registry.Register(&BlobNegotiationHandler{})
	ts := httptest.NewServer(NewServer(registry))
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + query
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial %q: %v", query, err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	t.Cleanup(func() { _ = ws.Close() })

	var cfg ConfigMessage
	if err := ws.ReadJSON(&cfg); err != nil {
		t.Fatalf("read config: %v", err)
	}
	return ws, cfg
}

func requestAvatar(t *testing.T, ws *websocket.Conn) (messageType int, data []byte) {
	t.Helper()
	req := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "BlobNegotiationHandler.GetAvatar",
		"params": []any{},
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	mt, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return mt, data
}

// Without the parameter, nothing changes: WebSocket clients keep getting the
// efficient binary frame, and the config frame says so.
func TestWSBlobDefaultsToBinaryFrame(t *testing.T) {
	ws, cfg := dialBlobNegotiation(t, "")
	if !cfg.BinaryFrames {
		t.Fatalf("config binaryFrames = false, want true by default")
	}

	mt, data := requestAvatar(t, ws)
	if mt != websocket.BinaryMessage {
		t.Fatalf("message type = %d, want binary (%d)", mt, websocket.BinaryMessage)
	}
	headerLen := int(binary.BigEndian.Uint32(data[:4]))
	var header binaryFrameHeader
	if err := json.Unmarshal(data[4:4+headerLen], &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.ID != "1" || header.ContentType != "text/plain" {
		t.Fatalf("unexpected header: %+v", header)
	}
	if payload := string(data[4+headerLen:]); payload != "hello" {
		t.Fatalf("payload = %q, want %q", payload, "hello")
	}
}

// A client that declines binary gets the JSON $blob envelope on a text frame —
// the same representation SSE and stream already use — instead of a binary
// frame it would silently drop.
func TestWSBinaryOptOutDeliversJSONBlobEnvelope(t *testing.T) {
	ws, cfg := dialBlobNegotiation(t, "?binary=0")
	if cfg.BinaryFrames {
		t.Fatalf("config binaryFrames = true, want false after opting out")
	}

	mt, data := requestAvatar(t, ws)
	if mt != websocket.TextMessage {
		t.Fatalf("message type = %d, want text (%d): %q", mt, websocket.TextMessage, data)
	}
	var msg struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Result struct {
			Blob *struct {
				ContentType string `json:"contentType"`
				Data        []byte `json:"data"`
			} `json:"$blob"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("expected JSON response, got %q: %v", data, err)
	}
	if msg.Type != "response" || msg.ID != "1" {
		t.Fatalf("unexpected envelope: %+v", msg)
	}
	if msg.Result.Blob == nil {
		t.Fatalf("result missing $blob marker: %s", data)
	}
	if msg.Result.Blob.ContentType != "text/plain" || string(msg.Result.Blob.Data) != "hello" {
		t.Fatalf("unexpected $blob payload: %+v", msg.Result.Blob)
	}
}

// A typo must not quietly fall back to binary frames — that would reinstate
// the exact silent hang the parameter exists to prevent.
func TestWSInvalidBinaryParamRejectsUpgrade(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&BlobNegotiationHandler{})
	ts := httptest.NewServer(NewServer(registry))
	defer ts.Close()

	for _, value := range []string{"fasle", "", "2"} {
		wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "?binary=" + value
		ws, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err == nil {
			_ = ws.Close()
			t.Fatalf("binary=%q: upgrade succeeded, want rejection", value)
		}
		if resp == nil {
			t.Fatalf("binary=%q: no HTTP response: %v", value, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("binary=%q: status = %d, want %d", value, resp.StatusCode, http.StatusBadRequest)
		}
	}
}

func TestWSBinaryFromRequest(t *testing.T) {
	tests := []struct {
		query   string
		want    bool
		wantErr bool
	}{
		{query: "", want: true},
		{query: "?binary=1", want: true},
		{query: "?binary=true", want: true},
		{query: "?binary=ON", want: true},
		{query: "?binary=0", want: false},
		{query: "?binary=false", want: false},
		{query: "?binary=No", want: false},
		{query: "?binary=off", want: false},
		{query: "?binary=", wantErr: true},
		{query: "?binary=maybe", wantErr: true},
		{query: "?other=0", want: true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/ws"+tt.query, nil)
		got, err := wsBinaryFromRequest(r)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %v", tt.query, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tt.query, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%q: got %v, want %v", tt.query, got, tt.want)
		}
	}
}

// SupportsBinary is the documented gate, but a missed check must not put an
// undecodable frame on the wire.
func TestWSTransportSendBinaryRefusedAfterOptOut(t *testing.T) {
	server, _ := newWSPair(t)
	tr := newWSTransport(server, ServerOptions{}, false)

	if tr.SupportsBinary() {
		t.Fatalf("SupportsBinary = true after opting out")
	}
	if err := tr.SendBinary([]byte("frame")); err != errBinaryUnsupported {
		t.Fatalf("SendBinary error = %v, want %v", err, errBinaryUnsupported)
	}
	if err := tr.SendBinaryCtx(context.Background(), []byte("frame")); err != errBinaryUnsupported {
		t.Fatalf("SendBinaryCtx error = %v, want %v", err, errBinaryUnsupported)
	}
}
