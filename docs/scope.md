# Scope: what aprot owns, and what it doesn't

This document is the rule used to decide whether a feature belongs in aprot.
It exists because the alternative was deciding scope ad hoc, per pull
request, wherever a decision happened to be needed — which is how identity
ended up living on connections for several releases without anyone choosing
that (#330).

## The product

aprot is typed Go↔TypeScript RPC with live subscriptions. WebSocket/SSE
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
  wire-error mapping (`Conn.sendErrorFor`), and principal resolution
  (`Conn.resolvePrincipal`, #330) all follow this shape. A rule that can
  drift per dispatch path eventually does.
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
- **Auth mechanics in, auth meaning out.** First-message auth, the
  pending-auth state, `AuthTimeout`, and mid-session token refresh are wire
  concerns and belong here. Verifying the token, looking up the user, and
  deciding what they may do never will.

## How to use this document

When a proposal arrives, ask which side of the rule it falls on. If it
moves a value between the wire and the handler uniformly across execution
paths, it is probably in. If it decides what a value means, how long it is
valid, or who is allowed to do what, it is probably out — ship the seam
that makes the consumer's implementation one obvious line, and document the
pattern instead. If a proposal genuinely straddles the line, this file is
the place to record the ruling once it is made, so the next one is cheaper.
