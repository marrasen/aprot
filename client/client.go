package client

import (
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// State is the connection state of a [Client].
type State int

const (
	// StateConnecting: the client is establishing a connection, either for
	// the first time or after the previous one dropped.
	StateConnecting State = iota
	// StateConnected: the connection is open and authenticated.
	StateConnected
	// StateClosed: the client has stopped for good, after [Client.Close], a
	// server rejection, or running out of reconnect attempts. [Client.Err]
	// says which.
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateClosed:
		return "closed"
	}
	return "State(" + strconv.Itoa(int(s)) + ")"
}

// Options configures a [Client]. The zero value is usable.
type Options struct {
	// AuthToken, if set, is called on every connect, and its token is sent
	// as the first message (the server's OnAuth hook). A rejected token
	// closes the client with an *Error carrying CodeAuthFailed, unless
	// [Options.ReconnectOnRejected] retries it.
	//
	// Without AuthToken, a server that requires auth answers the first
	// request with an auth_error. The client then closes with an *Error
	// carrying CodeAuthFailed, and pending calls fail with it.
	AuthToken func(ctx context.Context) (string, error)

	// Header is sent with the WebSocket upgrade request, for example a
	// cookie or an Authorization header. Ignored by [DialStream].
	Header http.Header

	// TLSConfig configures TLS for wss:// URLs. Nil uses the defaults.
	// Ignored by [DialStream].
	TLSConfig *tls.Config

	// NetDialContext dials the network connection under the WebSocket. Nil
	// uses a plain net.Dialer. Either way, the proxy settings from the
	// environment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY) apply; with a proxy,
	// NetDialContext dials the proxy. Ignored by [DialStream].
	NetDialContext func(ctx context.Context, network, addr string) (net.Conn, error)

	// Reconnect backoff. Attempt n waits n × ReconnectInterval, capped at
	// ReconnectMaxInterval. A value set here wins. A value left at 0 takes
	// the server's setting from its config frame, as the TypeScript client
	// does, and falls back to 1s and 10s.
	ReconnectInterval    time.Duration
	ReconnectMaxInterval time.Duration
	// ReconnectMaxAttempts caps consecutive failed reconnects before the
	// client closes. 0 takes the server's setting, which is unlimited by
	// default. A negative value means unlimited, whatever the server says.
	ReconnectMaxAttempts int
	// NoReconnect closes the client when the first connection drops,
	// instead of reconnecting.
	NoReconnect bool

	// ReconnectOnRejected retries when the server rejects a reconnect (its
	// connect hook fails, or it refuses the AuthToken), instead of closing
	// the client for good. Nil, the default, closes the client: the right
	// response to a real sign-out. Set it when a rejection is usually
	// temporary, such as a session store that is briefly down or a token
	// that AuthToken will mint fresh on the next attempt.
	//
	// It applies only after the first successful connect: a rejection
	// during [Dial] or [DialStream] is still returned. A connection
	// rejection the server sends mid-session (CodeConnectionRejected) is
	// retried the same way. While retrying, the client is in
	// StateConnecting and [Client.LastRejection] returns the rejection.
	ReconnectOnRejected *RejectedRetry

	// PingInterval is how often the client pings the server on a WebSocket
	// connection. A connection that delivers no message and no pong for
	// twice the interval is treated as dead: the client closes it and
	// reconnects. That catches a half-open connection, where the network
	// path died without either side seeing a close. Default 30s; a negative
	// value disables pings and the check.
	//
	// [DialStream] connections have no keepalive: the byte-stream framing
	// has no ping. Detect a dead stream in the dial func's transport, for
	// example with TCP keepalive.
	PingInterval time.Duration

	// ConnectTimeout bounds each connection attempt: dial, the server's
	// first frame, and auth. A server that accepts the connection but never
	// answers fails the attempt instead of stalling it. Default 10s, the
	// same as the TypeScript client.
	ConnectTimeout time.Duration

	// WriteTimeout bounds each WebSocket write. Default 10s.
	WriteTimeout time.Duration

	// MaxMessageSize bounds one inbound message on [DialStream]
	// connections. Default 32 MiB. (Outbound messages are bounded by the
	// server's own limit; see [ErrMessageTooLarge].) A larger message breaks
	// the connection, and a subscription whose result is that large makes
	// every reconnect fail the same way, so set it above the largest result
	// the server sends.
	MaxMessageSize int

	// OnStateChange, if set, is called on every state change, in order, on
	// a goroutine of its own. It may call methods on the client, including
	// Close.
	OnStateChange func(State)

	// Logger receives frames the client could not decode or route.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// RejectedRetry configures retries after the server rejects a reconnect.
// See [Options.ReconnectOnRejected]. It mirrors the TypeScript client's
// reconnectOnRejected option.
type RejectedRetry struct {
	// Delay is the wait before each retry. It is fixed, not backed off: a
	// rejection means the server is up and answered. Default 2s.
	Delay time.Duration
	// MaxAttempts caps consecutive rejections; after that the client closes
	// with the last rejection. 0 means unlimited. Any other outcome, such as
	// a network failure or a connection that drops normally, resets the
	// count.
	MaxAttempts int
}

const (
	defaultRejectedRetryDelay   = 2 * time.Second
	defaultPingInterval         = 30 * time.Second
	defaultReconnectInterval    = time.Second
	defaultReconnectMaxInterval = 10 * time.Second
	defaultWriteTimeout         = 10 * time.Second
	defaultConnectTimeout       = 10 * time.Second
	defaultMaxMessageSize       = 32 << 20
)

// Client is a connection to an aprot server. It reconnects automatically,
// re-establishes every open subscription after a reconnect, and is safe for
// concurrent use.
//
// Generated clients wrap a Client with typed methods. The generic functions
// [Call], [Subscribe], [Stream], [Stream2] and [OnPush] are the untyped-name
// layer underneath, and work on their own too.
type Client struct {
	opts   Options
	dial   func(ctx context.Context) (wireConn, error)
	logger *slog.Logger

	// authMu serializes auth handshakes, so two refreshes cannot interleave
	// their auth frames and verdicts.
	authMu sync.Mutex

	mu        sync.Mutex
	state     State
	conn      wireConn      // current connection; nil unless connected
	ready     chan struct{} // closed while connected; replaced on every drop
	closed    bool
	closeErr  error
	done      chan struct{} // closed when the client has stopped
	cancelRun context.CancelFunc
	nextID    uint64
	pending   map[string]*pendingCall
	subs      map[string]subEntry
	streams   map[string]streamEntry
	pushes    map[string]map[*pushHandler]struct{}
	authWait  chan error // non-nil while an auth frame awaits auth_ok/auth_error
	watch     *connWatch // read loop of the current connection
	notifier  stateNotifier

	// subSendMu orders subscribe and unsubscribe frames for the same
	// subscription. Without it, a Close racing the resubscribe after a
	// reconnect could send unsubscribe first, and the server would keep a
	// subscription the client has dropped.
	subSendMu sync.Mutex
	// subInFlight counts subscribe frames on the current connection still
	// waiting for their first answer; subQueue holds the ones waiting for a
	// slot. Both are reset whenever the connection changes. Guarded by mu.
	subInFlight int
	subQueue    []subEntry
	backoff     backoff // guarded by mu
	// lastRejection is the rejection being retried under
	// ReconnectOnRejected; nil once a connect succeeds. Guarded by mu.
	lastRejection *Error
	// dropRejection is a mid-session CodeConnectionRejected frame, held
	// for the run loop to retry when ReconnectOnRejected is set. Guarded by
	// mu.
	dropRejection *Error
}

// backoff holds the reconnect schedule. The fixed flags mark values set in
// Options, which the server's config frame must not override.
type backoff struct {
	interval, maxInterval time.Duration
	maxAttempts           int

	fixedInterval, fixedMaxInterval, fixedMaxAttempts bool
}

func (b backoff) delay(attempt int) time.Duration {
	d := b.interval * time.Duration(attempt)
	if d > b.maxInterval || d <= 0 {
		d = b.maxInterval
	}
	return d
}

// Dial connects to an aprot server's WebSocket endpoint, for example
// "ws://localhost:8080/ws". It returns once the first connection is open and,
// if [Options.AuthToken] is set, authenticated. If that first attempt fails,
// Dial returns the error and does not retry; after it succeeds, the client
// reconnects on its own.
func Dial(ctx context.Context, url string, opts Options) (*Client, error) {
	writeTimeout := opts.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultWriteTimeout
	}
	pingInterval := opts.PingInterval
	if pingInterval == 0 {
		pingInterval = defaultPingInterval
	} else if pingInterval < 0 {
		pingInterval = 0
	}
	return start(ctx, opts, func(ctx context.Context) (wireConn, error) {
		return dialWebSocket(ctx, url, &opts, writeTimeout, pingInterval)
	})
}

