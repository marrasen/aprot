package aprot

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/go-json-experiment/json"
)

func TestSendResponseByteSliceStaysJSON(t *testing.T) {
	rt := &recordingTransport{}
	c := &Conn{transport: rt, requests: make(map[string]context.CancelCauseFunc)}

	c.sendResponse("req-1", []byte("hello"))

	messages := rt.Messages()
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	var msg struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(messages[0], &msg); err != nil {
		t.Fatalf("expected JSON response, got %q: %v", messages[0], err)
	}
	if msg.Type != "response" || msg.ID != "req-1" {
		t.Fatalf("unexpected envelope: %+v", msg)
	}
	if msg.Result != "aGVsbG8=" {
		t.Fatalf("result = %q, want base64 %q", msg.Result, "aGVsbG8=")
	}
}

func TestSendResponseNilBlobPointerIsJSONNull(t *testing.T) {
	rt := &recordingTransport{}
	c := &Conn{transport: rt, requests: make(map[string]context.CancelCauseFunc)}

	c.sendResponse("req-1", (*Blob)(nil))

	messages := rt.Messages()
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	var msg struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(messages[0], &msg); err != nil {
		t.Fatalf("expected JSON response, got %q: %v", messages[0], err)
	}
	if msg.Result != nil {
		t.Fatalf("result = %v, want null", msg.Result)
	}
}

// noBinaryRecordingTransport records sends but reports no binary support,
// mirroring the SSE and stream transports.
type noBinaryRecordingTransport struct {
	recordingTransport
}

func (t *noBinaryRecordingTransport) SupportsBinary() bool { return false }

func TestSendResponseBlobFallsBackToJSONWithoutBinarySupport(t *testing.T) {
	rt := &noBinaryRecordingTransport{}
	c := &Conn{transport: rt, requests: make(map[string]context.CancelCauseFunc)}

	c.sendResponse("req-1", Blob{ContentType: "text/plain", Data: []byte("hello")})

	messages := rt.Messages()
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
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
	if err := json.Unmarshal(messages[0], &msg); err != nil {
		t.Fatalf("expected JSON response, got %q: %v", messages[0], err)
	}
	if msg.Type != "response" || msg.ID != "req-1" {
		t.Fatalf("unexpected envelope: %+v", msg)
	}
	if msg.Result.Blob == nil {
		t.Fatalf("result missing $blob marker: %s", messages[0])
	}
	if msg.Result.Blob.ContentType != "text/plain" || string(msg.Result.Blob.Data) != "hello" {
		t.Fatalf("unexpected $blob payload: %+v", msg.Result.Blob)
	}
}

func TestSendResponseBlobUsesBinaryFrameWhenSupported(t *testing.T) {
	rt := &recordingTransport{}
	c := &Conn{transport: rt, requests: make(map[string]context.CancelCauseFunc)}

	c.sendResponse("req-1", Blob{ContentType: "text/plain", Data: []byte("hello")})

	messages := rt.Messages()
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	frame := messages[0]
	if len(frame) < 4 {
		t.Fatalf("frame too short: %d bytes", len(frame))
	}
	headerLen := int(binary.BigEndian.Uint32(frame[:4]))
	if len(frame) < 4+headerLen {
		t.Fatalf("frame header length %d exceeds frame length %d", headerLen, len(frame))
	}
	var header binaryFrameHeader
	if err := json.Unmarshal(frame[4:4+headerLen], &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.Version != binaryFrameVersion {
		t.Fatalf("header version = %d, want %d", header.Version, binaryFrameVersion)
	}
	if header.Type != "response" || header.ID != "req-1" || header.ContentType != "text/plain" {
		t.Fatalf("unexpected header: %+v", header)
	}
	if payload := string(frame[4+headerLen:]); payload != "hello" {
		t.Fatalf("payload = %q, want %q", payload, "hello")
	}
}
