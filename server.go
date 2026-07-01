package aprot

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// Broadcaster is an interface for broadcasting push events to all clients.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEventFor.
type Broadcaster interface {
	Broadcast(data any)
}

// ConnectHook is called when a new connection is established.
// Return an error to reject the connection.
type ConnectHook func(ctx context.Context, conn *Conn) error

// DisconnectHook is called when a connection is closed.
type DisconnectHook func(ctx context.Context, conn *Conn)

// AuthHook validates a token sent by the client in an auth frame (first-message
// authentication and mid-session refresh). Return nil to accept — typically
// after calling conn.SetUserID with the authenticated identity — or an error to
// reject; the error's message is relayed to the client in an auth_error frame.
// Return [ErrAuthFailed] for a clean, client-facing failure message.
//
// Registering a hook (via [Server.OnAuth]) puts every new connection into a
// pending-auth state: it is rejected (auth_error) for any frame other than auth
// until a token is accepted, and closed if none arrives within
// [ServerOptions.AuthTimeout].
type AuthHook func(ctx context.Context, conn *Conn, token string) error

// ServerOptions configures the server behavior.
type ServerOptions struct {
	// ReconnectInterval is the initial reconnect delay in milliseconds. Default: 1000
	ReconnectInterval int
	// ReconnectMaxInterval is the maximum reconnect delay in milliseconds. Default: 30000
	ReconnectMaxInterval int
	// ReconnectMaxAttempts is the maximum number of reconnect attempts. 0 = unlimited. Default: 0
	ReconnectMaxAttempts int
	// MaxMessageSize is the maximum size in bytes of an inbound WebSocket
	// frame or SSE RPC request body. Larger messages close the connection
	// (WebSocket) or are rejected with 413 (SSE). Set to -1 to disable the
	// limit. Default: 4 MiB.
	MaxMessageSize int64
	// WriteTimeout is the maximum time to write a single outbound WebSocket
	// message. A connection whose peer stops reading is closed once a write
	// exceeds this timeout, so a stalled client cannot back-pressure the
	// whole server. Set to -1 to disable. Default: 30s.
	WriteTimeout time.Duration
	// PingInterval is how often the server sends WebSocket ping frames.
	// Set to -1 to disable keepalive pings. Default: 30s.
	PingInterval time.Duration
	// PongTimeout is how long a WebSocket connection may go without any
	// inbound traffic (data or pong) before it is considered dead and
	// closed. Must be larger than PingInterval; if it is set to a value less
	// than or equal to PingInterval (while both are enabled), NewServer clamps
	// it up to 2*PingInterval so healthy connections aren't dropped. Only
	// effective while pings are enabled. Set to -1 to disable. Default: 60s.
	PongTimeout time.Duration
	// MaxConcurrentRequests caps the number of in-flight requests a single
	// connection may run at once. Each inbound request/subscribe frame occupies
	// one slot for the duration of its handler (a streaming handler holds its
	// slot until the stream ends). When a connection is at the cap, further
	// requests are rejected with CodeTooManyRequests instead of spawning an
	// unbounded number of goroutines. Set to -1 to disable. Default: 256.
	MaxConcurrentRequests int
	// MaxServerConcurrentRequests caps the total number of in-flight requests
	// across all connections, bounding server-wide goroutine and memory growth
	// from a fleet of connections. Requests over the cap are rejected with
	// CodeTooManyRequests. Set to -1 to disable. Default: 10000.
	MaxServerConcurrentRequests int
	// MaxSubscriptions caps the number of active subscriptions a single
	// connection may hold. A subscribe beyond the cap is rejected with
	// CodeTooManyRequests. This also bounds refresh fan-out amplification, since
	// a server-side TriggerRefresh re-executes every matching subscription on a
	// connection. Set to -1 to disable. Default: 1024.
	MaxSubscriptions int
	// Observer receives connection, request, subscription, refresh, and
	// send-buffer events for metrics/observability. Nil (the default) disables
	// all observation with no hot-path cost. See [Observer].
	Observer Observer
	// AuthTimeout is how long a connection may stay unauthenticated after
	// connecting when an [AuthHook] is registered via [Server.OnAuth]. A
	// connection that has not sent a valid auth frame within this window is
	// closed. Ignored when no auth hook is set. Default: 10s. Set to -1 to
	// disable the timeout — but note that a disabled timeout lets unauthenticated
	// connections linger indefinitely, which is a DoS vector on public endpoints;
	// keep a bounded timeout there.
	AuthTimeout time.Duration
}