// DialStream connects over a byte stream using the server's ServeStream
// framing (newline-delimited JSON): a TCP connection, a Unix socket, or the
// stdio pipes of a child process. dial is called for the first connection
// and again for every reconnect; for a stream that cannot be reopened, set
// [Options.NoReconnect].
//
// Binary frames do not exist on a byte stream, so Blob results arrive in
// their JSON form. [Blob] decodes both forms.
func DialStream(ctx context.Context, dial func(ctx context.Context) (io.ReadWriteCloser, error), opts Options) (*Client, error) {
	maxSize := opts.MaxMessageSize
	if maxSize <= 0 {
		maxSize = defaultMaxMessageSize
	}
	// The line scanner needs room for the newline, and a larger limit means
	// no practical limit anyway (math.MaxInt would overflow).
	if maxSize >= math.MaxInt32 {
		maxSize = math.MaxInt32 - 1
	}
	return start(ctx, opts, func(ctx context.Context) (wireConn, error) {
		rw, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		return newStreamConn(rw, maxSize), nil
	})
}

func start(ctx context.Context, opts Options, dial func(context.Context) (wireConn, error)) (*Client, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{
		opts:     opts,
		dial:     dial,
		logger:   logger,
		state:    StateConnecting,
		ready:    make(chan struct{}),
		notifier: stateNotifier{fn: opts.OnStateChange},
		done:     make(chan struct{}),
		pending:  make(map[string]*pendingCall),
		subs:     make(map[string]subEntry),
		streams:  make(map[string]streamEntry),
		pushes:   make(map[string]map[*pushHandler]struct{}),
		backoff: backoff{
			interval:         opts.ReconnectInterval,
			maxInterval:      opts.ReconnectMaxInterval,
			maxAttempts:      max(opts.ReconnectMaxAttempts, 0),
			fixedInterval:    opts.ReconnectInterval > 0,
			fixedMaxInterval: opts.ReconnectMaxInterval > 0,
			fixedMaxAttempts: opts.ReconnectMaxAttempts != 0,
		},
	}
	if c.backoff.interval <= 0 {
		c.backoff.interval = defaultReconnectInterval
	}
	if c.backoff.maxInterval <= 0 {
		c.backoff.maxInterval = defaultReconnectMaxInterval
	}

	conn, watch, err := c.connect(ctx)
	if err != nil {
		if errors.Is(err, ErrClosed) {
			// The connection closed the client while it came up, for
			// example with an auth_error: report why.
			if cause := c.Err(); cause != nil {
				err = cause
			}
		}
		c.shutdown(err)
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancelRun = cancel
	closed := c.closed
	c.mu.Unlock()
	if closed {
		// Closed between connecting and here (a rejection on the first
		// frames): shutdown ran without a cancel func to call.
		cancel()
	}
	go c.run(runCtx, conn, watch)
	return c, nil
}

// connect dials, reads the server's first frame, authenticates, and marks
// the client connected. The returned watch reports when the connection's
// read loop ends. Ending ctx before the connection is up closes it, so a
// server that accepts but never answers cannot hold connect forever.
func (c *Client) connect(ctx context.Context) (conn wireConn, watch *connWatch, err error) {
	timeout := c.opts.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err = c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	stopWatchdog := context.AfterFunc(ctx, func() { _ = conn.close() })
	defer func() {
		if !stopWatchdog() && err == nil {
			// ctx ended just as the connection came up, and the watchdog
			// has closed it. Report the connection as lost; the read loop
			// sees the close and the caller handles it like any drop.
			c.logger.Debug("aprot client: connect raced ctx", "err", ctx.Err())
		}
		if err != nil && ctx.Err() != nil {
			err = fmt.Errorf("aprot client: connect: %w (%v)", ctx.Err(), err)
		}
	}()

	// The server's first frame is either the config frame or a connection
	// rejection followed by a close.
	data, _, err := conn.read()
	if err != nil {
		_ = conn.close()
		return nil, nil, fmt.Errorf("aprot client: reading handshake: %w", err)
	}
	var first inFrame
	if err := unmarshalWire(data, &first); err != nil {
		_ = conn.close()
		return nil, nil, fmt.Errorf("aprot client: decoding handshake: %w", err)
	}
	switch first.Type {
	case "config":
		c.applyConfig(conn, first)
	case "error":
		_ = conn.close()
		return nil, nil, &rejectedError{&Error{Code: first.Code, Message: first.Message, Data: first.Data}}
	default:
		_ = conn.close()
		return nil, nil, fmt.Errorf("aprot client: unexpected first frame %q", first.Type)
	}

	watch = &connWatch{done: make(chan struct{})}
	go func() {
		watch.err = c.readLoop(conn, watch)
		close(watch.done)
	}()

	if c.opts.AuthToken != nil {
		token, err := c.opts.AuthToken(ctx)
		if err != nil {
			_ = conn.close()
			<-watch.done
			return nil, nil, fmt.Errorf("aprot client: AuthToken: %w", err)
		}
		if err := c.authenticate(ctx, conn, token, watch); err != nil {
			_ = conn.close()
			<-watch.done
			var apiErr *Error
			if errors.As(err, &apiErr) {
				return nil, nil, &rejectedError{apiErr}
			}
			return nil, nil, err
		}
	}

	if !c.markConnected(conn, watch) {
		_ = conn.close()
		<-watch.done
		return nil, nil, ErrClosed
	}
	return conn, watch, nil
}

// rejectedError marks a failure the client must not retry: the server
// refused the connection or the token.
type rejectedError struct{ err *Error }

func (e *rejectedError) Error() string { return e.err.Error() }
func (e *rejectedError) Unwrap() error { return e.err }

// connWatch reports the end of one connection's read loop. done is closed
// when the loop returns; err is set before that.
type connWatch struct {
	done chan struct{}
	err  error
	// authSent counts auth frames on this connection still awaiting their
	// auth_ok or auth_error. A verdict when it is zero is unsolicited: the
	// server requires auth and the client sent none.
	authSent atomic.Int32
	// authAbandoned counts auth frames whose caller gave up (its ctx ended)
	// before the verdict arrived. The server answers auth frames in order,
	// so the next that many verdicts belong to them and are dropped, rather
	// than reaching a later RefreshAuth. Guarded by Client.mu.
	authAbandoned int
}

// run supervises the connection: it waits for the current one to drop, then
// reconnects until the client is closed. Failed attempts back off linearly. A
// rejection is retried at a fixed delay under Options.ReconnectOnRejected,
// and closes the client otherwise.
func (c *Client) run(ctx context.Context, conn wireConn, watch *connWatch) {
	// rejections counts consecutive rejections, as the TypeScript client's
	// reconnectOnRejected does: any other outcome resets it.
	rejections := 0
	for {
		select {
		case <-watch.done:
		case <-ctx.Done():
			_ = conn.close()
			<-watch.done
			return
		}
		dropErr := watch.err
		c.mu.Lock()
		rejected := c.dropRejection
		c.dropRejection = nil
		c.mu.Unlock()
		c.markDisconnected(conn, dropErr)

		if c.opts.NoReconnect {
			if rejected != nil {
				c.shutdown(rejected)
			} else {
				c.shutdown(fmt.Errorf("%w: %v", ErrConnectionLost, dropErr))
			}
			return
		}
		if rejected == nil {
			rejections = 0
		}

		var err error
		for attempt := 0; ; {
			var delay time.Duration
			if rejected != nil {
				retry := c.opts.ReconnectOnRejected
				if retry == nil || (retry.MaxAttempts > 0 && rejections >= retry.MaxAttempts) {
					c.shutdown(rejected)
					return
				}
				rejections++
				delay = retry.Delay
				if delay <= 0 {
					delay = defaultRejectedRetryDelay
				}
				c.mu.Lock()
				c.lastRejection = rejected
				c.mu.Unlock()
				c.logger.Debug("aprot client: connection rejected; retrying", "rejections", rejections, "err", rejected)
			} else {
				rejections = 0
				attempt++
				b := c.currentBackoff()
				if b.maxAttempts > 0 && attempt > b.maxAttempts {
					c.shutdown(fmt.Errorf("aprot client: gave up after %d reconnect attempts: %w", b.maxAttempts, err))
					return
				}
				delay = b.delay(attempt)
			}
			t := time.NewTimer(delay)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return
			}
			conn, watch, err = c.connect(ctx)
			if err == nil {
				c.mu.Lock()
				c.lastRejection = nil
				c.mu.Unlock()
				break
			}
			if ctx.Err() != nil {
				return
			}
			var rej *rejectedError
			if errors.As(err, &rej) {
				// The server answered, so the network is fine: restart
				// the backoff schedule.
				rejected = rej.err
				attempt = 0
				continue
			}
			rejected = nil
			c.logger.Debug("aprot client: reconnect failed", "attempt", attempt, "err", err)
		}
	}
}

