package aprot

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"math"
	"sync"
	"sync/atomic"

	"github.com/go-json-experiment/json"
)

// streamTransport adapts any bidirectional byte stream — stdio pipes to a
// child process, a Unix domain socket, a Windows named pipe, an arbitrary
// net.Conn — to the aprot protocol using newline-delimited JSON framing:
// exactly one protocol message per line, in both directions.
type streamTransport struct {
	noBinary
	rw   io.ReadWriteCloser
	send chan []byte
	done chan struct{} // closed once to signal shutdown; makes Send a no-op
	// closeOnce guards done/rw teardown: Close may be called by the server
	// (Stop, run loop) and by the ctx watcher in ServeStream concurrently.
	closeOnce sync.Once
	opts      ServerOptions
	// conn is a back-reference used only to report send-buffer pressure to
	// the server's observer. Set after construction, before the pumps start.
	conn *Conn
}

func newStreamTransport(rw io.ReadWriteCloser, opts ServerOptions) *streamTransport {
	return &streamTransport{
		rw:   rw,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
		opts: opts,
	}
}

// observer returns the server's observer via the connection back-reference,
// or nil when there is no connection or no observer registered.
func (t *streamTransport) observer() Observer {
	if t.conn == nil || t.conn.server == nil {
		return nil
	}
	return t.conn.server.observer
}

func (t *streamTransport) Send(data []byte) error {
	// Blocking send — waits for room in the buffer, unblocking immediately if
	// the transport is closed. Same delivery guarantees as the WebSocket
	// transport: nothing is silently dropped. Unlike WebSocket there is no
	// write deadline (io.Writer has none), so a peer that stops reading
	// stalls senders until the stream is closed; local pipe peers are
	// expected to be managed by the host process.
	select {
	case <-t.done:
		return ErrConnectionClosed
	case t.send <- data:
		return nil
	default:
		t.reportBufferFull()
	}
	select {
	case <-t.done:
		return ErrConnectionClosed
	case t.send <- data:
		return nil
	}
}

func (t *streamTransport) SendCtx(ctx context.Context, data []byte) error {
	select {
	case <-t.done:
		return ErrConnectionClosed
	case t.send <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		t.reportBufferFull()
	}
	select {
	case <-t.done:
		return ErrConnectionClosed
	case t.send <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reportBufferFull signals send-buffer backpressure to the observer. The
// frame is not dropped; the caller falls back to a blocking send.
func (t *streamTransport) reportBufferFull() {
	if o := t.observer(); o != nil {
		o.SendBufferFull(t.conn)
	}
}

// Close closes the underlying stream, which also unblocks the read loop.
func (t *streamTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.done)
		_ = t.rw.Close()
	})
	return nil
}

// CloseGracefully is identical to Close: the framing has no close frame, so
// the peer detects shutdown via EOF.
func (t *streamTransport) CloseGracefully() error {
	return t.Close()
}

// writePump writes messages from the send channel to the stream, one JSON
// document per line. It exits when done is closed or a write fails.
func (t *streamTransport) writePump() {
	defer t.Close()

	for {
		select {
		case <-t.done:
			return
		case data := <-t.send:
			if _, err := t.rw.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}
}

// readLoop reads newline-delimited frames and dispatches them to the
// connection. It returns when the stream is closed or errors (including a
// line exceeding MaxMessageSize), then unregisters the connection.
func (t *streamTransport) readLoop(conn *Conn) {
	defer func() {
		// Guard with server.done: if run() has already exited, nobody will
		// ever read unregister and ServeStream would never return.
		select {
		case conn.server.unregister <- conn:
		case <-conn.server.done:
		}
		_ = t.Close()
	}()

	maxSize := t.opts.MaxMessageSize
	if maxSize <= 0 || maxSize > math.MaxInt32 {
		maxSize = math.MaxInt32
	}
	sc := bufio.NewScanner(t.rw)
	sc.Buffer(make([]byte, 64*1024), int(maxSize))

	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		// Copy the line: dispatch runs handlers on their own goroutines with
		// the raw params aliasing this buffer, and the scanner reuses it on
		// the next Scan.
		data := make([]byte, len(line))
		copy(data, line)
		conn.handleIncomingMessage(data)
	}
}