func defaultServerOptions() ServerOptions {
	return ServerOptions{
		ReconnectInterval:    1000,
		ReconnectMaxInterval: 30000,
		ReconnectMaxAttempts: 0,
		MaxMessageSize:       4 << 20,
		WriteTimeout:         30 * time.Second,
		PingInterval:         30 * time.Second,
		PongTimeout:          60 * time.Second,

		MaxConcurrentRequests:       256,
		MaxServerConcurrentRequests: 10000,
		MaxSubscriptions:            1024,

		AuthTimeout: 10 * time.Second,
	}
}

// Server manages WebSocket connections and handler dispatch.
type Server struct {
	registry        *Registry
	upgrader        websocket.Upgrader
	conns           map[*Conn]struct{}
	userConns       map[string]map[*Conn]struct{} // userID -> connections
	mu              sync.RWMutex
	register        chan *Conn
	unregister      chan *Conn
	middleware      []Middleware
	nextConnID      uint64 // atomic counter for connection IDs
	options         ServerOptions
	connectHooks    []ConnectHook
	disconnectHooks []DisconnectHook
	authHook        AuthHook // nil disables first-message auth (no pending state)
	stopHooks       []func()
	stopping        atomic.Bool   // reject new connections when set
	stopCh          chan struct{} // closed by Stop() to signal run() to drain and exit
	stopOnce        sync.Once     // ensures stopCh is closed only once
	done            chan struct{} // closed by run() when it finishes
	requestsWg      sync.WaitGroup
	subscriptions   *subscriptionManager
	// reqSem bounds the total in-flight requests across all connections. It is
	// a counting semaphore (one buffer slot per allowed request); nil when
	// MaxServerConcurrentRequests disables the server-wide cap.
	reqSem chan struct{}
	// observer receives lifecycle/metrics events; nil disables observation.
	observer Observer
}