func (c *Client) currentBackoff() backoff {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backoff
}

// applyConfig applies a config frame received on conn. Reconnect settings
// from the server fill in only what Options left unset.
func (c *Client) applyConfig(conn wireConn, f inFrame) {
	conn.setOutboundLimit(f.MaxMessageSize)
	c.mu.Lock()
	defer c.mu.Unlock()
	if f.ReconnectInterval > 0 && !c.backoff.fixedInterval {
		c.backoff.interval = time.Duration(f.ReconnectInterval) * time.Millisecond
	}
	if f.ReconnectMaxInterval > 0 && !c.backoff.fixedMaxInterval {
		c.backoff.maxInterval = time.Duration(f.ReconnectMaxInterval) * time.Millisecond
	}
	if f.ReconnectMaxAttempts > 0 && !c.backoff.fixedMaxAttempts {
		c.backoff.maxAttempts = f.ReconnectMaxAttempts
	}
}

// LastRejection returns the server's most recent rejection under
// [Options.ReconnectOnRejected], or nil if there has been none since the last
// successful connect. It returns to nil once a reconnect succeeds, and keeps
// its value if the client closes while retrying or runs out of attempts, so
// it still explains why the session ended. While retrying, the client is in
// StateConnecting, and calls wait for the connection as they do during any
// reconnect.
func (c *Client) LastRejection() *Error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRejection
}

