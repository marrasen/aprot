package aprot

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-json-experiment/json"
)

// ConnInfo contains connection metadata captured at connection time. For the
// HTTP transports (WebSocket, SSE) it is populated from the HTTP request; for
// [Server.ServeStream] connections the caller supplies it, and any or all
// fields may be zero.
type ConnInfo struct {
	RemoteAddr string
	Header     http.Header
	Cookies    []*http.Cookie
	URL        string
	Host       string
}

// connInfoFromRequest captures HTTP request information for ConnInfo.
func connInfoFromRequest(r *http.Request) ConnInfo {
	return ConnInfo{
		RemoteAddr: r.RemoteAddr,
		Header:     r.Header.Clone(),
		Cookies:    r.Cookies(),
		URL:        r.URL.String(),
		Host:       r.Host,
	}
}

// Conn represents a single client connection.
type Conn struct {
	transport transport
	server    *Server
	requests  map[string]context.CancelCauseFunc
	mu        sync.Mutex
	closed    bool
	userID    string // associated user ID (set by middleware)
	id        uint64 // unique connection ID
	info      ConnInfo
	valuesMu  sync.RWMutex    // guards values map (separate from mu to avoid contention with sendJSON/requests)
	values    map[any]any     // connection-scoped key-value store (lazy init)
	ctx       context.Context // Context from HTTP request
	// reqSem bounds the number of in-flight requests this connection may run
	// concurrently. It is a counting semaphore (one buffer slot per allowed
	// request); nil when MaxConcurrentRequests disables the per-connection cap.
	reqSem chan struct{}
	// authenticated is false while a connection with an auth hook registered is
	// pending authentication, and flips true once a token is accepted. When no
	// auth hook is registered the gate is not consulted, so its value is unused.
	authenticated atomic.Bool
	// authTimer closes the connection if it stays unauthenticated past
	// AuthTimeout; stopped once authenticated. Guarded by mu.
	authTimer *time.Timer
}