// NewServer creates a new WebSocket server with the given registry.
// An optional ServerOptions can be passed to configure server behavior.
func NewServer(registry *Registry, opts ...ServerOptions) *Server {
	options := defaultServerOptions()
	if len(opts) > 0 {
		// Merge provided options with defaults
		opt := opts[0]
		if opt.ReconnectInterval > 0 {
			options.ReconnectInterval = opt.ReconnectInterval
		}
		if opt.ReconnectMaxInterval > 0 {
			options.ReconnectMaxInterval = opt.ReconnectMaxInterval
		}
		if opt.ReconnectMaxAttempts > 0 {
			options.ReconnectMaxAttempts = opt.ReconnectMaxAttempts
		}
		// Non-zero overrides the default; negative values disable a limit.
		if opt.MaxMessageSize != 0 {
			options.MaxMessageSize = opt.MaxMessageSize
		}
		if opt.WriteTimeout != 0 {
			options.WriteTimeout = opt.WriteTimeout
		}
		if opt.PingInterval != 0 {
			options.PingInterval = opt.PingInterval
		}
		if opt.PongTimeout != 0 {
			options.PongTimeout = opt.PongTimeout
		}
		if opt.MaxConcurrentRequests != 0 {
			options.MaxConcurrentRequests = opt.MaxConcurrentRequests
		}
		if opt.MaxServerConcurrentRequests != 0 {
			options.MaxServerConcurrentRequests = opt.MaxServerConcurrentRequests
		}
		if opt.MaxSubscriptions != 0 {
			options.MaxSubscriptions = opt.MaxSubscriptions
		}
		if opt.Observer != nil {
			options.Observer = opt.Observer
		}
		if opt.AuthTimeout != 0 {
			options.AuthTimeout = opt.AuthTimeout
		}
	}

	// PongTimeout is the inbound read deadline; pings fire every PingInterval.
	// If the deadline isn't comfortably longer than the ping period, a healthy
	// connection is closed before its next ping/pong can refresh the deadline.
	// When both are enabled but misconfigured, clamp PongTimeout up to twice the
	// ping interval rather than silently dropping live connections.
	if options.PingInterval > 0 && options.PongTimeout > 0 && options.PongTimeout <= options.PingInterval {
		options.PongTimeout = 2 * options.PingInterval
	}

	s := &Server{
		registry: registry,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins by default
			},
		},
		conns:         make(map[*Conn]struct{}),
		userConns:     make(map[string]map[*Conn]struct{}),
		register:      make(chan *Conn),
		unregister:    make(chan *Conn),
		middleware:    []Middleware{},
		options:       options,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
		subscriptions: newSubscriptionManager(),
	}
	if options.MaxServerConcurrentRequests > 0 {
		s.reqSem = make(chan struct{}, options.MaxServerConcurrentRequests)
	}
	s.observer = options.Observer
	// Run OnServerInit hooks (used by tasks/ to set up taskManager, middleware, etc.)
	for _, hook := range registry.serverInitHooks {
		hook(s)
	}

	go s.run()
	return s
}

// Use adds middleware to the chain.
// Middleware is executed in the order it is added.
func (s *Server) Use(mw ...Middleware) {
	s.middleware = append(s.middleware, mw...)
}

// OnConnect registers a hook to be called when a new connection is established.
// Hooks are called in the order they are registered.
// If a hook returns an error, the connection is rejected and subsequent hooks are not called.
func (s *Server) OnConnect(hook ConnectHook) {
	s.connectHooks = append(s.connectHooks, hook)
}

// OnDisconnect registers a hook to be called when a connection is closed.
// Hooks are called in the order they are registered.
// The connection's UserID is still available when the hook is called.
func (s *Server) OnDisconnect(hook DisconnectHook) {
	s.disconnectHooks = append(s.disconnectHooks, hook)
}

// OnStop registers a hook called during Server.Stop after in-flight requests
// drain. Used by the tasks package to shut down the taskManager.
func (s *Server) OnStop(hook func()) {
	s.stopHooks = append(s.stopHooks, hook)
}

// OnAuth registers the hook that validates client auth tokens for first-message
// authentication. Registering a hook enables the pending-auth flow: a new
// connection must send an auth frame ({"type":"auth","token":"..."}) with a
// token the hook accepts before any request or subscribe is processed; frames
// sent before then are rejected with auth_error, and a connection that does not
// authenticate within [ServerOptions.AuthTimeout] is closed. The same auth frame
// on a live connection refreshes the token. Calling OnAuth again replaces the
// hook. With no hook registered, connections behave as before (authenticate via
// [Server.OnConnect] / URL token if desired).
//
//	server.OnAuth(func(ctx context.Context, conn *aprot.Conn, token string) error {
//	    claims, err := verify(token)
//	    if err != nil {
//	        return aprot.ErrAuthFailed("invalid token")
//	    }
//	    conn.SetUserID(claims.Subject)
//	    return nil
//	})
func (s *Server) OnAuth(hook AuthHook) {
	s.authHook = hook
}

// authRequired reports whether an auth hook is registered (pending-auth enabled).
func (s *Server) authRequired() bool {
	return s.authHook != nil
}