// markConnected publishes conn and re-sends every open subscription.
//
// The snapshot of subscriptions and the publication of conn happen under one
// lock, and [Subscribe] registers and reads conn under the same lock. So
// exactly one side sends each subscribe frame: either Subscribe saw a nil
// conn and this sends it, or Subscribe saw conn and sends it itself.
func (c *Client) markConnected(conn wireConn, watch *connWatch) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.conn = conn
	c.watch = watch
	c.subInFlight = 0
	c.subQueue = nil
	subs := make([]subEntry, 0, len(c.subs))
	for _, s := range c.subs {
		subs = append(subs, s)
	}
	close(c.ready)
	c.mu.Unlock()

	c.setState(StateConnected)
	for _, s := range subs {
		c.sendSubscribe(conn, s)
	}
	return true
}

// stateNotifier delivers state changes to Options.OnStateChange in order, on
// its own goroutine, so the callback can call back into the client.
type stateNotifier struct {
	fn      func(State)
	mu      sync.Mutex
	queue   []State
	running bool
}

func (n *stateNotifier) push(s State) {
	if n.fn == nil {
		return
	}
	n.mu.Lock()
	n.queue = append(n.queue, s)
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.mu.Unlock()
	go func() {
		for {
			n.mu.Lock()
			if len(n.queue) == 0 {
				n.running = false
				n.mu.Unlock()
				return
			}
			next := n.queue[0]
			n.queue = n.queue[1:]
			n.mu.Unlock()
			n.fn(next)
		}
	}()
}

