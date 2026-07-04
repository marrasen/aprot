package aprot

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// Handlers used by the ServeStream tests. EchoRequest/EchoResponse and
// NotificationEvent are shared with the WebSocket integration tests.
type StreamServeHandlers struct{}

func (h *StreamServeHandlers) Echo(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: req.Message}, nil
}

type streamInfoResponse struct {
	RemoteAddr string `json:"remoteAddr"`
}

func (h *StreamServeHandlers) WhoAmI(ctx context.Context) (*streamInfoResponse, error) {
	conn := Connection(ctx)
	return &streamInfoResponse{RemoteAddr: conn.Info().RemoteAddr}, nil
}

// streamTestClient drives the client end of a ServeStream connection using
// newline-delimited JSON frames, mirroring what an Electron/stdio bridge
// would send.
type streamTestClient struct {
	t    *testing.T
	conn net.Conn
	sc   *bufio.Scanner
}

func newStreamTestClient(t *testing.T, conn net.Conn) *streamTestClient {
	t.Helper()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &streamTestClient{t: t, conn: conn, sc: sc}
}

func (c *streamTestClient) send(msg IncomingMessage) {
	c.t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatalf("marshal frame: %v", err)
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

// readFrame reads the next newline-delimited JSON frame.
func (c *streamTestClient) readFrame() map[string]any {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !c.sc.Scan() {
		c.t.Fatalf("read frame: %v", c.sc.Err())
	}
	var frame map[string]any
	if err := json.Unmarshal(c.sc.Bytes(), &frame); err != nil {
		c.t.Fatalf("unmarshal frame %q: %v", c.sc.Text(), err)
	}
	return frame
}

// readFrameOfType reads frames until one of the given type arrives, skipping
// unrelated frames (e.g. config or push frames interleaved with responses).
func (c *streamTestClient) readFrameOfType(msgType string) map[string]any {
	c.t.Helper()
	for i := 0; i < 20; i++ {
		frame := c.readFrame()
		if frame["type"] == msgType {
			return frame
		}
	}
	c.t.Fatalf("no frame of type %q within 20 frames", msgType)
	return nil
}

// startStreamServer wires a Server to one end of a net.Pipe via ServeStream
// and returns the client end plus a channel carrying ServeStream's result.
func startStreamServer(ctx context.Context, server *Server, info ConnInfo) (net.Conn, chan error) {
	serverEnd, clientEnd := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ServeStream(ctx, serverEnd, info)
	}()
	return clientEnd, errCh
}

func newStreamTestServer(t *testing.T) *Server {
	t.Helper()
	registry := NewRegistry()
	handlers := &StreamServeHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, &NotificationEvent{})
	server := NewServer(registry)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})
	return server
}

func TestServeStreamEcho(t *testing.T) {
	server := newStreamTestServer(t)
	clientEnd, _ := startStreamServer(context.Background(), server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)

	// The first frame must be the config message, matching the WS transport.
	cfg := client.readFrame()
	if cfg["type"] != "config" {
		t.Fatalf("first frame type = %v, want config", cfg["type"])
	}

	client.send(IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "StreamServeHandlers.Echo",
		Params: jsontext.Value(`[{"message":"hello"}]`),
	})
	resp := client.readFrameOfType("response")
	if resp["id"] != "1" {
		t.Errorf("response id = %v, want 1", resp["id"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["message"] != "hello" {
		t.Errorf("result.message = %v, want hello", result["message"])
	}
}

func TestServeStreamConnInfo(t *testing.T) {
	server := newStreamTestServer(t)
	clientEnd, _ := startStreamServer(context.Background(), server, ConnInfo{RemoteAddr: "child-process"})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)

	client.send(IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "StreamServeHandlers.WhoAmI",
	})
	resp := client.readFrameOfType("response")
	result, _ := resp["result"].(map[string]any)
	if result["remoteAddr"] != "child-process" {
		t.Errorf("remoteAddr = %v, want child-process", result["remoteAddr"])
	}
}

func TestServeStreamConnectHookReject(t *testing.T) {
	server := newStreamTestServer(t)
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		return ErrForbidden("not welcome")
	})

	clientEnd, errCh := startStreamServer(context.Background(), server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)

	frame := client.readFrameOfType("error")
	if frame["message"] != "not welcome" {
		t.Errorf("error message = %v, want 'not welcome'", frame["message"])
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("ServeStream returned nil, want rejection error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not return after rejection")
	}
}

func TestServeStreamServerStopping(t *testing.T) {
	server := newStreamTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	if err := server.ServeStream(context.Background(), serverEnd, ConnInfo{}); err == nil {
		t.Error("ServeStream on stopped server returned nil, want error")
	}
}

func TestServeStreamPush(t *testing.T) {
	server := newStreamTestServer(t)
	clientEnd, _ := startStreamServer(context.Background(), server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)

	// Read the config frame so the connection is known to be registered
	// before broadcasting.
	client.readFrameOfType("config")
	waitForConnections(t, server, 1)

	server.Broadcast(&NotificationEvent{Message: "ping"})
	frame := client.readFrameOfType("push")
	data, _ := frame["data"].(map[string]any)
	if data["message"] != "ping" {
		t.Errorf("push data.message = %v, want ping", data["message"])
	}
}

func TestServeStreamClientClose(t *testing.T) {
	server := newStreamTestServer(t)
	clientEnd, errCh := startStreamServer(context.Background(), server, ConnInfo{})
	client := newStreamTestClient(t, clientEnd)
	client.readFrameOfType("config")
	waitForConnections(t, server, 1)

	_ = clientEnd.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ServeStream returned %v on client close, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not return after client close")
	}
	waitForConnections(t, server, 0)
}

func TestServeStreamContextCancel(t *testing.T) {
	server := newStreamTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	clientEnd, errCh := startStreamServer(ctx, server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)
	client.readFrameOfType("config")
	waitForConnections(t, server, 1)

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ServeStream returned %v on ctx cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not return after ctx cancel")
	}
	waitForConnections(t, server, 0)
}

func TestServeStreamAuth(t *testing.T) {
	server := newStreamTestServer(t)
	server.OnAuth(func(ctx context.Context, conn *Conn, token string) error {
		if token != "sesame" {
			return ErrAuthFailed("bad token")
		}
		return nil
	})

	clientEnd, _ := startStreamServer(context.Background(), server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)
	client.readFrameOfType("config")

	// A request before auth must be rejected with auth_error.
	client.send(IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "StreamServeHandlers.Echo",
		Params: jsontext.Value(`[{"message":"early"}]`),
	})
	client.readFrameOfType("auth_error")

	// Authenticate, then the same request must succeed.
	client.send(IncomingMessage{Type: TypeAuth, Token: "sesame"})
	client.readFrameOfType("auth_ok")

	client.send(IncomingMessage{
		Type:   TypeRequest,
		ID:     "2",
		Method: "StreamServeHandlers.Echo",
		Params: jsontext.Value(`[{"message":"late"}]`),
	})
	resp := client.readFrameOfType("response")
	result, _ := resp["result"].(map[string]any)
	if result["message"] != "late" {
		t.Errorf("result.message = %v, want late", result["message"])
	}
}

// waitForConnections polls until the server reports n connections.
func waitForConnections(t *testing.T, server *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if server.ConnectionCount() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("connection count = %d, want %d", server.ConnectionCount(), n)
}