// ForEachConn iterates over a snapshot of the active connections. The
// callback runs outside the server's lock, so it may block or send; note
// that a connection may be closing concurrently while it is visited.
func (s *Server) ForEachConn(fn func(conn *Conn)) {
	for _, conn := range s.connsSnapshot() {
		fn(conn)
	}
}

// runConnectHooks executes all connect hooks in order.
// Returns the first error encountered, or nil if all hooks succeed.
func (s *Server) runConnectHooks(ctx context.Context, conn *Conn) error {
	for _, hook := range s.connectHooks {
		if err := hook(ctx, conn); err != nil {
			return err
		}
	}
	return nil
}

// runDisconnectHooks executes all disconnect hooks in order.
func (s *Server) runDisconnectHooks(conn *Conn) {
	ctx := conn.ctx
	for _, hook := range s.disconnectHooks {
		hook(ctx, conn)
	}
}

// buildHandler creates the middleware chain for a handler.
// The chain is: server middleware -> handler middleware -> actual handler
// Server middleware is outermost (executed first on request, last on response).
//
// For streaming handlers (HandlerKindStream / HandlerKindStream2) the final
// closure returns the raw reflect.Value of the iter.Seq wrapped in `any`.
// Middleware sees this return as soon as the handler hands back the iterator
// — it does not wrap the iteration phase itself.
func (s *Server) buildHandler(info *HandlerInfo) Handler {
	// The final handler that calls the actual method
	final := func(ctx context.Context, req *Request) (any, error) {
		if info.Kind == HandlerKindStream || info.Kind == HandlerKindStream2 {
			v, err := info.CallStream(ctx, req.Params)
			if err != nil {
				return nil, err
			}
			return v, nil
		}
		return info.Call(ctx, req.Params)
	}

	handler := final

	// Apply handler-specific middleware (inner layer)
	handlerMW := s.registry.GetMiddleware(info.StructName + "." + info.Name)
	for i := len(handlerMW) - 1; i >= 0; i-- {
		handler = handlerMW[i](handler)
	}

	// Apply server middleware (outer layer)
	for i := len(s.middleware) - 1; i >= 0; i-- {
		handler = s.middleware[i](handler)
	}

	return handler
}

// PushToUser sends a push message to all connections for a specific user.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEventFor.
func (s *Server) PushToUser(userID string, data any) {
	event := s.registry.eventName(data)

	// Snapshot under the lock, send outside it: pushes can block on a slow
	// connection's send buffer, and blocking while holding s.mu would stall
	// register/unregister for every other connection.
	s.mu.RLock()
	conns := make([]*Conn, 0, len(s.userConns[userID]))
	for conn := range s.userConns[userID] {
		conns = append(conns, conn)
	}
	s.mu.RUnlock()

	for _, conn := range conns {
		// A mid-session auth refresh can change a connection's identity between
		// the snapshot above and this send (SetUserID updates c.userID and the
		// userConns index in separate steps). Re-check the current identity so a
		// push for one user is never delivered to a connection that has since
		// re-authenticated as a different user.
		if conn.UserID() != userID {
			continue
		}
		_ = conn.push(event, data)
	}
}

// associateUser registers a connection with a user ID.
func (s *Server) associateUser(conn *Conn, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.userConns[userID] == nil {
		s.userConns[userID] = make(map[*Conn]struct{})
	}
	s.userConns[userID][conn] = struct{}{}
}

// disassociateUser removes a connection from user tracking.
func (s *Server) disassociateUser(conn *Conn) {
	// Read userID under the connection's own lock (it is written there by
	// SetUserID); reading it under s.mu alone is a data race. UserID() takes
	// c.mu and returns before we acquire s.mu, so the two locks never nest.
	userID := conn.UserID()
	if userID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if conns, ok := s.userConns[userID]; ok {
		delete(conns, conn)
		if len(conns) == 0 {
			delete(s.userConns, userID)
		}
	}
}

