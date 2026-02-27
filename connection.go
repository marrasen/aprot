package aprot

import (
	"context"
	"net/http"
	"sync"

	"github.com/go-json-experiment/json"
)

// ConnInfo contains HTTP request information captured at connection time.
type ConnInfo struct {
	RemoteAddr string
	Header     http.Header
	Cookies    []*http.Cookie
	URL        string
	Host       string
}

// Conn represents a single client connection.
type Conn struct {
	transport transport
	server    *Server
	requests  map[string]context.CancelFunc
	mu        sync.Mutex
	closed    bool
	userID    string          // associated user ID (set by middleware)
	id        uint64          // unique connection ID
	info      ConnInfo
	valuesMu  sync.RWMutex    // guards values map (separate from mu to avoid contention with sendJSON/requests)
	values    map[any]any     // connection-scoped key-value store (lazy init)
	ctx       context.Context // Context from HTTP request
}

// SetUserID associates this connection with a user ID.
// Call this from auth middleware after successful authentication.
// A user can have multiple connections (multiple tabs/devices).
func (c *Conn) SetUserID(userID string) {
	c.mu.Lock()
	oldUserID := c.userID
	c.userID = userID
	c.mu.Unlock()

	// Disassociate old user if changing
	if oldUserID != "" && oldUserID != userID {
		c.server.mu.Lock()
		if conns, ok := c.server.userConns[oldUserID]; ok {
			delete(conns, c)
			if len(conns) == 0 {
				delete(c.server.userConns, oldUserID)
			}
		}
		c.server.mu.Unlock()
	}

	// Associate new user
	if userID != "" {
		c.server.associateUser(c, userID)
	}
}

// UserID returns the associated user ID, or empty string if not set.
func (c *Conn) UserID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID
}

// ID returns the unique connection ID.
func (c *Conn) ID() uint64 {
	return c.id
}

// Info returns HTTP request information captured at connection time.
func (c *Conn) Info() ConnInfo {
	return c.info
}

// Context returns the context from the HTTP request.
// This is useful for accessing request-scoped values like zerolog loggers.
func (c *Conn) Context() context.Context {
	return c.ctx
}

// Set stores a value on the connection, keyed by an arbitrary key.
// This is useful for caching connection-scoped data (e.g. an authenticated
// principal) that persists for the connection's lifetime. The map is lazily
// initialized on first call, so connections that never call Set pay no cost.
// Keep stored values small — they live for the entire connection lifetime.
// Safe for concurrent use.
func (c *Conn) Set(key, value any) {
	c.valuesMu.Lock()
	defer c.valuesMu.Unlock()
	if c.values == nil {
		c.values = make(map[any]any)
	}
	c.values[key] = value
}

// Get retrieves a value previously stored with Set.
// Returns nil if the key was never set (or was set to nil).
// Use Load to distinguish between an unset key and a key set to nil.
// Safe for concurrent use.
func (c *Conn) Get(key any) any {
	c.valuesMu.RLock()
	defer c.valuesMu.RUnlock()
	if c.values == nil {
		return nil
	}
	return c.values[key]
}

// Load retrieves a value previously stored with Set.
// The ok result indicates whether the key was found.
// This follows the sync.Map convention and allows callers to distinguish
// between an unset key and a key explicitly set to nil.
// Safe for concurrent use.
func (c *Conn) Load(key any) (value any, ok bool) {
	c.valuesMu.RLock()
	defer c.valuesMu.RUnlock()
	if c.values == nil {
		return nil, false
	}
	value, ok = c.values[key]
	return
}

// RemoteAddr returns the remote address of the connection.
func (c *Conn) RemoteAddr() string {
	return c.info.RemoteAddr
}

func newConn(t transport, server *Server, id uint64, r *http.Request, ctx context.Context) *Conn {
	return &Conn{
		transport: t,
		server:    server,
		requests:  make(map[string]context.CancelFunc),
		id:        id,
		ctx:       ctx,
		info: ConnInfo{
			RemoteAddr: r.RemoteAddr,
			Header:     r.Header.Clone(),
			Cookies:    r.Cookies(),
			URL:        r.URL.String(),
			Host:       r.Host,
		},
	}
}