// isAuthenticated reports whether the connection has passed first-message auth.
// Only meaningful when the server has an auth hook registered.
func (c *Conn) isAuthenticated() bool {
	return c.authenticated.Load()
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

// isClosed reports whether the connection has been closed.
func (c *Conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Info returns HTTP request information captured at connection time.
func (c *Conn) Info() ConnInfo {
	return c.info
}

// Context returns the connection's base context: the HTTP request context
// for WebSocket/SSE connections, or the context passed to
// [Server.ServeStream]. This is useful for accessing request-scoped values
// like zerolog loggers.
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

func newConn(t transport, server *Server, id uint64, info ConnInfo, ctx context.Context) *Conn {
	c := &Conn{
		transport: t,
		server:    server,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        id,
		ctx:       ctx,
		info:      info,
	}
	if server.options.MaxConcurrentRequests > 0 {
		c.reqSem = make(chan struct{}, server.options.MaxConcurrentRequests)
	}
	return c
}

// acquireRequestSlot reserves one in-flight-request slot against both the
// per-connection and server-wide caps. It never blocks: if either cap is at
// capacity it rolls back any slot already taken and returns false, so the
// caller can reject the request with CodeTooManyRequests. A nil semaphore means
// that cap is disabled. Each successful acquire must be paired with exactly one
// releaseRequestSlot once the request finishes.
func (c *Conn) acquireRequestSlot() bool {
	if c.reqSem != nil {
		select {
		case c.reqSem <- struct{}{}:
		default:
			return false
		}
	}
	if c.server.reqSem != nil {
		select {
		case c.server.reqSem <- struct{}{}:
		default:
			// Roll back the per-connection slot taken above.
			if c.reqSem != nil {
				<-c.reqSem
			}
			return false
		}
	}
	return true
}

// releaseRequestSlot returns the slots reserved by acquireRequestSlot.
func (c *Conn) releaseRequestSlot() {
	if c.reqSem != nil {
		<-c.reqSem
	}
	if c.server.reqSem != nil {
		<-c.server.reqSem
	}
}

// dispatchRequest reserves an in-flight slot and runs handleRequest on its own
// goroutine, or rejects the request with CodeTooManyRequests when the
// connection or server is at capacity. The acquire and its paired release both
// live here (the release fires when the handler goroutine returns), so the
// handlers stay callable in isolation and both transports share one correct
// path.
func (c *Conn) dispatchRequest(msg IncomingMessage) {
	if !c.acquireRequestSlot() {
		c.sendError(msg.ID, CodeTooManyRequests, "too many concurrent requests")
		return
	}
	c.server.requestsWg.Add(1)
	go func() {
		defer c.releaseRequestSlot()
		c.handleRequest(msg)
	}()
}

// dispatchSubscribe is dispatchRequest's counterpart for subscribe frames.
func (c *Conn) dispatchSubscribe(msg IncomingMessage) {
	if !c.acquireRequestSlot() {
		c.sendError(msg.ID, CodeTooManyRequests, "too many concurrent requests")
		return
	}
	c.server.requestsWg.Add(1)
	go func() {
		defer c.releaseRequestSlot()
		c.handleSubscribe(msg)
	}()
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
	data, err := marshalJSON(v)
	if err != nil {
		return err
	}
	return c.sendRaw(data)
}

// sendRaw sends pre-marshaled bytes on the transport.
func (c *Conn) sendRaw(data []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrConnectionClosed
	}
	c.mu.Unlock()

	return c.transport.Send(data)
}

// sendJSONCtx marshals and sends v, returning early if ctx is canceled while
// the transport's outbound queue is full. Used for stream items where the
// sending goroutine must unblock promptly on request cancellation.
func (c *Conn) sendJSONCtx(ctx context.Context, v any) error {
	data, err := marshalJSON(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrConnectionClosed
	}
	c.mu.Unlock()

	return c.transport.SendCtx(ctx, data)
}

func (c *Conn) sendResponse(id, method string, result any) {
	if blob, ok := asBlob(result); ok {
		c.sendBlobResponse(id, method, blob)
		return
	}
	msg := ResponseMessage{
		Type:   TypeResponse,
		ID:     id,
		Result: result,
	}
	data, err := marshalJSON(msg)
	if err != nil {
		// A result that cannot be marshaled (e.g. a NaN float) must not leave
		// the request pending forever: surface it as an internal error instead.
		// Log it too — the client-facing error alone leaves no server-side
		// trace for operators.
		c.server.logger().Error("aprot: failed to encode response", "method", method, "id", id, "error", err)
		c.sendError(id, CodeInternalError, "failed to encode response: "+err.Error())
		return
	}
	_ = c.sendRaw(data)
}

// asBlob reports whether a handler result opts into binary delivery. Only the
// explicit Blob type does; a plain []byte keeps its JSON base64 encoding so
// existing handlers are unaffected. A nil *Blob is not a blob — it falls
// through to the JSON path and reaches the client as a null result, matching
// every other nil pointer result.
func asBlob(result any) (Blob, bool) {
	switch v := result.(type) {
	case Blob:
		return v, true
	case *Blob:
		if v == nil {
			return Blob{}, false
		}
		return *v, true
	}
	return Blob{}, false
}

// blobFallbackResult is the JSON representation of a Blob result on transports
// without binary frames (SSE, stream). The $blob marker lets the client
// convert it back into the same Blob value the binary path delivers, so the
// client-visible result type does not depend on the transport.
type blobFallbackResult struct {
	Blob Blob `json:"$blob"`
}

func (c *Conn) sendBlobResponse(id, method string, blob Blob) {
	if !c.transport.SupportsBinary() {
		msg := ResponseMessage{
			Type:   TypeResponse,
			ID:     id,
			Result: blobFallbackResult{Blob: blob},
		}
		data, err := marshalJSON(msg)
		if err != nil {
			c.server.logger().Error("aprot: failed to encode response", "method", method, "id", id, "error", err)
			c.sendError(id, CodeInternalError, "failed to encode response: "+err.Error())
			return
		}
		_ = c.sendRaw(data)
		return
	}
	frame, err := encodeBinaryFrame(binaryFrameHeader{
		Type:        "response",
		ID:          id,
		ContentType: blob.ContentType,
	}, blob.Data)
	if err != nil {
		c.server.logger().Error("aprot: failed to encode binary response", "method", method, "id", id, "error", err)
		c.sendError(id, CodeInternalError, "failed to encode binary response: "+err.Error())
		return
	}
	// Send failures mean the connection is going away; match the JSON path,
	// which discards sendRaw errors for the same reason.
	_ = c.transport.SendBinary(frame)
}

// errorCode maps a handler error to the protocol code that sendError /
// sendProtocolError / sendStreamEnd would send for it, so the observer can
// report the same code the client receives. Returns 0 for a nil error.
func (c *Conn) errorCode(err error) int {
	if err == nil {
		return 0
	}
	if perr, ok := err.(*ProtocolError); ok {
		return perr.Code
	}
	if code, found := c.server.registry.LookupError(err); found {
		return code
	}
	return CodeInternalError
}

func (c *Conn) sendError(id string, code int, message string) {
	msg := ErrorMessage{
		Type:    TypeError,
		ID:      id,
		Code:    code,
		Message: message,
	}
	_ = c.sendJSON(msg)
}

func (c *Conn) sendProtocolError(id string, perr *ProtocolError) {
	msg := ErrorMessage{
		Type:    TypeError,
		ID:      id,
		Code:    perr.Code,
		Message: perr.Message,
		Data:    perr.Data,
	}
	data, err := marshalJSON(msg)
	if err != nil {
		// An unmarshalable Data payload must not swallow the error frame:
		// resend without it. The remaining fields are plain strings and ints,
		// so this marshal cannot fail.
		msg.Data = nil
		data, _ = marshalJSON(msg)
	}
	_ = c.sendRaw(data)
}

func (c *Conn) sendProgress(id string, current, total int, message string) {
	msg := ProgressMessage{
		Type:    TypeProgress,
		ID:      id,
		Current: &current,
		Total:   &total,
		Message: message,
	}
	_ = c.sendJSON(msg)
}

func (c *Conn) registerRequest(id string, cancel context.CancelCauseFunc) {
	c.mu.Lock()
	if c.closed {
		// The connection was closed between the request goroutine starting and
		// reaching here. close() already swept c.requests, so storing this
		// cancel would leak it: the handler would block on ctx.Done() forever,
		// holding requestsWg and stalling Server.Stop. Cancel immediately.
		c.mu.Unlock()
		cancel(ErrConnectionClosed)
		return
	}
	// A client may (maliciously or accidentally) reuse an in-flight request
	// ID. Overwriting the map entry would orphan the previous request's
	// cancel func — its context would never be canceled until the connection
	// closes. Cancel the shadowed request instead.
	old := c.requests[id]
	c.requests[id] = cancel
	c.mu.Unlock()

	if old != nil {
		old(ErrClientCanceled)
	}
}

// unregisterRequest removes the request's cancel func from the map, but only
// if it is still the one this handler registered. A client may reuse an
// in-flight request ID, in which case registerRequest replaces the map entry
// and cancels the shadowed request. When that shadowed handler later unwinds,
// its deferred unregister must not delete the replacement's cancel func — doing
// so would make the still-running replacement uncancelable via cancel,
// unsubscribe, or connection-close bookkeeping. (#225)
func (c *Conn) unregisterRequest(id string, cancel context.CancelCauseFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reflect.ValueOf(c.requests[id]).Pointer() == reflect.ValueOf(cancel).Pointer() {
		delete(c.requests, id)
	}
}

func (c *Conn) cancelRequest(id string) {
	c.mu.Lock()
	cancel, ok := c.requests[id]
	c.mu.Unlock()
	if ok {
		cancel(ErrClientCanceled)
	}
}

// handleIncomingMessage processes a raw message from any transport.
func (c *Conn) handleIncomingMessage(data []byte) {
	var msg IncomingMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendError("", CodeParseError, "invalid JSON")
		return
	}

	// An auth frame is always handled — for the initial handshake and for
	// mid-session token refresh — and runs synchronously (it is the gate, and
	// must not count against the concurrency cap or race identity updates).
	if msg.Type == TypeAuth {
		c.handleAuth(msg.Token)
		return
	}

	// While a connection with an auth hook is still pending authentication,
	// reject every non-auth frame rather than run any handler.
	if c.server.authRequired() && !c.isAuthenticated() {
		c.sendAuthError("authentication required")
		return
	}

	switch msg.Type {
	case TypeRequest:
		c.dispatchRequest(msg)
	case TypeSubscribe:
		c.dispatchSubscribe(msg)
	case TypeUnsubscribe:
		c.cancelRequest(msg.ID)
		c.handleUnsubscribe(msg.ID)
	case TypeCancel:
		c.cancelRequest(msg.ID)
	default:
		c.sendError(msg.ID, CodeInvalidRequest, "unknown message type")
	}
}