// SetCheckOrigin sets the origin check function for the WebSocket upgrader.
//
// The default accepts any Origin so non-browser clients work out of the box.
// Deployments that authenticate browsers with cookies MUST restrict origins,
// otherwise any website can open an authenticated WebSocket to this server
// from a visitor's browser (cross-site WebSocket hijacking):
//
//	server.SetCheckOrigin(func(r *http.Request) bool {
//	    return r.Header.Get("Origin") == "https://app.example.com"
//	})
func (s *Server) SetCheckOrigin(f func(r *http.Request) bool) {
	s.upgrader.CheckOrigin = f
}

// WebSocket returns an http.Handler for WebSocket upgrades.
func (s *Server) WebSocket() http.Handler {
	return http.HandlerFunc(s.ServeHTTP)
}

// HTTPTransport returns an http.Handler for SSE+HTTP transport.
// Routes:
//   - GET  / — SSE event stream
//   - POST /rpc — RPC calls
//   - POST /cancel — Request cancellation
func (s *Server) HTTPTransport() http.Handler {
	return newSSEHandler(s)
}

// ServeHTTP implements http.Handler for WebSocket upgrades.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.stopping.Load() {
		http.Error(w, "server stopping", http.StatusServiceUnavailable)
		return
	}

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	connID := atomic.AddUint64(&s.nextConnID, 1)
	ctx := r.Context()
	wst := newWSTransport(ws, s.options)
	conn := newConn(wst, s, connID, r, ctx)
	// Give the transport a back-reference so it can report send-buffer pressure
	// and write timeouts to the observer. Set before the pumps start.
	wst.conn = conn

	// Bound the direct writes below (rejection/config) the same way the
	// write pump bounds its writes; the pump refreshes the deadline per
	// message once it starts.
	if s.options.WriteTimeout > 0 {
		_ = ws.SetWriteDeadline(time.Now().Add(s.options.WriteTimeout))
	}

	// Run connect hooks before starting message processing
	if err := s.runConnectHooks(ctx, conn); err != nil {
		// A hook may have called SetUserID before a later hook rejected the
		// connection; undo that association so a dead conn can't linger in
		// userConns and have a later PushToUser block on its send buffer.
		s.disassociateUser(conn)
		// Send rejection directly before pumps start
		sendConnectionRejectedWS(ws, err)
		_ = ws.Close()
		return
	}

	// Send config directly before pumps start
	sendConfigWS(ws, s.options)

	// Register the connection, but don't block forever if the server has
	// already shut down (run() has exited and will never read s.register).
	select {
	case s.register <- conn:
	case <-s.done:
		s.disassociateUser(conn)
		_ = ws.Close()
		return
	}

	// When an auth hook is registered, the connection is pending until it sends
	// a valid auth frame; close it if that doesn't happen within AuthTimeout.
	if s.authRequired() {
		conn.armAuthTimeout(s.options.AuthTimeout)
	}

	go wst.writePump()
	wst.readPump(conn)
}

// Broadcast sends a push message to all connected clients.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEventFor.
func (s *Server) Broadcast(data any) {
	event := s.registry.eventName(data)
	for _, conn := range s.connsSnapshot() {
		_ = conn.push(event, data)
	}
}

// connsSnapshot copies the current connection set under the read lock.
// Senders iterate the copy without holding the lock, so a blocking send
// can never wedge register/unregister processing.
func (s *Server) connsSnapshot() []*Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conns := make([]*Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	return conns
}

// ConnectionCount returns the number of active connections.
func (s *Server) ConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.conns)
}

// Stats returns a point-in-time snapshot of gauge-style server metrics
// (active connections and subscriptions). It is safe for concurrent use and is
// the pull-based companion to the push-based [Observer]; scrape it periodically
// for gauges rather than tracking those counts from events yourself.
func (s *Server) Stats() ServerStats {
	s.mu.RLock()
	conns := len(s.conns)
	s.mu.RUnlock()
	return ServerStats{
		Connections:   conns,
		Subscriptions: s.subscriptions.count(),
	}
}

