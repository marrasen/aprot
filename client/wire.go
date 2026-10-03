package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	experimentjson "github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// wireOptions matches the encoding the server applies to user data: a bare
// time.Duration travels as int64 nanoseconds, and `format:` struct tags are
// honored. Generated types copy the server's tags verbatim, so both sides
// must read them the same way.
var wireOptions = json.JoinOptions(
	jsonv1.FormatDurationAsNano(true),
	experimentjson.ExperimentalSupportFormatTag(true),
)

func marshalWire(v any) ([]byte, error) { return json.Marshal(v, wireOptions) }

func unmarshalWire(data []byte, v any) error { return json.Unmarshal(data, v, wireOptions) }

// outFrame is a client-to-server message.
type outFrame struct {
	Type   string         `json:"type"`
	ID     string         `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params jsontext.Value `json:"params,omitempty"`
	Token  string         `json:"token,omitempty"`
	Patch  bool           `json:"patch,omitempty"`
}

// inFrame is the union of every server-to-client message. Only the fields
// the frame's type uses are set.
type inFrame struct {
	Type    string           `json:"type"`
	ID      string           `json:"id"`
	Result  jsontext.Value   `json:"result"`
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    jsontext.Value   `json:"data"`
	Event   string           `json:"event"`
	Item    jsontext.Value   `json:"item"`
	Items   []jsontext.Value `json:"items"`
	Patch   jsontext.Value   `json:"patch"`
	Current *int             `json:"current"`
	Total   *int             `json:"total"`

	// Config frame fields, in milliseconds.
	ReconnectInterval    int `json:"reconnectInterval"`
	ReconnectMaxInterval int `json:"reconnectMaxInterval"`
	ReconnectMaxAttempts int `json:"reconnectMaxAttempts"`
}

// binaryFrameHeader is the JSON header of a binary frame. Layout: a 4-byte
// big-endian header length, the header, then the payload.
type binaryFrameHeader struct {
	Version     byte   `json:"version"`
	Type        string `json:"type"`
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
}

func decodeBinaryFrame(data []byte) (binaryFrameHeader, []byte, error) {
	var h binaryFrameHeader
	if len(data) < 4 {
		return h, nil, errors.New("binary frame shorter than its length prefix")
	}
	n := int64(binary.BigEndian.Uint32(data[:4]))
	rest := data[4:]
	if n > int64(len(rest)) {
		return h, nil, errors.New("binary frame header length exceeds frame")
	}
	if err := json.Unmarshal(rest[:n], &h); err != nil {
		return h, nil, fmt.Errorf("binary frame header: %w", err)
	}
	return h, rest[n:], nil
}

// wireConn is one live connection to the server.
type wireConn interface {
	// read returns the next message. binary reports a binary frame.
	read() (data []byte, binary bool, err error)
	// write sends one text message. Safe for concurrent use.
	write(data []byte) error
	close() error
}

// wsConn is a wireConn over a gorilla WebSocket.
type wsConn struct {
	ws           *websocket.Conn
	writeTimeout time.Duration
	mu           sync.Mutex
}

func dialWebSocket(ctx context.Context, url string, header http.Header, dialer *websocket.Dialer, writeTimeout time.Duration) (wireConn, error) {
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	ws, resp, err := dialer.DialContext(ctx, url, header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("aprot client: dial %s: %w (HTTP %s)", url, err, resp.Status)
		}
		return nil, fmt.Errorf("aprot client: dial %s: %w", url, err)
	}
	return &wsConn{ws: ws, writeTimeout: writeTimeout}, nil
}

func (c *wsConn) read() ([]byte, bool, error) {
	kind, data, err := c.ws.ReadMessage()
	if err != nil {
		return nil, false, err
	}
	return data, kind == websocket.BinaryMessage, nil
}

func (c *wsConn) write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeTimeout > 0 {
		_ = c.ws.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c *wsConn) close() error {
	c.mu.Lock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(time.Second))
	_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	c.mu.Unlock()
	return c.ws.Close()
}

// streamConn is a wireConn over a byte stream with newline-delimited JSON
// framing, matching the server's ServeStream.
type streamConn struct {
	rw io.ReadWriteCloser
	sc *bufio.Scanner
	mu sync.Mutex
}

func newStreamConn(rw io.ReadWriteCloser, maxMessageSize int) *streamConn {
	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 64*1024), maxMessageSize)
	return &streamConn{rw: rw, sc: sc}
}

func (c *streamConn) read() ([]byte, bool, error) {
	for c.sc.Scan() {
		line := c.sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		// The scanner reuses its buffer; frames are decoded later, so copy.
		return bytes.Clone(line), false, nil
	}
	if err := c.sc.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, io.EOF
}

func (c *streamConn) write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := make([]byte, 0, len(data)+1)
	buf = append(append(buf, data...), '\n')
	_, err := c.rw.Write(buf)
	return err
}

func (c *streamConn) close() error { return c.rw.Close() }