// sendAuthOK / sendAuthError emit the server's response to an auth frame.
func (c *Conn) sendAuthOK() {
	_ = c.sendJSON(AuthResultMessage{Type: TypeAuthOK})
}

func (c *Conn) sendAuthError(message string) {
	_ = c.sendJSON(AuthResultMessage{Type: TypeAuthError, Message: message})
}

// handleAuth validates a token via the server's auth hook. It serves both the
// initial handshake and mid-session refresh:
//   - success: mark authenticated, stop the pending-auth timeout, send auth_ok;
//   - failure while pending: send auth_error and close the connection;
//   - failure while already authenticated (a bad refresh): send auth_error but
//     keep the existing session — a live connection is not downgraded.
//
// With no auth hook registered, an auth frame is accepted as a no-op auth_ok so
// clients configured with getAuthToken still get a clean handshake.
func (c *Conn) handleAuth(token string) {
	if !c.server.authRequired() {
		c.sendAuthOK()
		return
	}

	err := c.server.authHook(c.ctx, c, token)
	if err == nil {
		firstAuth := c.authenticated.CompareAndSwap(false, true)
		if firstAuth {
			c.stopAuthTimer()
		}
		c.sendAuthOK()
		return
	}

	// Only a ProtocolError message (e.g. from ErrAuthFailed) is treated as
	// client-safe. Any other error from the hook — a raw DB/JWT/network failure —
	// is redacted to a generic message so internal detail can't leak to an
	// unauthenticated caller at the auth boundary.
	message := "authentication failed"
	if perr, ok := err.(*ProtocolError); ok {
		message = perr.Message
	}
	c.sendAuthError(message)

	// A failed refresh on an already-authenticated connection keeps the live
	// session; a failure while still pending rejects the connection.
	if !c.isAuthenticated() {
		c.close()
	}
}

