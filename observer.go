package aprot

import "time"

// Observer receives server lifecycle and per-request events, letting a
// deployment wire aprot into Prometheus, OpenTelemetry, structured logging, or
// any other telemetry system without aprot depending on those libraries.
//
// Register one via [ServerOptions.Observer]. All methods are called
// synchronously on the server's hot paths and may run concurrently from many
// goroutines, so implementations must be fast, non-blocking, and safe for
// concurrent use — offload any heavy work (I/O, aggregation) to a background
// goroutine. When no observer is registered the server skips this bookkeeping
// entirely, keeping the hot path allocation-free.
//
// Prefer high-cardinality-safe labels: Method is bounded by the handler set,
// but per-user or per-connection dimensions can explode a metrics backend's
// cardinality — aggregate those rather than emitting one series each.
//
// Embed [NoopObserver] to implement only the events you need and remain
// forward-compatible as new methods are added to this interface:
//
//	type myMetrics struct{ aprot.NoopObserver }
//	func (m *myMetrics) RequestCompleted(e aprot.RequestEvent) { /* ... */ }
type Observer interface {
	// ConnectionOpened fires after a connection is accepted, cleared by connect
	// hooks, and registered. ConnectionClosed fires once when it is torn down;
	// conn.UserID() is still valid at that point.
	ConnectionOpened(conn *Conn)
	ConnectionClosed(conn *Conn)

	// RequestCompleted fires once per client-initiated request or subscribe
	// after its handler (and, for streaming handlers, the full iteration)
	// finishes. Server-driven subscription refreshes are reported via
	// RefreshFanout, not here.
	RequestCompleted(event RequestEvent)

	// SubscriptionRegistered and SubscriptionUnregistered bracket an active
	// subscription's lifetime. Unregister fires on explicit unsubscribe and on
	// disconnect — once per subscription still active at close.
	SubscriptionRegistered(conn *Conn, method, id string)
	SubscriptionUnregistered(conn *Conn, method, id string)

	// RefreshFanout fires once per trigger key flushed by a server-side refresh,
	// reporting how many subscriptions matched that key (the fan-out size). A
	// large fan-out for a single trigger is an amplification signal.
	RefreshFanout(key string, matched int)

	// PatchFanout fires once per [Server.PatchSubscription] call, reporting how
	// many matching subscriptions received the patch frame and how many fell
	// back to a full refresh (clients without patch support). A persistently
	// non-zero refreshed count means some clients were generated before patch
	// support and still pay full-result amplification.
	PatchFanout(key string, patched, refreshed int)

	// SendBufferFull fires when a connection's outbound WebSocket buffer is full
	// at enqueue time — an early backpressure / slow-consumer signal and the
	// precursor to a stalled-client write timeout. The frame is not dropped: the
	// send blocks until room frees or the write times out.
	SendBufferFull(conn *Conn)

	// WriteTimedOut fires when an outbound WebSocket write exceeds
	// [ServerOptions.WriteTimeout] and the connection is dropped.
	WriteTimedOut(conn *Conn)

	// PushDropped fires when a push event registered with [Droppable] was
	// skipped because the connection had not yet written the previous one.
	// event is the wire event name, which is bounded by the registered push
	// event set and so safe as a metric label.
	//
	// Unlike SendBufferFull this reports a frame that really was discarded —
	// that is the contract the event opted into. A steady low rate is normal
	// for a producer faster than the slowest client; a rate at or near the
	// production rate means that client is receiving almost nothing.
	PushDropped(conn *Conn, event string)
}

// RequestEvent describes a completed request or subscribe, passed to
// [Observer.RequestCompleted].
type RequestEvent struct {
	// Method is the wire method name, e.g. "Users.GetUser".
	Method string
	// Subscribe is true when the client sent a subscribe frame rather than a
	// one-shot request.
	Subscribe bool
	// Duration is the wall-clock time from dispatch to completion. For streaming
	// handlers it spans the full iteration.
	Duration time.Duration
	// Code is the protocol error code sent to the client, or 0 on success.
	Code int
}

// ServerStats is a point-in-time snapshot of gauge-style server metrics,
// returned by [Server.Stats] for pull-based collection.
type ServerStats struct {
	// Connections is the number of active connections.
	Connections int
	// Subscriptions is the number of active subscriptions across all connections.
	Subscriptions int
	// InFlightRequests is the number of requests currently running across all
	// connections: requests, subscribe first-runs, and server-driven refreshes.
	// A handler that never returns is counted here forever, so a value that only
	// ever grows is the signal that one is stuck.
	InFlightRequests int
	// OldestRequestAge is how long the longest-running in-flight request has
	// been running, or zero when none are in flight. It answers "is something
	// stuck" directly: a healthy server's value stays near its slowest normal
	// handler, while a parked handler pushes it up without bound.
	OldestRequestAge time.Duration
}

// InFlightRequest describes one request that was running when the snapshot was
// taken, returned by [Server.InFlightRequests]. It names the method, which a
// count alone cannot, so a stuck handler can be identified rather than only
// detected.
type InFlightRequest struct {
	// ConnID is the ID of the connection the request arrived on, matching
	// [Conn.ID].
	ConnID uint64
	// UserID is the connection's associated user ID, or empty if none is set.
	UserID string
	// RequestID is the client-supplied request ID, unique per connection while
	// the request is in flight.
	RequestID string
	// Method is the wire method name, e.g. "Users.GetUser".
	Method string
	// Subscribe is true for a subscribe first-run or a server-driven refresh,
	// false for a one-shot request.
	Subscribe bool
	// Age is how long the request has been running.
	Age time.Duration
}

// NoopObserver implements [Observer] with methods that do nothing. Embed it in
// your observer to implement only the events you care about and stay
// forward-compatible as new events are added to the interface.
type NoopObserver struct{}

// Compile-time check that NoopObserver satisfies Observer (and thus that
// embedding it satisfies the interface too).
var _ Observer = NoopObserver{}

func (NoopObserver) ConnectionOpened(*Conn)                         {}
func (NoopObserver) ConnectionClosed(*Conn)                         {}
func (NoopObserver) RequestCompleted(RequestEvent)                  {}
func (NoopObserver) SubscriptionRegistered(*Conn, string, string)   {}
func (NoopObserver) SubscriptionUnregistered(*Conn, string, string) {}
func (NoopObserver) RefreshFanout(string, int)                      {}
func (NoopObserver) PatchFanout(string, int, int)                   {}
func (NoopObserver) SendBufferFull(*Conn)                           {}
func (NoopObserver) WriteTimedOut(*Conn)                            {}
func (NoopObserver) PushDropped(*Conn, string)                      {}