// markDisconnected fails everything bound to the dropped connection. Open
// subscriptions are kept; markConnected re-sends them.
func (c *Client) markDisconnected(conn wireConn, cause error) {
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}
	c.conn = nil
	c.watch = nil
	c.subInFlight = 0
	c.subQueue = nil
	c.ready = make(chan struct{})
	pending := c.pending
	c.pending = make(map[string]*pendingCall)
	streams := c.streams
	c.streams = make(map[string]streamEntry)
	subs := make([]subEntry, 0, len(c.subs))
	for _, s := range c.subs {
		subs = append(subs, s)
	}
	c.mu.Unlock()

	err := fmt.Errorf("%w: %v", ErrConnectionLost, cause)
	for _, p := range pending {
		p.ch <- callResult{err: err}
	}
	for _, s := range streams {
		s.end(err)
	}
	for _, s := range subs {
		s.connLost()
	}
	c.setState(StateConnecting)
}

// setState records a state change and queues it for OnStateChange. Queuing
// under c.mu keeps the callbacks in the same order as the changes.
func (c *Client) setState(s State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == s || c.state == StateClosed || c.closed {
		return
	}
	c.state = s
	c.notifier.push(s)
}

// State returns the current connection state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Done is closed once the client has stopped for good.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err reports why the client stopped: [ErrClosed] after [Client.Close], an
// [*Error] if the server rejected the connection or the auth token, or the
// last connection error after the reconnect attempts ran out. It returns nil
// while the client is running.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// Close closes the connection and stops reconnecting. Pending calls and
// streams fail with [ErrClosed], and every open subscription closes with
// [ErrClosed]. Close does not wait for the client's goroutines to exit, so
// it is safe to call from any callback.
func (c *Client) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

