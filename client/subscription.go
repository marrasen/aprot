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
	// sent records that a subscribe frame is going out on conn. Until the
	// first answer arrives, the subscription holds one of the connection's
	// subscribe slots (see maxSubscribesInFlight).
	sent(conn wireConn)
	// awaitingOn returns the connection whose subscribe frame is still
	// unanswered, or nil.
	awaitingOn() wireConn
	// takeAwaiting clears the unanswered state and returns the connection
	// it was on, or nil. Exactly one caller gets a non-nil result, and that
	// caller releases the slot.
	takeAwaiting() wireConn
	// markResend asks for the subscribe frame to be sent again once the
	// pending first answer arrives, on the same slot. takeResend reports and
	// clears that request.
	markResend()
	takeResend() bool
	// deliver hands over a full result. Exactly one of raw and blob is set.
	deliver(raw jsontext.Value, blob *Blob)
	// patch applies a subscription_patch payload. It returns false when the
	// subscription cannot apply it, and the client then re-subscribes to
	// fetch the full result.
	patch(raw jsontext.Value) bool
	// refreshError reports an error from a server-driven refresh. It does
	// not close the subscription: the server keeps it, and a later refresh
	// may succeed. (An error answering the subscribe frame closes it; the
	// client handles that case with fail.)
	refreshError(err error)
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
	awaiting wireConn // connection of an unanswered subscribe frame, or nil
	resend   bool     // re-send the subscribe frame after its first answer
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

// maxSubscribesInFlight caps how many subscribe frames on one connection
// may wait for their first answer at once. The server runs each one's first
// handler call in a request slot, and refuses frames beyond its
// MaxConcurrentRequests (256 by default) with CodeTooManyRequests. Without
// a cap, the resubscribe burst after a reconnect, or a program opening
// hundreds of subscriptions at once, could have some of them refused and
// closed. Later subscriptions wait in a queue; each answer sends the next.
const maxSubscribesInFlight = 64

// sendSubscribe sends a subscribe frame for s on conn, unless s has been
// closed or conn is no longer current. If maxSubscribesInFlight frames are
// already unanswered, s is queued instead and sent by releaseSubSlot.
//
// The check and the send happen under subSendMu, which Subscription.end
// also holds while it removes s and sends unsubscribe, so the two frames
// always reach the server in the order the decisions were made.
func (c *Client) sendSubscribe(conn wireConn, s subEntry) {
	c.subSendMu.Lock()
	defer c.subSendMu.Unlock()
	c.mu.Lock()
	if c.subs[s.id()] != s || c.conn != conn {
		c.mu.Unlock()
		return
	}
	if c.subInFlight >= maxSubscribesInFlight {
		c.subQueue = append(c.subQueue, s)
		c.mu.Unlock()
		return
	}
	c.subInFlight++
	c.mu.Unlock()
	c.writeSubscribe(conn, s)
}

// resubscribe asks the server for s's full result again, after a patch s
// could not apply. If s's first answer is still pending, the re-send waits
// for it and reuses its slot: sending at once would put two frames for s
// in flight on one slot, and the server answers both.
func (c *Client) resubscribe(conn wireConn, s subEntry) {
	if s.awaitingOn() == conn {
		s.markResend()
		return
	}
	c.sendSubscribe(conn, s)
}

// answered handles the first answer to s's subscribe frame on conn: it
// sends a re-send requested meanwhile on the same slot, or frees the slot.
func (c *Client) answered(conn wireConn, s subEntry) {
	if s.takeResend() {
		c.subSendMu.Lock()
		c.mu.Lock()
		current := c.subs[s.id()] == s && c.conn == conn
		c.mu.Unlock()
		if current {
			c.writeSubscribe(conn, s)
			c.subSendMu.Unlock()
			return
		}
		c.subSendMu.Unlock()
	}
	c.releaseSubSlot(conn)
}

// writeSubscribe marks s as awaiting its answer and writes the frame. The
// caller holds subSendMu and has taken a slot for s.
func (c *Client) writeSubscribe(conn wireConn, s subEntry) {
	s.sent(conn)
	// A failed write means the connection is dropping; the reconnect
	// re-sends every subscription.
	_ = c.send(conn, outFrame{Type: "subscribe", ID: s.id(), Method: s.method(), Params: s.params(), Patch: s.wantsPatch()})
}

// releaseSubSlot frees the slot of a subscribe frame on conn that has been
// answered (or whose subscription ended), and sends queued subscriptions
// into the free slots. A release for a connection that is no longer current
// is ignored: markConnected and markDisconnected reset the count.
func (c *Client) releaseSubSlot(conn wireConn) {
	c.subSendMu.Lock()
	defer c.subSendMu.Unlock()
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}
	c.subInFlight--
	var next []subEntry
	for c.subInFlight < maxSubscribesInFlight && len(c.subQueue) > 0 {
		s := c.subQueue[0]
		c.subQueue[0] = nil
		c.subQueue = c.subQueue[1:]
		if c.subs[s.id()] != s {
			continue // closed while queued
		}
		c.subInFlight++
		next = append(next, s)
	}
	c.mu.Unlock()
	for _, s := range next {
		c.writeSubscribe(conn, s)
	}
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
	c.mu.Lock()
	if cur, ok := c.subs[s.subID]; ok && cur == subEntry(s) {
		delete(c.subs, s.subID)
	}
	// Drop it from the slot queue, so closed entries do not pile up while
	// every slot is busy.
	for i, q := range c.subQueue {
		if q == subEntry(s) {
			c.subQueue = append(c.subQueue[:i], c.subQueue[i+1:]...)
			break
		}
	}
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		// A result already on the wire is dropped by the read loop: the ID
		// is no longer in the table.
		_ = c.send(conn, outFrame{Type: "unsubscribe", ID: s.subID})
	}
	c.subSendMu.Unlock()

	// A subscription closed before its first answer gives its slot to the
	// next queued one.
	if aw := s.takeAwaiting(); aw != nil {
		c.releaseSubSlot(aw)
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

func (s *Subscription[T]) sent(conn wireConn) {
	s.mu.Lock()
	s.awaiting = conn
	s.mu.Unlock()
}

func (s *Subscription[T]) awaitingOn() wireConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.awaiting
}

func (s *Subscription[T]) markResend() {
	s.mu.Lock()
	s.resend = true
	s.mu.Unlock()
}

func (s *Subscription[T]) takeResend() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.resend
	s.resend = false
	return r
}

func (s *Subscription[T]) takeAwaiting() wireConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	aw := s.awaiting
	s.awaiting = nil
	return aw
}

func (s *Subscription[T]) connLost() {
	s.mu.Lock()
	var zero T
	s.current = zero
	s.hasCur = false
	s.awaiting = nil
	s.resend = false
	s.mu.Unlock()
}

func (s *Subscription[T]) refreshError(err error) {
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