// ServerBroadcaster returns the server as a Broadcaster.
// This allows external packages to broadcast push events without
// exposing the *Server type directly.
func (c *Conn) ServerBroadcaster() Broadcaster {
	return c.server
}

// Push sends a push message to this connection.
// The event name is derived from the Go type of data, which must have been
// registered via RegisterPushEventFor.
func (c *Conn) Push(data any) error {
	event := c.server.registry.eventName(data)
	return c.push(event, data)
}

// push sends a push message with an explicit event name (internal use).
func (c *Conn) push(event string, data any) error {
	msg := PushMessage{
		Type:  TypePush,
		Event: event,
		Data:  data,
	}
	return c.sendJSON(msg)
}

func (c *Conn) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	return c.transport.Send(data)
}

func (c *Conn) sendResponse(id string, result any) {
	msg := ResponseMessage{
		Type:   TypeResponse,
		ID:     id,
		Result: result,
	}
	c.sendJSON(msg)
}

func (c *Conn) sendError(id string, code int, message string) {
	msg := ErrorMessage{
		Type:    TypeError,
		ID:      id,
		Code:    code,
		Message: message,
	}
	c.sendJSON(msg)
}

func (c *Conn) sendProgress(id string, current, total int, message string) {
	msg := ProgressMessage{
		Type:    TypeProgress,
		ID:      id,
		Current: &current,
		Total:   &total,
		Message: message,
	}
	c.sendJSON(msg)
}

func (c *Conn) sendPong() {
	msg := PongMessage{
		Type: TypePong,
	}
	c.sendJSON(msg)
}

func (c *Conn) registerRequest(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests[id] = cancel
}

func (c *Conn) unregisterRequest(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.requests, id)
}

func (c *Conn) cancelRequest(id string) {
	c.mu.Lock()
	cancel, ok := c.requests[id]
	c.mu.Unlock()
	if ok {
		cancel()
	}
}

// handleIncomingMessage processes a raw message from any transport.
func (c *Conn) handleIncomingMessage(data []byte) {
	var msg IncomingMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendError("", CodeParseError, "invalid JSON")
		return
	}

	switch msg.Type {
	case TypeRequest:
		c.server.requestsWg.Add(1)
		go c.handleRequest(msg)
	case TypeCancel:
		c.cancelRequest(msg.ID)
	case TypePing:
		c.sendPong()
	default:
		c.sendError(msg.ID, CodeInvalidRequest, "unknown message type")
	}
}

func (c *Conn) handleRequest(msg IncomingMessage) {
	defer c.server.requestsWg.Done()

	info, ok := c.server.registry.Get(msg.Method)
	if !ok {
		c.sendError(msg.ID, CodeMethodNotFound, "method not found: "+msg.Method)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.registerRequest(msg.ID, cancel)
	defer func() {
		c.unregisterRequest(msg.ID)
		cancel()
	}()

	// Add progress reporter and connection to context
	progress := newProgressReporter(c, msg.ID)
	ctx = withProgress(ctx, progress)
	ctx = withConnection(ctx, c)

	// Add handler info to context for middleware
	ctx = withHandlerInfo(ctx, info)

	// Create request object for middleware
	req := &Request{
		ID:     msg.ID,
		Method: msg.Method,
		Params: msg.Params,
	}
	ctx = withRequest(ctx, req)

	// Build and execute middleware chain
	handler := c.server.buildHandler(info)
	result, err := handler(ctx, req)

	// Check if context was canceled
	if ctx.Err() == context.Canceled {
		c.sendError(msg.ID, CodeCanceled, "request canceled")
		return
	}

	if err != nil {
		if perr, ok := err.(*ProtocolError); ok {
			c.sendError(msg.ID, perr.Code, perr.Message)
		} else if code, found := c.server.registry.LookupError(err); found {
			c.sendError(msg.ID, code, err.Error())
		} else {
			c.sendError(msg.ID, CodeInternalError, err.Error())
		}
		return
	}

	c.sendResponse(msg.ID, result)
}

func (c *Conn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	// Cancel all pending requests
	for _, cancel := range c.requests {
		cancel()
	}
	c.mu.Unlock()
	c.transport.Close()
}

func (c *Conn) closeGracefully() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for _, cancel := range c.requests {
		cancel()
	}
	c.mu.Unlock()
	c.transport.CloseGracefully()
}