func (s *Server) run() {
	defer close(s.done)

	stopCh := s.stopCh
	for {
		select {
		case conn := <-s.register:
			if s.stopping.Load() {
				// This connection registered after Stop's close sweep, so it
				// would otherwise never be closed and would keep run() from
				// ever seeing an empty connection set. Close it and don't
				// track it; its pumps tear down on their own.
				conn.close()
				continue
			}
			s.mu.Lock()
			s.conns[conn] = struct{}{}
			s.mu.Unlock()
			if s.observer != nil {
				s.observer.ConnectionOpened(conn)
			}
		case conn := <-s.unregister:
			s.mu.Lock()
			_, existed := s.conns[conn]
			if existed {
				delete(s.conns, conn)
			}
			empty := len(s.conns) == 0
			s.mu.Unlock()

			if existed {
				s.runDisconnectHooks(conn) // Before disassociate so UserID() works
				s.disassociateUser(conn)
				conn.close()
				if s.observer != nil {
					s.observer.ConnectionClosed(conn)
				}
			}

			if s.stopping.Load() && empty {
				return
			}
		case <-stopCh:
			stopCh = nil // prevent re-firing
			s.mu.RLock()
			empty := len(s.conns) == 0
			s.mu.RUnlock()
			if empty {
				return
			}
		}
	}
}

// Stop gracefully shuts down the server. It rejects new connections,
// closes existing connections with a close frame, waits for in-flight
// requests to complete, and waits for disconnect hooks to finish.
// Returns nil on clean shutdown, or ctx.Err() if the context expires.
func (s *Server) Stop(ctx context.Context) error {
	s.stopping.Store(true)

	// Close all current connections gracefully
	s.mu.RLock()
	conns := make([]*Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.RUnlock()

	for _, conn := range conns {
		conn.closeGracefully()
	}

	// Wait for in-flight requests to complete
	requestsDone := make(chan struct{})
	go func() {
		s.requestsWg.Wait()
		close(requestsDone)
	}()

	select {
	case <-requestsDone:
		// All requests finished
	case <-ctx.Done():
		return ctx.Err()
	}

	// Run OnStop hooks
	for _, hook := range s.stopHooks {
		hook()
	}

	// Signal run() to exit
	s.stopOnce.Do(func() { close(s.stopCh) })
	// Wait for run() to finish processing remaining unregister events, but
	// don't ignore the caller's deadline — a pathological disconnect hook or
	// a wedged connection must not be able to hang Stop forever.
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Registry returns the server's handler registry.
func (s *Server) Registry() *Registry {
	return s.registry
}

// sendConnectionRejectedWS sends an error message directly to a WebSocket connection.
// Used when a connect hook rejects the connection before pumps are started.
func sendConnectionRejectedWS(ws *websocket.Conn, err error) {
	code := CodeConnectionRejected
	message := "connection rejected"
	var errData any
	if perr, ok := err.(*ProtocolError); ok {
		code = perr.Code
		message = perr.Message
		errData = perr.Data
	} else if err != nil {
		message = err.Error()
	}

	msg := ErrorMessage{
		Type:    TypeError,
		ID:      "",
		Code:    code,
		Message: message,
		Data:    errData,
	}
	data, _ := json.Marshal(msg)
	_ = ws.WriteMessage(websocket.TextMessage, data)
}

// sendConfigWS sends the server configuration directly to a WebSocket connection.
// Called before the pumps are started.
func sendConfigWS(ws *websocket.Conn, opts ServerOptions) {
	msg := ConfigMessage{
		Type:                 TypeConfig,
		ReconnectInterval:    opts.ReconnectInterval,
		ReconnectMaxInterval: opts.ReconnectMaxInterval,
		ReconnectMaxAttempts: opts.ReconnectMaxAttempts,
	}
	data, _ := json.Marshal(msg)
	_ = ws.WriteMessage(websocket.TextMessage, data)
}