// shutdown stops the client permanently with err as the reason. Only the
// first call has an effect.
func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	conn := c.conn
	c.conn = nil
	cancel := c.cancelRun
	pending := c.pending
	c.pending = make(map[string]*pendingCall)
	streams := c.streams
	c.streams = make(map[string]streamEntry)
	subs := c.subs
	c.subs = make(map[string]subEntry)
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.close()
	}
	for _, p := range pending {
		p.ch <- callResult{err: err}
	}
	for _, s := range streams {
		s.end(err)
	}
	for _, s := range subs {
		s.fail(err)
	}
	c.mu.Lock()
	c.state = StateClosed
	c.notifier.push(StateClosed)
	c.mu.Unlock()
	// ready is never closed again; waiters select on done too.
	close(c.done)
}

// waitConnected blocks until the client is connected and returns that
// connection.
func (c *Client) waitConnected(ctx context.Context) (wireConn, error) {
	for {
		c.mu.Lock()
		if c.closed {
			err := c.closeErr
			c.mu.Unlock()
			return nil, err
		}
		if c.conn != nil {
			conn := c.conn
			c.mu.Unlock()
			return conn, nil
		}
		ready := c.ready
		c.mu.Unlock()
		select {
		case <-ready:
		case <-c.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *Client) newID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return strconv.FormatUint(c.nextID, 10)
}

func (c *Client) send(conn wireConn, f outFrame) error {
	data, err := marshalWire(f)
	if err != nil {
		return err
	}
	return conn.write(data)
}

// authenticate sends an auth frame on conn and waits for the server's
// verdict.
func (c *Client) authenticate(ctx context.Context, conn wireConn, token string, watch *connWatch) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	wait := make(chan error, 1)
	c.mu.Lock()
	c.authWait = wait
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.authWait == wait {
			c.authWait = nil
		}
		c.mu.Unlock()
	}()

	watch.authSent.Add(1)
	if err := c.send(conn, outFrame{Type: "auth", Token: token}); err != nil {
		watch.authSent.Add(-1)
		return err
	}
	select {
	case err := <-wait:
		return err
	case <-watch.done:
		return fmt.Errorf("%w during auth", ErrConnectionLost)
	case <-ctx.Done():
		c.mu.Lock()
		if c.authWait == wait {
			// Give up on the verdict; the read loop drops it when it comes.
			c.authWait = nil
			watch.authAbandoned++
			c.mu.Unlock()
			return ctx.Err()
		}
		c.mu.Unlock()
		// The read loop already took this verdict and is handing it over.
		return <-wait
	}
}

