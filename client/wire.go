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
	"os"
	"sync"
	"sync/atomic"
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
	Type    string         `json:"type"`
	ID      string         `json:"id"`
	Result  jsontext.Value `json:"result"`
	Code    int            `json:"code"`
	Message string         `json:"message"`
	// Timeout marks the server's pending-auth timeout auth_error.
	Timeout bool             `json:"timeout"`
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
	// MaxMessageSize is the largest inbound message the server accepts, in
	// bytes. Absent (0) from servers that predate it or have no limit.
	MaxMessageSize int64 `json:"maxMessageSize"`
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
	// write sends one text message. Safe for concurrent use. A message
	// larger than the outbound limit is refused with an error wrapping
	// ErrMessageTooLarge, and the connection stays up.
	write(data []byte) error
	// setOutboundLimit sets the largest message the server accepts, from
	// its config frame. 0 means no limit.
	setOutboundLimit(n int64)
	close() error
}

// outboundLimit is the server's advertised inbound limit, shared by both
// wireConn implementations.
type outboundLimit struct{ max atomic.Int64 }

func (l *outboundLimit) setOutboundLimit(n int64) {
	if n < 0 {
		n = 0
	}
	l.max.Store(n)
}

// check refuses a message of size bytes on the wire (framing included).
func (l *outboundLimit) check(size int) error {
	if max := l.max.Load(); max > 0 && int64(size) > max {
		return fmt.Errorf("%w: the message is %d bytes, and the server's MaxMessageSize is %d bytes", ErrMessageTooLarge, size, max)
	}
	return nil
}

// wsConn is a wireConn over a gorilla WebSocket.
type wsConn struct {
	outboundLimit
	ws           *websocket.Conn
	writeTimeout time.Duration
	mu           sync.Mutex // serializes data writes; gorilla allows one writer

	// Keepalive. pingInterval 0 disables it. A read deadline of
	// 2 × pingInterval, pushed forward by every message and pong, turns a
	// silent (half-open) connection into a read error.
	pingInterval time.Duration
	stopPing     chan struct{}
	closeOnce    sync.Once
}

func dialWebSocket(ctx context.Context, url string, opts *Options, writeTimeout, pingInterval time.Duration) (wireConn, error) {
	dialer := &websocket.Dialer{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: opts.TLSConfig,
		NetDialContext:  opts.NetDialContext,
	}
	ws, resp, err := dialer.DialContext(ctx, url, opts.Header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("aprot client: dial %s: %w (HTTP %s)", url, err, resp.Status)
		}
		return nil, fmt.Errorf("aprot client: dial %s: %w", url, err)
	}
	c := &wsConn{ws: ws, writeTimeout: writeTimeout, pingInterval: pingInterval, stopPing: make(chan struct{})}
	if pingInterval > 0 {
		c.extendReadDeadline()
		ws.SetPongHandler(func(string) error {
			c.extendReadDeadline()
			return nil
		})
		go c.pingLoop()
	}
	return c, nil
}

func (c *wsConn) extendReadDeadline() {
	_ = c.ws.SetReadDeadline(time.Now().Add(2 * c.pingInterval))
}

// pingLoop sends a ping every pingInterval until the connection closes.
// WriteControl may run concurrently with WriteMessage, so it does not take
// c.mu, and a slow data write cannot hold up a ping.
func (c *wsConn) pingLoop() {
	t := time.NewTicker(c.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			deadline := time.Now().Add(c.pingInterval)
			if c.writeTimeout > 0 && c.writeTimeout < c.pingInterval {
				deadline = time.Now().Add(c.writeTimeout)
			}
			// A failed ping needs no handling here: a broken connection
			// fails the read loop, or the read deadline does.
			_ = c.ws.WriteControl(websocket.PingMessage, nil, deadline)
		case <-c.stopPing:
			return
		}
	}
}

func (c *wsConn) read() ([]byte, bool, error) {
	kind, data, err := c.ws.ReadMessage()
	if err != nil {
		// A failed read ends the connection: gorilla allows no further
		// reads. Stop the pings and release the socket here, because
		// nothing else closes a connection that dropped on its own.
		c.closeOnce.Do(func() { close(c.stopPing) })
		_ = c.ws.Close()
		if c.pingInterval > 0 && errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, false, fmt.Errorf("aprot client: no message or pong from the server for %v (keepalive): %w", 2*c.pingInterval, err)
		}
		return nil, false, err
	}
	if c.pingInterval > 0 {
		c.extendReadDeadline()
	}
	return data, kind == websocket.BinaryMessage, nil
}

func (c *wsConn) write(data []byte) error {
	if err := c.check(len(data)); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeTimeout > 0 {
		_ = c.ws.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c *wsConn) close() error {
	c.closeOnce.Do(func() { close(c.stopPing) })
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	return c.ws.Close()
}

// streamConn is a wireConn over a byte stream with newline-delimited JSON
// framing, matching the server's ServeStream.
type streamConn struct {
	outboundLimit
	rw io.ReadWriteCloser
	sc *bufio.Scanner
	mu sync.Mutex
}

func newStreamConn(rw io.ReadWriteCloser, maxMessageSize int) *streamConn {
	sc := bufio.NewScanner(rw)
	// The scanner's limit covers the line and its newline, so a message of
	// exactly maxMessageSize bytes needs one more. The server does the same.
	sc.Buffer(make([]byte, min(64*1024, maxMessageSize+1)), maxMessageSize+1)
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
	// The stream is finished either way; release it, because nothing else
	// closes a connection that dropped on its own.
	_ = c.rw.Close()
	if err := c.sc.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, io.EOF
}

func (c *streamConn) write(data []byte) error {
	// The server's line scanner must hold the line and its newline.
	if err := c.check(len(data) + 1); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := make([]byte, 0, len(data)+1)
	buf = append(append(buf, data...), '\n')
	_, err := c.rw.Write(buf)
	return err
}

func (c *streamConn) close() error { return c.rw.Close() }
