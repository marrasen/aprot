package aprot

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/go-json-experiment/json"
)

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