// RefreshAuth sends a new token on the live connection without reconnecting.
// On failure the server keeps the existing session, and RefreshAuth returns
// an [*Error] with CodeAuthFailed. Reconnects keep using
// [Options.AuthToken].
func (c *Client) RefreshAuth(ctx context.Context, token string) error {
	conn, err := c.waitConnected(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	watch := c.watch
	c.mu.Unlock()
	if watch == nil {
		return ErrConnectionLost
	}
	return c.authenticate(ctx, conn, token, watch)
}

// readLoop reads and routes frames until the connection fails.
func (c *Client) readLoop(conn wireConn, watch *connWatch) error {
	for {
		data, binary, err := conn.read()
		if err != nil {
			return err
		}
		if binary {
			c.handleBinary(data)
			continue
		}
		var f inFrame
		if err := unmarshalWire(data, &f); err != nil {
			c.logger.Warn("aprot client: undecodable frame", "err", err)
			continue
		}
		c.handleFrame(conn, watch, f)
	}
}

func (c *Client) handleFrame(conn wireConn, watch *connWatch, f inFrame) {
	switch f.Type {
	case "response":
		c.deliverResult(f.ID, f.Result, nil)
	case "error":
		c.handleError(conn, f)
	case "auth_ok", "auth_error":
		if f.Timeout && c.opts.AuthToken != nil {
			// The server's pending-auth timeout fired; it closes the
			// connection next. It is not the verdict on our auth frame,
			// which may be crossing it, so the close takes the normal
			// reconnect path instead of being treated as a rejection.
			return
		}
		solicited := watch.authSent.Add(-1) >= 0
		if !solicited {
			watch.authSent.Store(0)
			if f.Type == "auth_error" && c.opts.AuthToken == nil {
				// The server requires auth and the client has no token:
				// every request will get this answer, and the server will
				// close the connection when its auth timeout runs out.
				// Reconnecting cannot help, so stop and say why. (With an
				// AuthToken, this is the server's auth timeout firing while
				// the token was still being fetched: the close that follows
				// takes the normal reconnect path.)
				msg := f.Message
				if msg == "" {
					msg = "authentication required"
				}
				c.shutdown(&Error{Code: CodeAuthFailed, Message: msg})
			}
			return
		}
		c.mu.Lock()
		if watch.authAbandoned > 0 {
			// The verdict for an auth whose caller gave up.
			watch.authAbandoned--
			c.mu.Unlock()
			return
		}
		wait := c.authWait
		c.authWait = nil
		c.mu.Unlock()
		if wait == nil {
			return
		}
		if f.Type == "auth_ok" {
			wait <- nil
		} else {
			msg := f.Message
			if msg == "" {
				msg = "authentication failed"
			}
			wait <- &Error{Code: CodeAuthFailed, Message: msg}
		}
	case "progress":
		c.mu.Lock()
		p := c.pending[f.ID]
		c.mu.Unlock()
		if p != nil && p.progress != nil {
			p.progress(Progress{Current: f.Current, Total: f.Total, Message: f.Message})
		}
	case "push":
		c.dispatchPush(f.Event, f.Data)
	case "subscription_patch":
		c.mu.Lock()
		s := c.subs[f.ID]
		c.mu.Unlock()
		if s != nil && !s.patch(f.Patch) {
			// The subscription cannot apply this patch: fetch the full
			// result again rather than go stale.
			c.resubscribe(conn, s)
		}
	case "stream_item":
		c.mu.Lock()
		s := c.streams[f.ID]
		c.mu.Unlock()
		if s != nil {
			s.push(f.Item)
		} else {
			c.rejectStreamForCall(conn, f.ID)
		}
	case "stream_chunk":
		c.mu.Lock()
		s := c.streams[f.ID]
		c.mu.Unlock()
		if s != nil {
			for _, item := range f.Items {
				s.push(item)
			}
		} else {
			c.rejectStreamForCall(conn, f.ID)
		}
	case "stream_end":
		c.mu.Lock()
		s := c.streams[f.ID]
		delete(c.streams, f.ID)
		c.mu.Unlock()
		if s == nil {
			c.rejectStreamForCall(conn, f.ID)
		}
		if s != nil {
			if f.Code != 0 {
				s.end(&Error{Code: f.Code, Message: f.Message, Data: f.Data})
			} else {
				s.end(nil)
			}
		}
	case "config":
		c.applyConfig(conn, f)
	default:
		c.logger.Debug("aprot client: ignoring unknown frame", "type", f.Type)
	}
}

// rejectStreamForCall fails a [Call] whose method turned out to be a
// streaming handler, and cancels the stream at the server. Without it the
// call would wait until its ctx ended.
func (c *Client) rejectStreamForCall(conn wireConn, id string) {
	c.mu.Lock()
	p, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return
	}
	_ = c.send(conn, outFrame{Type: "cancel", ID: id})
	p.ch <- callResult{err: &Error{Code: CodeInvalidRequest, Message: "method is a streaming handler; use Stream or Stream2"}}
}