// writeStreamFrame marshals v and writes it directly to w as one line.
// Used for the config/rejection frames sent before the write pump starts.
func writeStreamFrame(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// ServeStream serves a single aprot connection over rw using
// newline-delimited JSON framing: one protocol message per line, in both
// directions. It gives the server a transport-agnostic entry point for
// byte streams the HTTP handlers can't reach — the stdio pipes of a child
// process (e.g. a Go backend spawned by an Electron or Tauri app), a Unix
// domain socket, or a Windows named pipe:
//
//	// stdio: the parent process is the single client.
//	err := server.ServeStream(ctx, struct {
//	    io.Reader
//	    io.WriteCloser
//	}{os.Stdin, os.Stdout}, aprot.ConnInfo{})
//
//	// listener (Unix domain socket, named pipe, TCP):
//	for {
//	    c, err := ln.Accept()
//	    if err != nil { break }
//	    go server.ServeStream(ctx, c, aprot.ConnInfo{RemoteAddr: c.RemoteAddr().String()})
//	}
//
// The caller supplies ConnInfo (all fields optional) since there is no HTTP
// request to capture it from. The connection participates fully in the
// server: connect/disconnect hooks, first-message auth, middleware,
// subscriptions, streaming, push, and Stop all behave as they do for
// WebSocket connections.
//
// ServeStream blocks until the connection ends and returns nil on a normal
// close (peer EOF, ctx canceled, server Stop). It returns an error if the
// connection was refused: the connect-hook rejection error, or
// ErrServerShutdown when the server is stopping.
//
// Transport notes: MaxMessageSize bounds inbound line length (an oversized
// line closes the connection), but PingInterval/PongTimeout/WriteTimeout do
// not apply — a raw byte stream has no ping frames or write deadlines.
// Liveness is the stream's own lifetime: manage the peer process or socket
// and cancel ctx (or close rw) to end the connection.
func (s *Server) ServeStream(ctx context.Context, rw io.ReadWriteCloser, info ConnInfo) error {
	if s.stopping.Load() {
		_ = rw.Close()
		return ErrServerShutdown
	}

	connID := atomic.AddUint64(&s.nextConnID, 1)
	st := newStreamTransport(rw, s.options)
	conn := newConn(st, s, connID, info, ctx)
	// Back-reference so the transport can report send-buffer pressure to the
	// observer. Set before the pumps start.
	st.conn = conn

	// Enqueue the config frame before the connect hooks run, so it is first
	// on the wire ahead of anything a hook pushes. Enqueuing (rather than a
	// direct write, as the WS transport does) keeps setup from blocking on a
	// peer that isn't reading yet — a raw stream has no write deadline to
	// bound a direct write. The send buffer is empty here, so this never
	// blocks; the write pump flushes it once the connection is accepted.
	if cfgData, err := json.Marshal(configMessage(s.options)); err == nil {
		_ = st.Send(cfgData)
	}

	if err := s.runConnectHooks(ctx, conn); err != nil {
		// A hook may have called SetUserID before a later hook rejected the
		// connection; undo that association so a dead conn can't linger in
		// userConns and have a later PushToUser block on its send buffer.
		s.disassociateUser(conn)
		_ = writeStreamFrame(rw, connectionRejectedMessage(err))
		_ = rw.Close()
		return err
	}

	// Register the connection, but don't block forever if the server has
	// already shut down (run() has exited and will never read s.register).
	select {
	case s.register <- conn:
	case <-s.done:
		s.disassociateUser(conn)
		_ = rw.Close()
		return ErrServerShutdown
	}

	// When an auth hook is registered, the connection is pending until it
	// sends a valid auth frame; close it if that doesn't happen in time.
	if s.authRequired() {
		conn.armAuthTimeout(s.options.AuthTimeout)
	}

	// Tie the connection to ctx: canceling it closes the stream, which
	// unblocks the read loop below.
	stop := context.AfterFunc(ctx, func() { conn.close() })
	defer stop()

	go st.writePump()
	st.readLoop(conn)
	return nil
}
