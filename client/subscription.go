package client

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"sync"
)

// subEntry is the client's untyped view of a [Subscription].
type subEntry interface {
	id() string
	method() string
	params() jsontext.Value
	wantsPatch() bool
	// sent records that a subscribe frame is going out, so the next error
	// for this ID answers it.
	sent()
	// deliver hands over a full result. Exactly one of raw and blob is set.
	deliver(raw jsontext.Value, blob *Blob)
	// patch applies a subscription_patch payload. It returns false when the
	// subscription cannot apply it, and the client then re-subscribes to
	// fetch the full result.
	patch(raw jsontext.Value) bool
	// serverError handles an error frame for this ID. An error answering a
	// subscribe frame closes the subscription; an error from a later
	// refresh does not, because the server keeps the subscription and a
	// later refresh may succeed.
	serverError(err error)
	// connLost forgets state tied to the dropped connection.
	connLost()
	// fail closes the subscription with err.
	fail(err error)
}

// SubscribeOption configures a [Subscription].
type SubscribeOption[T any] func(*Subscription[T])

// WithPatch declares that the subscription can apply the patches the server
// sends with aprot.PatchSubscription, instead of receiving a full result
// after every change. apply gets the latest result and the raw patch and
// returns the new result, which is delivered on C like any other.
//
// apply runs on the client's read goroutine, so it must be quick. It must
// not modify current in place: the reader may hold the same value from C.
// Copy what you change. If apply returns an error, the subscription closes
// with that error.
//
// After a reconnect, a patch can arrive before the fresh full result. The
// client then fetches the full result again instead of patching a value
// from the old connection.
func WithPatch[T any](apply func(current T, patch jsontext.Value) (T, error)) SubscribeOption[T] {
	return func(s *Subscription[T]) { s.applyPatch = apply }
}

// OnError registers fn for errors from server-driven refreshes. The server
// keeps a subscription whose refresh failed, and a later refresh may
// succeed, so such an error does not close C. Without OnError, the client
// logs it. fn runs on the client's read goroutine, so it must be quick.
//
// An error answering the subscribe itself (bad params, a permission error,
// an unknown method) is different: it closes C, and Err returns it.
func OnError[T any](fn func(error)) SubscribeOption[T] {
	return func(s *Subscription[T]) { s.onError = fn }
}

// Subscription is a live query. The server runs the handler once, then runs
// it again whenever data it depends on changes, and each new result arrives
// on C.
//
// C holds at most one value. If the reader falls behind, an unread result is
// replaced by the newer one, so a slow reader never holds up the connection
// and always sees the latest data. Every result is a full snapshot, so
// skipping one loses nothing.
//
// The subscription survives reconnects: the client re-subscribes, and the
// fresh result arrives on the same C.
//
// C is closed when the subscription ends: after [Subscription.Close], when
// the ctx passed to [Subscribe] is done, when the client closes, or when the
// server answers the subscribe with an error. [Subscription.Err] then says
// why. Errors from later refreshes do not close C; see [OnError]. Always
// end a subscription you no longer read, typically with defer sub.Close();
// otherwise the server keeps re-running its handler until the client closes.
type Subscription[T any] struct {
	// C delivers each new result.
	C <-chan T

	c          chan T
	client     *Client
	subID      string
	wireMethod string
	rawParams  jsontext.Value
	applyPatch func(T, jsontext.Value) (T, error)
	onError    func(error)

	mu       sync.Mutex // guards the fields below, and every send on c
	stopCtx  func() bool
	closed   bool
	err      error
	current  T
	hasCur   bool
	awaiting bool // a subscribe frame is out and not yet answered
}

// Subscribe opens a subscription to method with params. See [Subscription].
// It does not block: a subscription made while the client is reconnecting is
// sent once the connection is back. An error encoding params closes the
// returned subscription at once.
func Subscribe[T any](ctx context.Context, c *Client, method string, params []any, opts ...SubscribeOption[T]) *Subscription[T] {
	ch := make(chan T, 1)
	s := &Subscription[T]{C: ch, c: ch, client: c, wireMethod: method}
	for _, opt := range opts {
		opt(s)
	}

	raw, err := marshalParams(params)
	if err != nil {
		s.closeWith(err)
		return s
	}
	s.rawParams = raw
	s.subID = c.newID()

	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		s.closeWith(err)
		return s
	}
	c.subs[s.subID] = s
	conn := c.conn
	c.mu.Unlock()

	// See markConnected: if conn is nil here, the reconnect sends it.
	if conn != nil {
		c.sendSubscribe(conn, s)
	}

	stop := context.AfterFunc(ctx, func() { s.end(ctx.Err()) })
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		stop()
	} else {
		s.stopCtx = stop
		s.mu.Unlock()
	}
	return s
}