// stopAuthTimer cancels the pending-auth timeout, if armed.
func (c *Conn) stopAuthTimer() {
	c.mu.Lock()
	t := c.authTimer
	c.authTimer = nil
	c.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// armAuthTimeout starts the pending-auth timeout: if the connection has not
// authenticated within d, it is sent an auth_error and closed. A non-positive d
// disables the timeout. Called once, after the connection is registered.
func (c *Conn) armAuthTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.AfterFunc(d, func() {
		if c.isAuthenticated() {
			return
		}
		c.sendAuthError("authentication timeout")
		c.close()
	})
	c.mu.Lock()
	c.authTimer = t
	c.mu.Unlock()
}

func (c *Conn) handleRequest(msg IncomingMessage) {
	defer c.server.requestsWg.Done()

	// Report the completed request to the observer (if any). reqCode is updated
	// at each terminal branch; this defer runs after the panic-recovery defer
	// below, so a panicking handler is still reported as CodeInternalError.
	observer := c.server.observer
	reqCode := 0
	if observer != nil {
		start := time.Now()
		defer func() {
			observer.RequestCompleted(RequestEvent{
				Method:   msg.Method,
				Duration: time.Since(start),
				Code:     reqCode,
			})
		}()
	}

	// Each request runs on its own goroutine, so an unrecovered panic would
	// kill the whole process, not just this request (unlike net/http).
	defer func() {
		if r := recover(); r != nil {
			reqCode = CodeInternalError
			c.sendError(msg.ID, CodeInternalError, fmt.Sprintf("handler panicked: %v", r))
		}
	}()

	info, ok := c.server.registry.Get(msg.Method)
	if !ok {
		reqCode = CodeMethodNotFound
		c.sendError(msg.ID, CodeMethodNotFound, "method not found: "+msg.Method)
		return
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	c.registerRequest(msg.ID, cancel)
	defer func() {
		c.unregisterRequest(msg.ID, cancel)
		cancel(nil)
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

	// Add refresh queue for batched trigger processing.
	// Server reference lets TriggerRefreshNow flush mid-handler.
	rq := &refreshQueue{server: c.server}
	ctx = withRefreshQueue(ctx, rq)

	// For streaming handlers, attach a hooks container so middleware can
	// register post-iteration callbacks via OnStreamComplete. Unary
	// handlers never populate the slot; OnStreamComplete is a no-op for
	// them.
	var sHooks *streamHooks
	if info.Kind != HandlerKindUnary {
		ctx, sHooks = withStreamCompleteHooks(ctx)
	}

	// Build and execute middleware chain
	handler := c.server.buildHandler(info)
	result, err := handler(ctx, req)

	// Check if context was canceled
	if ctx.Err() == context.Canceled {
		reqCode = CodeCanceled
		c.sendError(msg.ID, CodeCanceled, "request canceled")
		return
	}

	if err != nil {
		reqCode = c.errorCode(err)
		if perr, ok := err.(*ProtocolError); ok {
			c.sendProtocolError(msg.ID, perr)
		} else if code, found := c.server.registry.LookupError(err); found {
			c.sendError(msg.ID, code, err.Error())
		} else {
			c.sendError(msg.ID, CodeInternalError, err.Error())
		}
		return
	}

	// Streaming handlers hand back a reflect.Value wrapping the returned
	// iter.Seq / iter.Seq2. Drive iteration now that middleware has returned.
	if info.Kind == HandlerKindStream || info.Kind == HandlerKindStream2 {
		seqVal, _ := result.(reflect.Value)
		reqCode = c.errorCode(c.streamIterator(ctx, msg.ID, seqVal, info, sHooks))
		c.server.processRefreshQueue(rq)
		return
	}

	c.sendResponse(msg.ID, msg.Method, result)

	// Process batched refresh triggers after response is sent
	c.server.processRefreshQueue(rq)
}

// streamIterator drives an iter.Seq / iter.Seq2 returned from a streaming
// handler. Each yielded element is sent as a StreamItemMessage; iteration
// stops when the request context is canceled or the transport fails. A
// StreamEndMessage always terminates the stream, carrying an error code
// only on abnormal termination (handler panic, marshal/send error). Clean
// client-side cancellation produces an empty StreamEndMessage.
//
// After iteration ends — for any reason — any callbacks registered via
// OnStreamComplete on the request context are invoked with the final
// cause and the number of items yielded. Cancellation causes surface
// as the sentinel the context carries (ErrClientCanceled,
// ErrConnectionClosed, or ErrServerShutdown), so logging middleware
// can distinguish client disconnect from server shutdown.
// streamIterator returns the terminal error sent on the StreamEndMessage (nil
// for a clean end or client cancellation), so the caller can report the stream's
// outcome code to the observer.
func (c *Conn) streamIterator(ctx context.Context, reqID string, seq reflect.Value, info *HandlerInfo, hooks *streamHooks) error {
	if !seq.IsValid() || seq.Kind() == reflect.Func && seq.IsNil() {
		c.sendStreamEnd(reqID, nil)
		if hooks != nil {
			hooks.run(nil, 0)
		}
		return nil
	}

	yieldType := seq.Type().In(0)
	var streamErr error
	itemCount := 0
	var chunker *streamChunker
	if cfg := c.server.options.StreamChunking; cfg != nil {
		chunker = newStreamChunker(ctx, c, reqID, *cfg)
	}
	yieldFn := reflect.MakeFunc(yieldType, func(args []reflect.Value) []reflect.Value {
		if ctx.Err() != nil {
			return []reflect.Value{reflect.ValueOf(false)}
		}
		var item any
		if info.Kind == HandlerKindStream2 {
			item = []any{args[0].Interface(), args[1].Interface()}
		} else {
			item = args[0].Interface()
		}
		var err error
		if chunker != nil {
			err = chunker.add(item)
		} else {
			err = c.sendJSONCtx(ctx, StreamItemMessage{
				Type: TypeStreamItem,
				ID:   reqID,
				Item: item,
			})
		}
		if err != nil {
			streamErr = err
			return []reflect.Value{reflect.ValueOf(false)}
		}
		itemCount++
		return []reflect.Value{reflect.ValueOf(true)}
	})

	func() {
		defer func() {
			if r := recover(); r != nil {
				streamErr = fmt.Errorf("stream handler panicked: %v", r)
			}
		}()
		seq.Call([]reflect.Value{yieldFn})
	}()

	// Flush any partial chunk before the terminal frame. Items already
	// yielded by the handler must reach the client even when the stream ends
	// abnormally (panic), so flush regardless of streamErr; a flush failure
	// only becomes the stream's error when nothing worse happened first.
	if chunker != nil {
		if err := chunker.close(); err != nil && streamErr == nil {
			streamErr = err
		}
	}

	// Compute the final cause for the completion hooks. Cancellation
	// wins over a late transport error because the cancel is the root
	// reason the handler stopped; we surface the specific CancelReason
	// (ErrClientCanceled / ErrConnectionClosed / ErrServerShutdown)
	// rather than a downstream send error caused by the same shutdown.
	var finalErr error
	if ctx.Err() != nil {
		finalErr = context.Cause(ctx)
	} else if streamErr != nil {
		finalErr = streamErr
	}

	// Run hooks before sending the terminal message. Running first
	// guarantees the log entry reflects the real outcome even if the
	// transport is already torn down by the time sendStreamEnd fires.
	if hooks != nil {
		hooks.run(finalErr, itemCount)
	}

	// Context cancellation or transport close are clean terminations from
	// the client's point of view — no error payload on the end message.
	if streamErr != nil && ctx.Err() == nil && streamErr != ErrConnectionClosed {
		c.sendStreamEnd(reqID, streamErr)
		return streamErr
	}
	c.sendStreamEnd(reqID, nil)
	return nil
}

// sendStreamEnd sends a terminal StreamEndMessage for the given request ID.
// Non-nil err is mapped to a protocol code the same way as sendError.
func (c *Conn) sendStreamEnd(reqID string, err error) {
	msg := StreamEndMessage{Type: TypeStreamEnd, ID: reqID}
	if err != nil {
		if perr, ok := err.(*ProtocolError); ok {
			msg.Code = perr.Code
			msg.Message = perr.Message
			msg.Data = perr.Data
		} else if code, found := c.server.registry.LookupError(err); found {
			msg.Code = code
			msg.Message = err.Error()
		} else {
			msg.Code = CodeInternalError
			msg.Message = err.Error()
		}
	}
	data, merr := marshalJSON(msg)
	if merr != nil {
		// An unmarshalable Data payload must not swallow the terminal frame:
		// resend without it (see sendProtocolError).
		msg.Data = nil
		data, _ = marshalJSON(msg)
	}
	_ = c.sendRaw(data)
}

func (c *Conn) handleSubscribe(msg IncomingMessage) {
	defer c.server.requestsWg.Done()

	// Report the completed subscribe to the observer (if any). See handleRequest
	// for the defer-ordering rationale.
	observer := c.server.observer
	reqCode := 0
	if observer != nil {
		start := time.Now()
		defer func() {
			observer.RequestCompleted(RequestEvent{
				Method:    msg.Method,
				Subscribe: true,
				Duration:  time.Since(start),
				Code:      reqCode,
			})
		}()
	}

	defer func() {
		if r := recover(); r != nil {
			reqCode = CodeInternalError
			c.sendError(msg.ID, CodeInternalError, fmt.Sprintf("handler panicked: %v", r))
		}
	}()

	info, ok := c.server.registry.Get(msg.Method)
	if !ok {
		reqCode = CodeMethodNotFound
		c.sendError(msg.ID, CodeMethodNotFound, "method not found: "+msg.Method)
		return
	}

	// Subscriptions require a reproducible unary result to re-send on refresh;
	// streaming handlers can't satisfy that contract.
	if info.Kind != HandlerKindUnary {
		reqCode = CodeInvalidRequest
		c.sendError(msg.ID, CodeInvalidRequest, "streaming handlers cannot be subscribed: "+msg.Method)
		return
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	c.registerRequest(msg.ID, cancel)
	defer func() {
		c.unregisterRequest(msg.ID, cancel)
		cancel(nil)
	}()

	// Add standard context values
	progress := newProgressReporter(c, msg.ID)
	ctx = withProgress(ctx, progress)
	ctx = withConnection(ctx, c)
	ctx = withHandlerInfo(ctx, info)

	req := &Request{
		ID:     msg.ID,
		Method: msg.Method,
		Params: msg.Params,
	}
	ctx = withRequest(ctx, req)

	// Add trigger collector for subscription
	tc := &triggerCollector{keys: make(map[string]struct{})}
	ctx = withTriggerCollector(ctx, tc)

	// Add refresh queue for batched trigger processing.
	// Server reference lets TriggerRefreshNow flush mid-handler.
	rq := &refreshQueue{server: c.server}
	ctx = withRefreshQueue(ctx, rq)

	// Build and execute middleware chain
	handler := c.server.buildHandler(info)
	result, err := handler(ctx, req)

	if ctx.Err() == context.Canceled {
		reqCode = CodeCanceled
		c.sendError(msg.ID, CodeCanceled, "request canceled")
		return
	}

	if err != nil {
		reqCode = c.errorCode(err)
		if perr, ok := err.(*ProtocolError); ok {
			c.sendProtocolError(msg.ID, perr)
		} else if code, found := c.server.registry.LookupError(err); found {
			c.sendError(msg.ID, code, err.Error())
		} else {
			c.sendError(msg.ID, CodeInternalError, err.Error())
		}
		return
	}

	// If the client unsubscribed while the handler was running, don't register.
	// The context can be canceled in the window after the check above, so report
	// this as canceled (not the default success) to match that sibling branch;
	// no response is sent because the subscriber is already gone.
	if ctx.Err() != nil {
		reqCode = CodeCanceled
		return
	}

	// Register or update subscription with collected trigger keys
	tc.mu.Lock()
	keys := tc.keys
	tc.mu.Unlock()

	if len(keys) > 0 {
		if c.server.subscriptions.has(c.id, msg.ID) {
			c.server.subscriptions.updateKeys(c.id, msg.ID, keys)
			// Re-subscribe with the same ID may carry new params; refresh them
			// so server-driven re-execution doesn't replay the stale ones.
			c.server.subscriptions.updateParams(c.id, msg.ID, msg.Params, msg.Patch)
		} else if ok, limited := c.server.subscriptions.register(&subscription{
			conn:       c,
			id:         msg.ID,
			method:     msg.Method,
			keys:       keys,
			params:     msg.Params,
			wantsPatch: msg.Patch,
		}); !ok {
			if limited {
				// The connection is at its subscription cap; tell the client so
				// it can back off rather than silently dropping the subscribe.
				reqCode = CodeTooManyRequests
				c.sendError(msg.ID, CodeTooManyRequests, "subscription limit reached")
			}
			// Otherwise the connection closed while the handler was running; the
			// subscription was refused so it can't outlive the conn. Either way,
			// don't send a success response.
			return
		} else if observer != nil {
			observer.SubscriptionRegistered(c, msg.Method, msg.ID)
		}
	}

	c.sendResponse(msg.ID, msg.Method, result)

	// Process batched refresh triggers after response is sent
	c.server.processRefreshQueue(rq)
}

// refreshSubscription re-executes a subscription handler server-side
// and sends the updated response directly to the subscriber.
func (c *Conn) refreshSubscription(sub *subscription) {
	defer c.server.requestsWg.Done()
	defer func() {
		if r := recover(); r != nil {
			c.sendError(sub.id, CodeInternalError, fmt.Sprintf("handler panicked: %v", r))
		}
	}()

	// Check that the subscription still exists (may have been unsubscribed)
	if !c.server.subscriptions.has(c.id, sub.id) {
		return
	}

	info, ok := c.server.registry.Get(sub.method)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	c.registerRequest(sub.id, cancel)
	defer func() {
		c.unregisterRequest(sub.id, cancel)
		cancel(nil)
	}()

	// Add standard context values
	progress := newProgressReporter(c, sub.id)
	ctx = withProgress(ctx, progress)
	ctx = withConnection(ctx, c)
	ctx = withHandlerInfo(ctx, info)

	req := &Request{
		ID:     sub.id,
		Method: sub.method,
		Params: sub.params,
	}
	ctx = withRequest(ctx, req)

	// Add trigger collector for dynamic key updates
	tc := &triggerCollector{keys: make(map[string]struct{})}
	ctx = withTriggerCollector(ctx, tc)

	// No refreshQueue — prevents cascading refreshes
	// (TriggerRefresh calls during re-execution are no-ops)

	handler := c.server.buildHandler(info)
	result, err := handler(ctx, req)

	if ctx.Err() == context.Canceled {
		return
	}

	if err != nil {
		if perr, ok := err.(*ProtocolError); ok {
			c.sendProtocolError(sub.id, perr)
		} else if code, found := c.server.registry.LookupError(err); found {
			c.sendError(sub.id, code, err.Error())
		} else {
			c.sendError(sub.id, CodeInternalError, err.Error())
		}
		return
	}

	// If unsubscribed while handler was running, don't send or update
	if ctx.Err() != nil {
		return
	}

	// Update trigger keys (handler may have registered different keys)
	tc.mu.Lock()
	keys := tc.keys
	tc.mu.Unlock()

	if len(keys) > 0 {
		c.server.subscriptions.updateKeys(c.id, sub.id, keys)
	}

	c.sendResponse(sub.id, sub.method, result)
}

func (c *Conn) handleUnsubscribe(id string) {
	c.server.subscriptions.unregister(c.id, id)
}

func (c *Conn) close() {
	c.closeWithCause(ErrConnectionClosed, false)
}

func (c *Conn) closeGracefully() {
	c.closeWithCause(ErrServerShutdown, true)
}

// closeWithCause marks the connection closed, cancels its in-flight requests
// with cause, unregisters its subscriptions, and closes the transport (with a
// close frame when graceful and the transport supports one). It reports
// whether this call performed the transition; false means the connection was
// already closed.
func (c *Conn) closeWithCause(cause error, graceful bool) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.closed = true
	// Cancel all pending requests
	for _, cancel := range c.requests {
		cancel(cause)
	}
	c.mu.Unlock()
	c.server.subscriptions.unregisterConn(c.id)
	if graceful {
		_ = c.transport.CloseGracefully()
	} else {
		_ = c.transport.Close()
	}
	return true
}