func (c *Client) handleError(conn wireConn, f inFrame) {
	apiErr := &Error{Code: f.Code, Message: f.Message, Data: f.Data}
	c.mu.Lock()
	if p, ok := c.pending[f.ID]; ok && f.ID != "" {
		delete(c.pending, f.ID)
		c.mu.Unlock()
		p.ch <- callResult{err: apiErr}
		return
	}
	if s, ok := c.streams[f.ID]; ok && f.ID != "" {
		// A streaming handler that fails before its first item reports a
		// plain error frame, not stream_end.
		delete(c.streams, f.ID)
		c.mu.Unlock()
		s.end(apiErr)
		return
	}
	s := c.subs[f.ID]
	c.mu.Unlock()
	if s != nil && f.ID != "" {
		if aw := s.takeAwaiting(); aw != nil {
			// The error answers the subscribe itself: the server did not
			// register it.
			c.releaseSubSlot(aw)
			s.fail(apiErr)
		} else {
			s.refreshError(apiErr)
		}
		return
	}
	if f.Code == CodeConnectionRejected {
		// The server is ending the session and will close the connection.
		if c.opts.ReconnectOnRejected != nil && !c.opts.NoReconnect {
			// Retry it as a rejection, at the fixed delay.
			c.mu.Lock()
			if c.conn == conn {
				c.dropRejection = apiErr
			}
			c.mu.Unlock()
			_ = conn.close()
			return
		}
		// Shut down before the close lands, so the client does not
		// reconnect into the same rejection.
		c.shutdown(apiErr)
		return
	}
	if f.ID == "" {
		c.logger.Warn("aprot client: connection error from server", "code", f.Code, "message", f.Message)
	}
}

// deliverResult routes a result to the pending call or subscription with id.
// Exactly one of raw and blob is set.
func (c *Client) deliverResult(id string, raw jsontext.Value, blob *Blob) {
	c.mu.Lock()
	if p, ok := c.pending[id]; ok {
		delete(c.pending, id)
		c.mu.Unlock()
		p.ch <- callResult{raw: raw, blob: blob}
		return
	}
	if st, ok := c.streams[id]; ok {
		// A plain result for a stream: the method is not a streaming
		// handler. The handler has finished, so there is nothing to cancel.
		delete(c.streams, id)
		c.mu.Unlock()
		st.end(&Error{Code: CodeInvalidRequest, Message: "method is not a streaming handler; use Call"})
		return
	}
	s := c.subs[id]
	c.mu.Unlock()
	if s != nil {
		s.deliver(raw, blob)
		if aw := s.takeAwaiting(); aw != nil {
			c.answered(aw, s)
		}
	}
}

func (c *Client) handleBinary(data []byte) {
	h, payload, err := decodeBinaryFrame(data)
	if err != nil {
		c.logger.Warn("aprot client: undecodable binary frame", "err", err)
		return
	}
	if h.Version != 1 || h.Type != "response" {
		err := &Error{Code: CodeInternalError, Message: fmt.Sprintf("unsupported binary frame (version %d, type %s)", h.Version, h.Type)}
		c.mu.Lock()
		p, ok := c.pending[h.ID]
		delete(c.pending, h.ID)
		s := c.subs[h.ID]
		c.mu.Unlock()
		if ok {
			p.ch <- callResult{err: err}
		} else if s != nil {
			s.fail(err)
		}
		return
	}
	c.deliverResult(h.ID, nil, &Blob{ContentType: h.ContentType, Data: payload})
}

func marshalParams(params []any) (jsontext.Value, error) {
	if params == nil {
		params = []any{}
	}
	data, err := marshalWire(params)
	if err != nil {
		return nil, fmt.Errorf("aprot client: encoding params: %w", err)
	}
	return data, nil
}

// decodeResult decodes a result into T. A binary Blob result is assigned
// directly when T is Blob or *Blob.
func decodeResult[T any](raw jsontext.Value, blob *Blob) (T, error) {
	var v T
	if blob != nil {
		switch p := any(&v).(type) {
		case *Blob:
			*p = *blob
			return v, nil
		case **Blob:
			*p = blob
			return v, nil
		}
		return v, fmt.Errorf("aprot client: got binary result, but %T is not a Blob", v)
	}
	if len(raw) == 0 {
		return v, nil
	}
	if err := unmarshalWire(raw, &v); err != nil {
		return v, fmt.Errorf("aprot client: decoding result as %T: %w", v, err)
	}
	return v, nil
}