// sendSubscribe sends a subscribe frame for s on conn, unless s has been
// closed or conn is no longer current. The check and the send happen under
// subSendMu, which Subscription.end also holds while it removes s and sends
// unsubscribe, so the two frames always reach the server in the order the
// decisions were made.
func (c *Client) sendSubscribe(conn wireConn, s subEntry) {
	c.subSendMu.Lock()
	defer c.subSendMu.Unlock()
	c.mu.Lock()
	current := c.subs[s.id()] == s && c.conn == conn
	c.mu.Unlock()
	if !current {
		return
	}
	s.sent()
	// A failed write means the connection is dropping; the reconnect
	// re-sends every subscription.
	_ = c.send(conn, outFrame{Type: "subscribe", ID: s.id(), Method: s.method(), Params: s.params(), Patch: s.wantsPatch()})
}

// Close ends the subscription: it tells the server to stop, and closes C.
// Err then returns nil. Close is safe to call more than once and from any
// goroutine.
func (s *Subscription[T]) Close() { s.end(nil) }

// Err reports why C was closed: nil after [Subscription.Close], ctx.Err()
// when the ctx passed to [Subscribe] ended the subscription, an [*Error]
// from the server, or the client's error when the client closed. It returns
// nil while the subscription is open.
func (s *Subscription[T]) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// end closes the subscription with err and unsubscribes at the server.
func (s *Subscription[T]) end(err error) {
	if !s.closeWith(err) {
		return
	}
	if s.subID == "" {
		return
	}
	c := s.client
	c.subSendMu.Lock()
	defer c.subSendMu.Unlock()
	c.mu.Lock()
	if cur, ok := c.subs[s.subID]; ok && cur == subEntry(s) {
		delete(c.subs, s.subID)
	}
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		// A result already on the wire is dropped by the read loop: the ID
		// is no longer in the table.
		_ = c.send(conn, outFrame{Type: "unsubscribe", ID: s.subID})
	}
}

// closeWith marks the subscription closed and closes C. It reports whether
// this call did it.
func (s *Subscription[T]) closeWith(err error) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.closed = true
	s.err = err
	var zero T
	s.current = zero
	close(s.c)
	stop := s.stopCtx
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	return true
}

func (s *Subscription[T]) id() string             { return s.subID }
func (s *Subscription[T]) method() string         { return s.wireMethod }
func (s *Subscription[T]) params() jsontext.Value { return s.rawParams }
func (s *Subscription[T]) wantsPatch() bool       { return s.applyPatch != nil }
func (s *Subscription[T]) fail(err error)         { s.end(err) }

func (s *Subscription[T]) sent() {
	s.mu.Lock()
	s.awaiting = true
	s.mu.Unlock()
}

func (s *Subscription[T]) connLost() {
	s.mu.Lock()
	var zero T
	s.current = zero
	s.hasCur = false
	s.awaiting = false
	s.mu.Unlock()
}

func (s *Subscription[T]) serverError(err error) {
	s.mu.Lock()
	awaiting := s.awaiting
	s.mu.Unlock()
	if awaiting {
		s.end(err)
		return
	}
	if s.onError != nil {
		s.onError(err)
		return
	}
	s.client.logger.Warn("aprot client: subscription refresh failed", "method", s.wireMethod, "err", err)
}

func (s *Subscription[T]) deliver(raw jsontext.Value, blob *Blob) {
	v, err := decodeResult[T](raw, blob)
	if err != nil {
		s.end(err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.awaiting = false
	s.publishLocked(v)
}

// patch runs only on the read goroutine, as does every other change to
// current, so apply can run without holding s.mu. That lets apply call
// Close without deadlocking.
func (s *Subscription[T]) patch(raw jsontext.Value) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return true
	}
	if s.applyPatch == nil || !s.hasCur {
		s.mu.Unlock()
		return false
	}
	cur := s.current
	s.mu.Unlock()

	next, err := s.applyPatch(cur, raw)
	if err != nil {
		s.end(fmt.Errorf("aprot client: applying patch: %w", err))
		return true
	}
	s.mu.Lock()
	if s.hasCur {
		s.publishLocked(next)
	}
	s.mu.Unlock()
	return true
}

// publishLocked puts v on C, replacing an unread older value. s.mu is held,
// and only publishLocked sends on c, so after the drain the slot is free and
// the send cannot block.
func (s *Subscription[T]) publishLocked(v T) {
	if s.closed {
		return
	}
	if s.applyPatch != nil {
		s.current = v
		s.hasCur = true
	}
	select {
	case <-s.c:
	default:
	}
	s.c <- v
}
