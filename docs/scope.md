# Scope: what aprot owns, and what it doesn't

This document is the rule used to decide whether a feature belongs in aprot.
It exists because the alternative was deciding scope ad hoc, per pull
request, wherever a decision happened to be needed — which is how identity
ended up living on connections for several releases without anyone choosing
that (#330).

## The product

aprot is typed Go↔TypeScript RPC with live subscriptions. WebSocket
with subscription hooks is the product; REST, MCP, and the byte-stream
transport are adapters onto the same handlers. Adapters get full
correctness — the same pipeline, middleware, error mapping, and panic
policy — but when a design decision trades off against the subscription
path, the subscription path wins.

## The rule

> **aprot owns transport concerns** — how a call arrives, how a credential
> travels on the wire, and where identity is stashed so middleware reads it
> the same way on every execution path.
>
> **aprot does not own policy** — what a credential means, how to resolve
> it, how long to trust it, or what it is allowed to do.

A useful test of any rule is that it says no to something. This one decided
two questions from the same issue in opposite directions:

- The request-scoped **principal** (`WithPrincipal` / `PrincipalFrom` /
  `Conn.SetPrincipalProvider`) is **in**: it is a carrier and a population
  guarantee. aprot moves the value; it never inspects it.
- A TTL-memoizing **session cache** for principals is **out**: keying,
  expiry, and logout invalidation are consumer policy. The docs show the
  memoization pattern; shipping it would mean owning staleness, negative
  caching, and cache-stampede bug reports for every consumer's revocation
  story at once.

## Consequences already applied

These are the standing decisions the rule produced. New work should stay
consistent with them.

- **Every cross-path invariant lives in one helper, and every dispatch path
  routes through it.** There are five places a handler executes: socket
  unary, socket streaming, subscribe-first-run, server-driven subscription
  refresh, and request-scoped (REST attached/serverless, MCP). Streaming and
  refresh bypass `Server.invoke` by design, so an invariant installed only
  at that seam silently misses both. Panic policy (`panicError`, #327),
  wire-error mapping (`Conn.sendErrorFor`), principal resolution
  (`Conn.resolvePrincipal`, #330), and connection registration
  (`Server.registerConn`, #347) all follow this shape. A rule that can
  drift per dispatch path eventually does — so the drift is now a CI
  failure: `matrix_test.go` asserts every cross-path invariant on every
  dispatch path (#339). Adding a path means adding a column, adding an
  invariant means adding a row, and a cell that cannot hold must carry a
  reason rather than be absent.
- **A connection the client can use is never missing from the server's
  fan-out set.** The inverse of the presence rule below: presence is a
  transport fact, so the server's own view of it must not lag the client's.
  Registration therefore completes in the accepting goroutine, before the
  config frame reaches the wire and before the pumps start, on both
  accept paths (#347). It used to be a handoff to `run()` over a channel, so
  a client holding the config frame could complete request round-trips while
  `Broadcast` could not see it.
- **Connection presence is a transport fact, never an auth signal.**
  `Connection(ctx) != nil` means "there is a socket here" — nil on REST and
  MCP, and that nil is correct (#329). Nothing in aprot may fake a
  connection to make transport-specific middleware pass; that was the
  detached-conn-by-default mistake in the MCP adapter.
- **Identity is per execution; the connection holds at most a resolver.**
  The principal is resolved once per handler execution — including
  server-driven refreshes, which re-run on the server's schedule and are
  therefore exactly where a stale identity snapshot does the most damage.
  aprot never stores a raw credential; the consumer's `OnAuth` closure
  owns it.
- **`Conn.UserID` is an address, not an identity.** It is the routing key
  for `PushToUser` / `DisconnectUser` fan-out. The principal is the
  authorization input. Consumers set both when they coincide; aprot never
  derives one from the other.
- **Identity is a per-execution snapshot; the address is a live routing
  fact.** This is why the two seams resolve differently (#336). The
  principal is an authorization input, so it must be stable for the
  duration of a handler execution — it is resolved once, up front, on every
  dispatch path. The address only answers "where is this user reachable",
  so `UserID(ctx)` reads through to the connection instead of snapshotting
  at dispatch: a refresh that runs after a mid-session re-authentication
  should fan out to where the user is *now*, and a handler should see an
  address its own middleware just set. Read-through also has no per-path
  code, so it has no drift surface. The carrier for request-scoped paths
  (`WithUserID`) is in for the same reason `WithPrincipal` is: it moves a
  value between the wire and the handler uniformly, and never inspects it.
- **MCP is in as a transport adapter, and experimental until it has a
  user.** It falls on the "in" side of the rule — it moves calls onto the same
  handlers through the same pipeline — so it gets full correctness, with the
  subscription path winning any trade-off. It is marked experimental because
  no real consumer exists yet and the specification churns (the adapter pins
  revision 2025-06-18): its API may be revised without a breaking-change
  entry. It is kept rather than deleted for lack of users because it is the
  **second consumer of the request-scoped seam**, and one consumer is not
  enough — with REST alone that seam drifted REST-shaped for several releases
  (#316, #330). An adapter nobody uses is still the canary for the uniformity
  guarantee, but **only while CI exercises it**: the invariant matrix (#339)
  is the standing keep-condition. If that coverage lapses, deletion becomes
  the right call, on the same "unused in practice" standard that removed the
  SSE transport (#280). Ruling recorded for #340.
- **The SSE transport is out; the transport abstraction stays.** SSE was
  removed in full — `transport_sse.go`, `sse_handler.go`,
  `Server.HTTPTransport`, `ConnectedMessage`/`TypeConnected`, and the
  client's `SSETransport` (#280). No consumer ever used it, and being
  half-duplex it was never a free mirror of the WebSocket path: requests
  arrived on a separate `POST /rpc` keyed by a connection ID the stream
  issued, which meant a second copy of the accept path, of first-message
  auth, and of the inbound size limit. Every protocol feature had to answer
  "and how does this behave on SSE?" before it could ship.

  What stays is everything that was never SSE-specific: the internal
  `transport` interface, `SupportsBinary`, the `$blob` JSON fallback, and the
  byte-stream transport. Those carry the multi-transport guarantee that a
  client-visible result type never depends on the transport, and they are
  load-bearing for the WebSocket binary opt-out (#279) — which is now what
  keeps the fallback path exercised, in unit tests and e2e alike. Deleting an
  unused transport is not deleting the seam that made it cheap; the seam
  earns its keep with two live users.

  The deciding test was the one #280 named: not "has it been used" but "do we
  pay a tax when adding features?" We did, on every feature. Ruling recorded
  for #280.
- **A connection-scoped fact may be reported to the client, but never
  becomes an authorization input.** `SharedTaskState.startedHere` says which
  connection carried the call (#370). That is transport, so carrying it is
  in. It sits next to `isOwner`, which is per user, rather than narrowing it:
  ownership is the cancel policy's input and must survive a reconnect, while
  an origin flag by definition does not. Two flags with one meaning each,
  not one flag that means both.
- **Auth mechanics in, auth meaning out.** First-message auth, the
  pending-auth state, `AuthTimeout`, and mid-session token refresh are wire
  concerns and belong here. Verifying the token, looking up the user, and
  deciding what they may do never will.
- **aprot keeps what an auth hook set, and never undoes it.** A hook that
  calls `Conn.SetUserID` or `Conn.SetPrincipalProvider` and then fails
  keeps both applied — on an error and on a recovered panic alike (#384).
  The hook made those calls. Undoing them would be aprot deciding what the
  call meant, which is the policy side of the rule, and it would mean the
  library overwriting consumer state on a condition the consumer did not
  ask it to watch.

  The invariant lives on the hook instead, and the docs state it: **run the
  checks first, set the address and the principal provider last**, once
  success is certain. A hook written that way has nothing half-applied on
  any failure path, so the question does not arise. One rule the consumer
  can follow beats a partial unwind the library can only ever do halfway —
  it could restore those two fields, but never `Conn.Set` values, the
  consumer's own stores, or external side effects, and never atomically
  against a request already dispatching on the connection.

  This replaces an earlier decision on the same issue to capture both
  fields before the hook and restore them on failure. Ruling recorded for
  #384.

- **Delivery semantics are in; what makes a payload stale is out.**
  `Droppable()` lets a push event opt out of aprot's delivery guarantee, and a
  `Blob` push event goes out as a binary frame (#387). Both are transport: how
  a frame travels, and whether the queue is allowed to grow on its behalf.
  aprot never inspects the payload to decide — the consumer declares it once,
  at registration.

  The **threshold** is the part worth recording, because it is where this
  nearly became policy. #387 proposed "take the non-blocking path", which on
  the existing 256-slot buffer means dropping only once the buffer is full —
  roughly 76 MB and minutes of backlog for the 300 KB video frames that
  motivated it. That is not a droppable event, it is a queue with a late
  panic. The allowance is therefore one unwritten frame per connection, and a
  **constant rather than a `ServerOptions` knob**: the number is the
  semantics, not a tuning parameter. Raising it buys smoothness by adding
  exactly that many stale frames of latency, which is the opposite of what a
  live preview wants. This is the same shape as the #374 ruling — ship the
  mechanism, not the threshold — except that here there is no consumer-side
  comparison to leave out, so aprot must pick, and picking means picking the
  one value that matches the meaning of the word.

  Declared **per event type, not per call**. Staleness is a property of the
  payload, so the same frame is as expendable on a broadcast as on a targeted
  push; one registry lookup serves `Conn.Push`, `Server.Broadcast` and
  `Server.PushToUser`, which is the one-helper rule applied to a fan-out
  invariant rather than a dispatch path. A per-call flag would have needed
  three new methods and could have disagreed with itself across them.

  The paired **no** is head-of-line priority. #387 also asked whether a 300 KB
  frame ahead of a control message is worth designing for. It is not, and a
  priority lane is refused: aprot's protocol is one ordered queue per
  connection, and a second lane means no defined order between a control frame
  and a data frame, on every transport, forever. The droppable allowance is
  itself the mitigation — it is the unbounded queue, not the frame size, that
  turns 300 KB into minutes of delay. Revisit only with a measurement.

  **Opt-in is explicit, never inferred from shape.** A binary push event is a
  named type embedding `Blob` and nothing else, and that is checked exactly.
  The first attempt matched structurally, by reflect convertibility, on the
  reasoning that a struct with Blob's fields *is* a Blob. That was wrong, and
  wrong in the direction the rule cares about: struct conversion ignores tags
  and methods, so a consumer's own `{ContentType string; Data []byte}` with its
  own JSON tags would have been reinterpreted as binary — new wire encoding,
  new generated type, its `MarshalJSON` bypassed — without anyone asking.
  aprot may decide how a frame travels; it may not decide that somebody's type
  means something other than what they wrote. When a feature needs to know an
  intent that the type system cannot express, take the declaration, do not
  infer it from a shape that can coincide.

  What stays **out**: conflation (replace the queued frame with the newer one)
  and per-event allowances. Both decide which value supersedes which, which is
  the consumer's model of its own data, and neither is needed once the queue
  is bounded at one. Ruling recorded for #387.
- **Reporting what the connection is doing is in; deciding when that is
  wrong is out.** `ServerStats.InFlightRequests`, `OldestRequestAge`,
  `Server.InFlightRequests()` and `Conn.InFlightRequests()` report the
  connection's own request bookkeeping — how many executions it is running
  and for how long (#374). That is a transport fact, and it was the one
  connection-scoped structure whose size depends on application behaviour
  with nothing reporting it: `c.values` is keyed by fixed types and
  subscriptions are capped by `MaxSubscriptions`, but a handler that never
  returns keeps its request entry forever, because the unregister runs from a
  `defer` as the handler unwinds. What aprot does **not** ship is a
  `SlowRequestThreshold` option or an `Observer.RequestSlow` event, both of
  which #374 offered. How long a handler may legitimately run is consumer
  policy — it varies per method and per deployment — and owning it would mean
  owning a sweeper goroutine, a default that is wrong for someone, and the
  definition of "slow". Ship the numbers; the consumer's alert is one
  comparison. Ruling recorded for #374.
- **Connection state answers transport questions; aprot exports no
  connection-level auth accessor.** `Conn.Detached()` reports whether there
  is a socket behind the connection, so code that must choose a delivery
  path — push to a live client, or fold the payload into the response — can
  decide up front instead of calling `Push` and handling `ErrDetachedConn`
  after the fact. That is a transport fact, so it is in. Its doc comment
  states what it answers, because `!Detached()` is one careless read away
  from becoming the auth signal #326 was about. The paired ruling is a
  **no**: `NewDetachedConn` does not take the authenticated state, and
  aprot exports no `Conn.Authenticated()`. The `authenticated` flag has one
  job — the first-message gate in `Conn.handleIncomingMessage` — and it
  runs only when a frame arrives off a transport. A
  detached conn has no read loop and is never registered with the server,
  so nothing reads the flag; a constructor argument would advertise a
  control that controls nothing. Exporting a reader would make it real,
  which is the reason not to: connection-level auth state is the #326
  footgun in a new wrapper, and the principal already answers that question
  per execution. Ruling recorded for #342.

## How to use this document

When a proposal arrives, ask which side of the rule it falls on. If it
moves a value between the wire and the handler uniformly across execution
paths, it is probably in. If it decides what a value means, how long it is
valid, or who is allowed to do what, it is probably out — ship the seam
that makes the consumer's implementation one obvious line, and document the
pattern instead. If a proposal genuinely straddles the line, this file is
the place to record the ruling once it is made, so the next one is cheaper.
