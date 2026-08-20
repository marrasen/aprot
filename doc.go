// Package aprot is a Go library for building type-safe real-time APIs with
// automatic TypeScript client generation. It supports both WebSocket and
// SSE+HTTP transports.
//
// # Overview
//
// aprot follows a define-register-serve-generate workflow:
//
//  1. Define handler methods on Go structs — each becomes a callable RPC endpoint.
//  2. Register handlers (with optional middleware) on a [Registry].
//  3. Create a [Server] and mount it on your HTTP router.
//  4. Run the [Generator] to emit a fully typed TypeScript client.
//
// The generated client includes standalone functions, React hooks (optional),
// typed error checking, enum const objects, and push event handlers — all
// derived from your Go source code. For details on the generated TypeScript
// API (hook behavior, cancellation, loading states), browse the committed
// example output at example/react/client/src/api/ and
// example/vanilla/client/static/api/ in the repository.
//
// # Handlers
//
// Handler methods are ordinary Go methods on a struct. They must accept
// [context.Context] as the first parameter and return either error (void)
// or (T, error):
//
//	func (h *Handlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) { ... }
//	func (h *Handlers) DeleteUser(ctx context.Context, id string) error { ... }
//	func (h *Handlers) ListUsers(ctx context.Context) ([]User, error) { ... }
//	func (h *Handlers) Add(ctx context.Context, a int, b int) (*SumResult, error) { ... }
//
// Parameters are positional — each Go parameter becomes a separate argument in
// the TypeScript client. Parameter names are extracted from Go source via AST
// parsing, so the names you choose in Go are the names your TypeScript client
// uses.
//
// # Streaming Handlers
//
// Handlers may return [iter.Seq] or [iter.Seq2] instead of a single value to
// stream results incrementally. Each yielded element is delivered to the
// client as a separate wire message, so UIs can render rows as they arrive
// instead of waiting for the full response:
//
//	func (h *Handlers) ListUsers(ctx context.Context) (iter.Seq[*User], error) {
//	    return func(yield func(*User) bool) {
//	        for cursor.Next(ctx) {
//	            var u User
//	            cursor.Scan(&u)
//	            if !yield(&u) {
//	                return // client canceled
//	            }
//	        }
//	    }, nil
//	}
//
//	func (h *Handlers) Prices(ctx context.Context) (iter.Seq2[string, float64], error) { ... }
//
// The generator emits an AsyncIterable on the TypeScript side, so clients
// consume streams with a `for await` loop. Cancellation is bidirectional:
// breaking out of the loop (or calling the hook's cancel function) cancels
// the handler's context immediately, and the next `yield` returns false.
//
// Streaming is WebSocket/SSE only. [Registry.RegisterREST] and
// [Registry.EnableREST] panic at registration time for streaming handlers
// because REST cannot deliver multi-message responses over a single HTTP
// request. See [OnStreamComplete] in the Middleware section for observing
// the real end of a stream from logging / metrics middleware.
//
// For large streams, [ServerOptions.StreamChunking] batches consecutive
// items into single stream_chunk frames (flushed on an item count, byte
// size, or max-delay threshold — see [StreamChunking]) instead of sending
// one frame per item. Batching is transparent to the generated client's
// AsyncIterable but requires a client generated from the same aprot version.
//
// # Binary Blob Responses
//
// Returning [Blob] (or *Blob) from a unary handler opts the result into
// binary delivery:
//
//	func (h *FileHandlers) GetAvatar(ctx context.Context, userID string) (aprot.Blob, error) {
//	    return aprot.Blob{ContentType: "image/png", Data: pngBytes}, nil
//	}
//
// The generated client method is typed Promise<Blob> and resolves a DOM Blob.
// Over WebSocket the payload is sent as a binary frame (4-byte big-endian
// header length, JSON header, raw payload — no base64 inflation). Transports
// without binary frames (SSE, byte-stream) fall back to a JSON envelope whose
// result is {"$blob": {contentType, data}} with base64 data; the generated
// client converts it back into a DOM Blob, so the resolved type is identical
// on every transport. Server-driven subscription refreshes deliver Blobs the
// same way.
//
// Only the explicit Blob type opts in, and only as a top-level result: a
// plain []byte result keeps its base64 string encoding, and a Blob nested in
// another struct, streamed as an item, or used as a parameter travels as
// ordinary JSON ({contentType?, data}).
//
// Hand-written WebSocket clients must decode the binary frame; one that
// handles only text frames drops Blob responses silently, leaving the call
// pending with no server-side trace. See docs/binary-frames.md for the frame
// format and a reference decoder.
//
// Such a client can instead decline binary frames for the lifetime of the
// connection with a "binary=0" query parameter on the upgrade URL, which
// routes Blob results to the JSON $blob envelope described above. Accepted
// values are 1/true/yes/on and 0/false/no/off; an unrecognized value fails
// the upgrade with 400 rather than silently defaulting. The config frame the
// server sends immediately after the upgrade reports the negotiated mode as
// "binaryFrames" (always false on SSE and stream), so a client can verify it
// before making its first call.
//
// # Input Transformation
//
// Request struct fields can be normalized before handler dispatch using
// "transform" struct tags. Transforms run after JSON decoding and before
// struct validation, so validator rules see the cleaned value:
//
//	type SignupRequest struct {
//	    Email    string   `json:"email"    transform:"trim,lowercase" validate:"required,email"`
//	    Username string   `json:"username" transform:"trim"            validate:"required,min=3"`
//	    Slug     string   `json:"slug"     transform:"trimleft=/,lowercase"`
//	    Tags     []string `json:"tags"     transform:"trim,removeempty"`
//	}
//
// Supported ops (applied in the order listed in the tag):
//
//   - trim                   strings.TrimSpace
//   - trimleft[=cutset]      TrimLeft; optional cutset (defaults to whitespace)
//   - trimright[=cutset]     TrimRight; optional cutset (defaults to whitespace)
//   - uppercase              strings.ToUpper
//   - lowercase              strings.ToLower
//   - removeempty            []string only — drop empty elements
//
// Ops apply to string, *string (nil-safe), and []string fields, and the
// walker recurses into nested structs, *struct, and []struct so nested
// tags are picked up automatically. There is no registry opt-in — a
// field is transformed if and only if it carries a "transform" tag.
//
// Every "transform" tag reachable from a handler's param types is
// statically checked at registration time via [ValidateTransformTags].
// Unknown ops, "removeempty" on a non-[]string field, or a "transform"
// tag on an unsupported field type (int, bool, time.Time, …) cause
// [Registry.Register] to panic when the server boots, rather than
// turning every request into a [CodeInvalidParams] response at runtime.
// [ApplyTransforms] is also exposed so the same walker can be invoked
// on ad-hoc values outside the handler flow.
//
// # Request Validation
//
// Request struct fields can declare validation rules via the "validate"
// struct tag, using the vocabulary from github.com/go-playground/validator.
// Validation is opt-in per registry: nothing happens until
// [Registry.SetValidator] is called with a [StructValidator]. The supplied
// [NewPlaygroundValidator] wraps the go-playground implementation and
// produces a structured error payload that flows through to the generated
// TypeScript client:
//
//	type CreateUserRequest struct {
//	    Name  string `json:"name"  validate:"required,min=2,max=100"`
//	    Email string `json:"email" validate:"required,email"`
//	    Age   int    `json:"age"   validate:"gte=13,lte=120"`
//	}
//
//	registry := aprot.NewRegistry()
//	registry.Register(&Handlers{})
//	registry.SetValidator(aprot.NewPlaygroundValidator())
//
// Validation runs after [ApplyTransforms] inside [HandlerInfo.Call], so
// rules like "required,min=1" observe the already-normalized value. Failures
// are returned as a [ProtocolError] with [CodeValidationFailed] and a
// []FieldError payload describing every rule that failed, which the
// generated TypeScript client exposes via its ApiError type.
//
// # Registry
//
// A [Registry] collects handler groups, push events, enums, and custom errors
// for both server dispatch and code generation:
//
//	registry := aprot.NewRegistry()
//	registry.Register(&PublicHandlers{})
//	registry.Register(&AdminHandlers{}, authMiddleware)
//	registry.RegisterPushEventFor(&PublicHandlers{}, UserCreatedEvent{})
//	registry.RegisterEnumFor(&PublicHandlers{}, StatusValues())
//	registry.RegisterError(sql.ErrNoRows, "NotFound")
//
// Each [Registry.Register] call creates a handler group with its own middleware
// chain and a corresponding TypeScript file.
//
// Struct fields declared as any / interface{} are emitted as `unknown` in the
// generated TypeScript. When such a field in practice always carries one
// concrete type, [Registry.OverrideFieldType] refines the generated type
// without touching runtime serialization:
//
//	registry.OverrideFieldType(AuditEvent{}, "Payload", OrderPayload{})
//	// generated: payload?: OrderPayload (interface declared like any other type)
//
// The tasks subpackage uses this to type task metadata: tasks.EnableWithMeta[M]
// emits TaskNode.meta / SharedTaskState.meta as M's interface.
//
// # Middleware
//
// Middleware wraps handlers to add cross-cutting behavior. It follows the
// standard func(next) -> func pattern:
//
//	func LoggingMiddleware() aprot.Middleware {
//	    return func(next aprot.Handler) aprot.Handler {
//	        return func(ctx context.Context, req *aprot.Request) (any, error) {
//	            start := time.Now()
//	            result, err := next(ctx, req)
//	            log.Printf("[%s] %s took %v", req.ID, req.Method, time.Since(start))
//	            return result, err
//	        }
//	    }
//	}
//
// Server-level middleware applies to all handlers. Per-handler middleware
// applies only to the handlers registered in the same [Registry.Register]
// call. Execution order: server middleware (outer) → handler middleware
// (inner) → handler.
//
//	server.Use(LoggingMiddleware())                                // all handlers
//	registry.Register(&ProtectedHandlers{}, AuthMiddleware())     // this group only
//
// Middleware sees a streaming handler "return" as soon as its [iter.Seq]
// value is constructed — before any items have been yielded — so a
// naive `time.Since(start)` measurement logs 0ms for every stream. Call
// [OnStreamComplete] from middleware to register a callback that fires
// when the stream has actually terminated (via exhaustion, handler
// panic, or client cancellation), with the terminal error and the
// number of items delivered:
//
//	func LoggingMiddleware() aprot.Middleware {
//	    return func(next aprot.Handler) aprot.Handler {
//	        return func(ctx context.Context, req *aprot.Request) (any, error) {
//	            start := time.Now()
//	            aprot.OnStreamComplete(ctx, func(err error, items int) {
//	                log.Printf("stream %s done in %s items=%d err=%v",
//	                    req.Method, time.Since(start), items, err)
//	            })
//	            return next(ctx, req)
//	        }
//	    }
//	}
//
// Calling [OnStreamComplete] on a unary-handler context is a no-op, so
// the same middleware can log both streaming and unary handlers without
// branching on handler kind.
//
// # Logging Request Params
//
// [Request.Params] is the raw JSON payload (a [jsontext.Value]) and is
// available to middleware verbatim — log it with "%s" or convert with
// string(req.Params). Two caveats apply when logging params:
//
//   - Secrets leak easily. Login / token / API-key handlers will dump
//     plaintext credentials to your log file unless you redact them.
//   - Params are positional. The wire payload is a JSON array whose
//     elements are the handler arguments in source order; for a handler
//     like Login(username, password string), the wire is
//     `["alice", "hunter2"]` — there are no `password` keys to key-match
//     on. Use a method-name skip list for those.
//
// The example/vanilla/api package ships a reference implementation
// (LoggingMiddleware + LoggingOptions + DefaultLoggingOptions) that
// applies both strategies: recursive object-key redaction for struct
// params, plus a per-method skip list for handlers whose entire input
// is sensitive. Use it as a starting point rather than copying
// `log.Printf("%s", req.Params)` verbatim.
//
// # Server
//
// A [Server] handles WebSocket upgrades, SSE streams, and HTTP POST dispatch.
// Mount it directly for WebSocket, or use [Server.HTTPTransport] for SSE+HTTP:
//
//	server := aprot.NewServer(registry)
//	http.Handle("/ws", server)                   // WebSocket
//	http.Handle("/sse", server.HTTPTransport())  // SSE+HTTP
//	http.Handle("/sse/", server.HTTPTransport())
//
// Both transports can run simultaneously and share connection tracking —
// [Server.Broadcast], [Server.PushToUser], [Server.DisconnectUser], and
// [Server.ConnectionCount] work across all connections regardless of
// transport.
//
// For byte streams HTTP can't reach, [Server.ServeStream] serves one
// connection over any io.ReadWriteCloser using newline-delimited JSON
// framing (one protocol message per line) — the stdio pipes of a child
// process, a Unix domain socket, or a Windows named pipe. This is the entry
// point for embedding a Go backend in a desktop app (e.g. an Electron main
// process spawning the Go binary and bridging to the renderer):
//
//	// stdio: the parent process is the single client.
//	server.ServeStream(ctx, struct {
//	    io.Reader
//	    io.WriteCloser
//	}{os.Stdin, os.Stdout}, aprot.ConnInfo{})
//
//	// or one connection per accepted socket:
//	go server.ServeStream(ctx, c, aprot.ConnInfo{RemoteAddr: c.RemoteAddr().String()})
//
// Stream connections participate fully in hooks, auth, middleware,
// subscriptions, streaming, and push. On the TypeScript side, the generated
// client's ApiClientOptions.transport accepts a custom ClientTransport
// instance to carry the same protocol over any message channel (an Electron
// preload/MessagePort bridge, for example).
//
// Handlers can additionally be exposed over REST/HTTP alongside (or instead
// of) WebSocket. Use [Registry.RegisterREST] for REST-only handlers, or
// [Registry.EnableREST] to mark an existing WebSocket handler for REST as
// well. [NewRESTAdapter] returns an [http.Handler] that serves every
// REST-exposed handler in the registry:
//
//	registry.Register(&UserHandlers{})          // WebSocket only
//	registry.RegisterREST(&TodoHandlers{})      // REST only
//	registry.RegisterMCP(&AssistantTools{})     // MCP tools only (with EnableMCP)
//	registry.Register(&BothHandlers{})          // WebSocket...
//	registry.EnableREST(&BothHandlers{})        // ...and also REST
//	http.Handle("/api/", aprot.NewRESTAdapter(registry))
//
// The generated TypeScript client contains socket-reachable groups only:
// RegisterREST-only and RegisterMCP groups are skipped, since client
// functions for them would call over the socket where their methods
// deliberately do not exist.
//
// HTTP method and path are derived from the handler method name by
// convention (e.g. CreateUser → POST /users/create-user), and path
// parameters are mapped from the Go parameter list. Streaming handlers
// cannot be exposed via REST and will panic at registration — use
// WebSocket or SSE for those.
//
// REST requests run through the same request pipeline as WebSocket and SSE.
// The pipeline has a single transport-agnostic entry point, [Server.Invoke]:
// it installs the request context (handler info, request, refresh queue),
// runs server and group middleware, and flushes refresh triggers on success.
// The socket dispatch, the REST adapter and the MCP adapter all execute
// through it, and custom request-scoped transports can call it directly.
// Consequently middleware sees [HandlerInfoFromContext] and
// [RequestFromContext] on every transport, server middleware registered with
// [Server.Use] applies to REST and MCP requests too (when a [Server] has been
// built from the same [Registry]), and a mutation handler that calls
// [TriggerRefresh] over any transport refreshes subscribed WebSocket/SSE
// clients.
//
// On request-scoped transports (REST, MCP) there is no socket, and
// [Connection] returns nil — connection presence means "there is a socket
// here", never "the caller authenticated". Middleware that must run on
// every transport should not gate on it. A caller that authenticates a
// request itself (e.g. a wrapping http.Handler validating a token) can
// still hand middleware a connection: [Server.NewDetachedConn] returns a
// connection bound to no transport, and [WithConnection] attaches it to
// the request context. Detached connections never enter push fan-out;
// their lifetime is the caller's (per request or per session), and no
// cleanup is needed.
//
// Handlers can also be served as MCP (Model Context Protocol) tools, so an
// AI assistant calls them through the same pipeline, middleware and auth.
// Exposure is per-method opt-in via [Registry.EnableMCP], with model-facing
// names, descriptions and behavior hints ([MCPTool]); the aprot/mcp
// subpackage serves the tools over HTTP. Tool input schemas are produced by
// [Registry.SchemaFor], the same JSON Schema generation OpenAPI uses. Every
// registration mode qualifies, and [Registry.RegisterMCP] registers a group
// whose only surface is MCP: no WebSocket dispatch, no REST routes, no
// OpenAPI entry, and no generated TypeScript client functions — a curated
// model-facing tool group never becomes browser API surface. The aprot/mcp
// adapter panics at construction if a RegisterMCP group has no tools
// enabled via EnableMCP, since such a group would be reachable nowhere.
//
// The aprot/mcp adapter is experimental: its API may change without notice,
// and without a breaking-change entry, until the first real consumer arrives.
// The MCP specification churns and the adapter pins revision 2025-06-18. The
// handlers it serves are not experimental; only the adapter's own surface is.
//
// Godoc drives descriptions in OpenAPI and MCP output, and parameter names
// in REST routes. At development time it is extracted from source on demand;
// deployed binaries have no source, so [GenerateSourceDocsGo] emits a Go
// file that bakes the extracted docs into the build (commit it and
// regenerate alongside the TypeScript clients; the emitted
// RegisterSourceDocs seeds the registry via [Registry.SetSourceDocs]).
//
// Use [ServerOptions] to configure client reconnection behavior and
// connection hardening. The reconnect settings are sent to clients on
// connect; TypeScript clients apply them automatically. The hardening
// settings protect the server from misbehaving clients:
//
//	server := aprot.NewServer(registry, aprot.ServerOptions{
//	    MaxMessageSize: 1 << 20,          // max inbound WS frame / SSE body size (default 4 MiB)
//	    WriteTimeout:   10 * time.Second, // drop peers that stop reading (default 30s)
//	    PingInterval:   30 * time.Second, // WebSocket keepalive ping interval (default 30s)
//	    PongTimeout:    60 * time.Second, // drop peers with no inbound traffic (default 60s)
//
//	    MaxConcurrentRequests:       256,   // in-flight requests per connection (default 256)
//	    MaxServerConcurrentRequests: 10000, // in-flight requests across all connections (default 10000)
//	    MaxSubscriptions:            1024,  // active subscriptions per connection (default 1024)
//	})
//
// Set a field to -1 to disable that limit; zero applies the default.
// Handler panics are recovered per request and surfaced to the client as
// internal errors — one buggy handler cannot take down the process. The
// guarantee holds on every transport and dispatch path: a socket request
// gets an error frame (including stream ends and server-driven subscription
// refreshes), a REST request gets a 500 JSON error, and an MCP tool call
// gets a tool result with isError set, instead of a dropped connection.
// Clients receive a generic "handler panicked" message — a panic value can
// embed internal state, so the value and stack go only to
// [ServerOptions.Logger]. http.ErrAbortHandler is the one exception: the
// REST and MCP adapters re-panic it into net/http, preserving the stdlib's
// abort-quietly convention.
//
// The concurrency caps bound the work a single connection (or the whole
// fleet) can pin at once: each inbound request or subscribe frame takes one
// in-flight slot for the duration of its handler, and each connection may hold
// only MaxSubscriptions active subscriptions (which also bounds refresh
// fan-out). A frame that would exceed a cap is rejected with
// [CodeTooManyRequests] rather than spawning unbounded goroutines.
//
// # Security
//
// The WebSocket upgrader accepts any Origin header by default so that
// non-browser clients work out of the box. Deployments that authenticate
// browsers with cookies must restrict origins with [Server.SetCheckOrigin] —
// otherwise any website a logged-in user visits can open an authenticated
// WebSocket to the server from the user's browser (cross-site WebSocket
// hijacking). [SameOriginCheck] is the recommended argument: it accepts the
// server's own origin (Origin host == request Host, case-insensitive, port
// included) plus any extra origins listed verbatim (for dev proxies that
// rewrite Host while forwarding the browser's Origin), and rejects a missing,
// unparseable, or "null" Origin — the sharp edges (substring look-alikes,
// port confusion, sandboxed-iframe "null") that hand-written checks tend to
// get wrong:
//
//	server.SetCheckOrigin(aprot.SameOriginCheck())                        // production
//	server.SetCheckOrigin(aprot.SameOriginCheck("http://localhost:5173")) // with a dev proxy
//
// Because it rejects requests without an Origin header, SameOriginCheck suits
// browser-facing deployments; keep a custom func for deployments that mix
// browser and non-browser clients on one endpoint.
//
// The SSE and REST transports are plain HTTP, so cross-origin browser clients
// need CORS response headers and OPTIONS preflight handling instead. [CORS]
// returns a standard func(http.Handler) http.Handler wrapper for that, closed
// by default and mirroring the SetCheckOrigin guidance above — list exact
// origins (never "*") whenever AllowCredentials is set for cookie auth:
//
//	cors := aprot.CORS(aprot.CORSOptions{
//	    AllowedOrigins:   []string{"https://app.example.com"},
//	    AllowCredentials: true,
//	})
//	http.Handle("/api/", http.StripPrefix("/api", cors(rest)))
//	http.Handle("/sse", cors(server.HTTPTransport()))
//
// # Authentication
//
// [Server.OnAuth] registers a hook that authenticates a token the client sends
// over the connection itself, so secrets stay out of the URL (and out of access
// / proxy / CDN logs). Registering a hook puts every new connection into a
// pending-auth state: it must send an auth frame
// ({"type":"auth","token":"..."}) with a token the hook accepts before any
// request or subscribe runs; frames sent earlier get an auth_error, and a
// connection that does not authenticate within [ServerOptions.AuthTimeout]
// (default 10s) is closed. The same auth frame on a live connection refreshes
// the token without reconnecting. Works over both WebSocket and SSE.
//
//	server.OnAuth(func(ctx context.Context, conn *aprot.Conn, token string) error {
//	    claims, err := verify(token)
//	    if err != nil {
//	        return aprot.ErrAuthFailed("invalid token")
//	    }
//	    conn.SetUserID(claims.Subject)
//	    return nil
//	})
//
// With no hook registered the flow is unchanged (authenticate via [Server.OnConnect]
// / a URL token if desired). The generated TypeScript client drives this with a
// getAuthToken option and a refreshAuth(token) method.
//
// [ServerOptions.AllowAnonymous] keeps the hook but admits unauthenticated
// connections, for apps that mix public and protected APIs on one endpoint.
// Anonymous connections skip the pending-auth state and the auth timeout, and
// run with [Conn.UserID] "" until they authenticate; a client that sends an
// auth frame later upgrades the live session in place, and a token that is
// offered and rejected still closes an unauthenticated connection. Admitting a
// connection is not authorizing the call — gate protected handlers on the
// principal ([PrincipalFrom]) in the handler or in middleware, never on the
// address or on connection presence. Anonymous connections carry no user ID, so
// [Server.PushToUser] and [Server.DisconnectUser] cannot reach them until they
// authenticate.
//
//	server := aprot.NewServer(registry, aprot.ServerOptions{AllowAnonymous: true})
//	server.OnAuth(authHook) // anonymous clients simply omit getAuthToken
//
// On the client, a short-lived credential must be minted per connection
// attempt: the getConnectParams option (and a URL function) is resolved on
// every attempt, including auto-reconnects, and reconnectOnRejected opts into
// retrying a rejected connection instead of treating it as terminal. See
// docs/auth.md for the full recipe, including React StrictMode wiring.
//
// # Request Identity: the Principal
//
// "Who is asking" is a property of each handler execution, not of a socket.
// aprot carries a request-scoped principal — an opaque value the consumer's
// auth resolves to — in the context: [WithPrincipal] attaches it,
// [PrincipalFrom] reads it back (nil when anonymous). Middleware and
// handlers authorize on the principal; they never infer identity from
// [Connection], whose non-nil result only means "there is a socket here".
//
// Population depends on the transport. On sockets, register a
// [PrincipalProvider] on the connection (typically from the [Server.OnAuth]
// hook via [Conn.SetPrincipalProvider]); it runs once per execution —
// requests, subscribes, and server-driven subscription refreshes — so a
// revoked or upgraded identity takes effect without a reconnect. A provider
// error fails the execution with that error before middleware runs. On
// request-scoped transports (REST, MCP), a wrapping http.Handler that
// authenticates the request attaches the resolved principal directly with
// [WithPrincipal].
//
// A request-scoped execution that carries a connection resolves that
// connection's provider too, so a consumer who installs a detached connection
// (see [Server.NewDetachedConn]) to reuse socket-shaped middleware gets the
// same identity over REST and MCP as over a socket. Precedence when both are
// present: [WithPrincipal] upstream wins and the provider does not run, since
// the wrapper that authenticated the request is the authority on that
// execution. WithPrincipal(ctx, nil) counts as resolved — an explicit
// anonymous result is still a result. A provider error fails the execution
// before middleware runs, with the error's own wire code.
//
// Because the provider runs per execution, the natural way to bound
// identity lookups is a session cache owned by the consumer: memoize the
// resolver keyed by credential or session ID with a TTL of your choosing,
// and revocation takes effect within that TTL on every transport at once.
// Resolving once in OnAuth and returning the snapshot is the degenerate
// cache (TTL = connection lifetime).
//
// The principal is distinct from [Conn.UserID]: UserID is an address — the
// key push fan-out uses to decide where [Server.PushToUser] deliveries go —
// while the principal is an authorization input. Set both when they
// coincide.
//
// # Request Address: UserID(ctx)
//
// The address has its own seam, so a handler can name where its caller is
// reachable without knowing which transport the call arrived on. [UserID]
// resolves the first non-empty of: the value attached with [WithUserID],
// then [Connection](ctx).UserID(). It returns "" when the execution carries
// no address.
//
// On sockets the address comes from [Conn.SetUserID] as before (typically
// from [Server.OnAuth] or auth middleware) and [UserID] reads it through the
// connection. On request-scoped transports (REST, MCP) the same wrapping
// http.Handler that attaches the principal attaches the address next to it:
//
//	ctx := aprot.WithPrincipal(r.Context(), user)
//	ctx = aprot.WithUserID(ctx, user.ID)
//	next.ServeHTTP(w, r.WithContext(ctx))
//
// WithUserID(ctx, "") is ignored and falls through to the connection, so a
// wrapper that forwards a header unconditionally never blanks an attached
// connection's address.
//
// The address is read through, not snapshotted at dispatch: a handler in the
// very request whose middleware called [Conn.SetUserID] sees the new
// address, and a server-driven subscription refresh after a mid-session
// re-authentication fans out to where the user is now. The trade-off is that
// the address can change while a handler runs — read it once into a local
// when a consistent value is needed.
//
// Never authorize on the address. It answers "where do I reach this user",
// not "who is asking"; a non-empty address is not evidence that the caller
// authenticated. Read [PrincipalFrom] for the authorization input.
//
// # Connection Lifecycle
//
// [Server.OnConnect] and [Server.OnDisconnect] hooks react to connection
// events. OnConnect hooks can reject connections by returning an error:
//
//	server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
//	    session, err := validateSession(conn.Info().Cookies)
//	    if err != nil {
//	        return aprot.ErrConnectionRejected("invalid session")
//	    }
//	    conn.SetUserID(session.UserID) // address: where to reach this user
//	    conn.SetPrincipalProvider(func(ctx context.Context) (any, error) {
//	        return lookupUser(ctx, session.UserID) // identity: who is asking
//	    })
//	    return nil
//	})
//
// Each [Conn] has a unique ID, HTTP request info captured at connection time
// (via [Conn.Info]), and key-value storage (via [Conn.Set], [Conn.Get],
// [Conn.Load]) for caching authentication state or other per-connection data.
//
// On the TypeScript side, the connection URL is resolved per connection
// attempt: passing a function instead of a string to the ApiClient
// constructor, or supplying the getConnectParams option, re-runs it on every
// attempt — the initial connect, every auto-reconnect, and every page-wake
// reconnect. That is the contract short-lived credentials rely on, so each
// attempt carries a freshly minted token rather than replaying a stale one.
//
// [Conn.SetUserID] / [Conn.UserID] is an address used for push targeting
// ([Server.PushToUser]). It is not a security boundary. Authorize on the
// request-scoped principal instead, read with [PrincipalFrom] — see "Request
// Identity: the Principal". Storing a principal in the connection's key-value
// store is not equivalent: a value stashed that way is invisible to
// PrincipalFrom, and it freezes identity for the connection's lifetime, so a
// revoked role waits for a reconnect and server-driven refreshes keep running
// against the stale snapshot.
//
// To revoke access mid-session — e.g. when an admin deletes a user who still
// holds an authenticated connection — [Server.DisconnectUser] gracefully
// closes every connection currently associated with a user ID and returns the
// number closed. Each connection's in-flight requests are canceled with
// [ErrConnectionClosed] and its disconnect hooks run through the normal
// teardown path; other users' connections are untouched.
//
// # Observability
//
// Register an [Observer] via [ServerOptions.Observer] to receive connection,
// request, subscription, refresh-fan-out, and send-buffer-pressure events, and
// map them onto Prometheus, OpenTelemetry, or structured logs — aprot takes no
// dependency on a metrics library. Observation is opt-in: with no observer the
// hot path allocates nothing. Embed [NoopObserver] to implement only the events
// you need and stay forward-compatible:
//
//	type metrics struct{ aprot.NoopObserver }
//	func (metrics) RequestCompleted(e aprot.RequestEvent) {
//	    requestDuration.WithLabelValues(e.Method, strconv.Itoa(e.Code)).Observe(e.Duration.Seconds())
//	}
//	server := aprot.NewServer(registry, aprot.ServerOptions{Observer: metrics{}})
//
// Observer methods run synchronously on hot paths, so implementations must be
// fast and non-blocking. For gauge-style values, [Server.Stats] returns a
// pull-based snapshot of active connection and subscription counts.
//
// Set [ServerOptions.Logger] (a *slog.Logger; nil uses slog.Default) to
// receive server-side error logs. Currently logged: response-encode failures —
// a handler result that cannot be marshaled is reported to the client as an
// internal error and logged with the method name, so the failure leaves a
// trace operators can find — and handler panics, logged with the method
// name, panic value, and stack trace (clients only receive a generic
// message, so the value and stack exist nowhere else). [Server.Logger]
// returns the effective logger, so adapters and middleware can write to
// the same sink.
//
// # Push Events
//
// Push events are server-to-client messages broadcast to all connected clients
// or targeted to specific users:
//
//	server.Broadcast(&UserCreatedEvent{ID: "1", Name: "Alice"})
//	server.PushToUser("user_123", &NotificationEvent{Message: "hello"})
//
// Push event types must be registered with [Registry.RegisterPushEventFor].
// The event name on the wire is derived from the Go type name.
//
// # Subscription Refresh
//
// Subscription refresh automatically pushes updated query results to clients
// when related data changes. Query handlers declare trigger keys with
// [RegisterRefreshTrigger], and mutation handlers fire them with
// [TriggerRefresh]:
//
//	// Query handler — declares dependency on "users" trigger key
//	func (h *H) ListUsers(ctx context.Context) ([]User, error) {
//	    aprot.RegisterRefreshTrigger(ctx, "users")
//	    return h.db.ListUsers(ctx)
//	}
//
//	// Mutation handler — fires trigger to refresh all subscribed clients
//	func (h *H) CreateUser(ctx context.Context, req *CreateReq) (*User, error) {
//	    user, err := h.db.CreateUser(ctx, req)
//	    if err != nil {
//	        return nil, err
//	    }
//	    aprot.TriggerRefresh(ctx, "users")
//	    return user, nil
//	}
//
// Multiple TriggerRefresh calls within a single request are batched and
// deduplicated. [TriggerRefreshNow] flushes the queue immediately — use it in
// long-running handlers that make observable state transitions over time.
// TriggerRefresh works on every transport: a mutation arriving over REST
// refreshes subscribed WebSocket/SSE clients just like one arriving over a
// socket (the [RESTAdapter] must share its [Registry] with a [Server]).
//
// From background goroutines, cron jobs, webhook fan-in, or any other code
// path that runs outside of a request handler, use the [Server.TriggerRefresh]
// method instead — it flushes immediately and does not require a request
// context:
//
//	go func() {
//	    for range ticker.C {
//	        server.TriggerRefresh("users")
//	    }
//	}()
//
// [RegisterRefreshTrigger] takes variadic strings that form a composite key.
// It is a no-op when called from a non-subscribe request. The package-level
// [TriggerRefresh] is a no-op outside a request context. Subscriptions are
// cleaned up automatically on client disconnect.
//
// # Subscription Patches
//
// TriggerRefresh re-runs the query and re-sends the entire result, which
// amplifies small mutations into large frames on big subscribed collections.
// [PatchSubscription] pushes a small typed patch instead:
//
//	func (h *H) SetRating(ctx context.Context, id string, rating int) error {
//	    h.store.SetRating(id, rating)
//	    return aprot.PatchSubscription(ctx, RatingPatch{ID: id, Rating: rating}, "photos")
//	}
//
// Subscribers that registered a patch reducer receive the payload as a
// subscription_patch frame and apply it client-side; everyone else falls back
// to a full refresh automatically, so mixed client fleets stay consistent. In
// generated React clients the reducer is the applyPatch hook option, applied
// to the shared query-cache snapshot so every component sees it:
//
//	const { data } = useListPhotos({ applyPatch: mergeByKey('id') });
//
// mergeByKey builds the common reducer for keyed-array results; wrapped
// results take a hand-written reducer. Vanilla clients pass onPatch to the
// generated subscribe functions and fold patches themselves. Patches are
// meant for in-place updates to existing entries — keep using TriggerRefresh
// for structural changes (items added or removed). [Server.PatchSubscription]
// is the out-of-request variant, and [Observer.PatchFanout] reports how many
// subscribers took each path.
//
// # Error Handling
//
// Return [ProtocolError] values from handlers to send structured errors to
// clients. Built-in helpers cover common cases:
//
//	aprot.ErrUnauthorized("invalid token")      // -32001
//	aprot.ErrForbidden("access denied")         // -32003
//	aprot.ErrInvalidParams("name is required")  // -32602
//	aprot.ErrInternal(err)                      // -32603
//
// Register Go errors with [Registry.RegisterError] for automatic conversion.
// The generated TypeScript client includes typed error checking:
//
//	registry.RegisterError(sql.ErrNoRows, "NotFound")
//	// In TypeScript: err.isNotFound(), ErrorCode.NotFound
//
// A handler result that cannot be encoded as JSON (for example a float
// carrying NaN or Inf, which JSON rejects) is surfaced to the client as an
// internal error (-32603) rather than leaving the request pending. Likewise,
// an error or stream-end frame whose Data payload cannot be encoded is resent
// without the payload so the terminal frame always reaches the client.
//
// # Connection Errors (TypeScript Client)
//
// Connection-level failures surface as a structured ConnectionError on the
// generated TypeScript client — separate from ApiError, which represents a
// structured server-side error response. ConnectionError extends Error and
// exposes a typed `reason` field so apps can render appropriate UI:
//
//	'offline'         — navigator.onLine was false at failure time.
//	'server-rejected' — server sent ApiError with code ConnectionRejected
//	                    before closing, or a first-message auth_error failed
//	                    the handshake; the original ApiError is attached as
//	                    err.cause.
//	'server-closed'   — transport closed cleanly after the WebSocket upgrade
//	                    completed; err.closeCode and err.closeReason carry
//	                    the WebSocket CloseEvent fields.
//	'network-error'   — pre-upgrade failure or close code 1006 (refused,
//	                    unreachable, TLS, HTTP error during upgrade).
//	                    Browsers deliberately collapse these into one bucket.
//	'manual'          — caller invoked client.disconnect().
//
// In-flight request and requestStream calls reject with a ConnectionError
// when the connection drops. Calls issued while disconnected reject with the
// most recent ConnectionError, falling back to 'offline' or 'manual' when
// none is available. Use client.onConnectionError(listener) to drive UI like
// an "Offline" banner; client.getLastConnectionError() returns the most
// recently observed error or null.
//
// The 'server-rejected' bucket is also surfaced via the existing
// onConnectionRejected ApiClientOption callback — both fire for the same
// underlying event, kept for backward compatibility.
//
// A rejection stops auto-reconnect, which is the right response to a real
// sign-out. For short-lived credentials, where a rejection is usually an
// expired token, session propagation lag, or clock skew, the
// reconnectOnRejected option retries instead — {delayMs, maxAttempts}, or true
// for the defaults — and disconnect() cancels a pending retry.
// client.getLastRejection() returns the ApiError from the most recent
// rejection (null after the next successful connect), so a UI can tell
// "session expired" from "server unreachable" even after later transport
// failures overwrite getLastConnectionError(). The React useConnection() hook
// exposes it as `rejection`.
//
// # Enum Support
//
// Register Go enum types with [Registry.RegisterEnumFor] or
// [Registry.RegisterEnum] to generate TypeScript const objects with full type
// safety. String-based enums derive names by capitalizing values; int-based
// enums use the String() method:
//
//	type Status string
//	const (
//	    StatusActive  Status = "active"
//	    StatusExpired Status = "expired"
//	)
//	func StatusValues() []Status { return []Status{StatusActive, StatusExpired} }
//
//	registry.RegisterEnumFor(handler, StatusValues())
//
// Struct fields with enum types generate TypeScript fields typed as the enum
// union (not raw string/number).
//
// # Code Generation
//
// [Generator] reads a [Registry] and emits TypeScript client code. It supports
// two output modes: [OutputVanilla] (standalone functions + subscribe helpers)
// and [OutputReact] (adds React hooks with auto-refetch and mutation state):
//
//	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
//	    OutputDir: "./client/src/api",
//	    Mode:      aprot.OutputReact,
//	})
//	gen.Generate()
//
// The generated client never connects automatically: constructing an
// ApiClient (or mounting the React <ApiClientProvider>, a plain context
// provider) only configures it. TypeScript callers must invoke
// client.connect() once after construction — otherwise every request, hook,
// and subscription rejects with a ConnectionError ("Not connected").
// Awaiting connect() is optional; requests issued while connecting are
// buffered and flushed when the connection is ready, and auto-reconnect
// takes over after the first successful call:
//
//	const client = new ApiClient(getWebSocketUrl());
//	await client.connect(); // REQUIRED — never called automatically
//
// connect() is cheap and idempotent: a no-op while connected or connecting,
// and an immediate attempt otherwise — including while a reconnect backoff is
// pending, in which case the backoff is abandoned rather than waited out. Call
// it whenever the connection needs to be live (after signing in, on resume)
// rather than caching an "already connected" flag, which skips the call
// exactly when the socket has since dropped. client.reconnectNow() does the
// same without the connected/connecting no-op, for a socket the runtime left
// half-open that still reports 'connected'. Neither clears subscriptions or
// rejects in-flight requests the way disconnect() + connect() does.
//
// The generator creates split files: client.ts (base client), one file per
// handler group, and optional shared type files for types used across groups.
// A shared file is named after its Go package (e.g. api.ts); if that name
// would collide with a handler file (package "settings" and handler "Settings"
// both map to settings.ts), the shared file is emitted as {pkg}.types.ts
// instead so neither overwrites the other.
// Use [NamingPlugin] to customize TypeScript name conventions.
//
// Generated function names — methods, hooks and push-event handlers — are
// escaped with a trailing underscore when they would collide with a TypeScript
// reserved word, so a handler named Delete emits "export function delete_(...)"
// rather than a module that does not parse. Strict-mode-only reserved words
// (let, static, yield, await) are escaped too, because generated modules are
// always strict. The escape runs after the [NamingPlugin], so a custom plugin
// cannot reintroduce a reserved name, and it is cosmetic: the wire method name
// (for example "Users.Delete") is unchanged, as are handler parameter names on
// the wire, which are matched by position.
//
// [Generator.GenerateTo] writes the same client to a single io.Writer, all
// handler groups combined into one file. Both entry points render the same
// templates — the same ApiClient, the same per-method functions, subscribe
// helpers and (in [OutputReact]) hooks — so the split and unsplit outputs
// cannot drift apart. Generate is still the better default: shared type files,
// Zod schemas and stale-file cleanup all need a directory to write into.
//
// Generate also cleans up after itself: top-level .ts files in OutputDir that
// begin with the "// Code generated by aprot. DO NOT EDIT." marker but were
// not produced by the current run (leftovers from a renamed or removed handler
// group) are deleted, so stale files cannot break the TypeScript build.
// Hand-written files, non-.ts files, and subdirectories are never touched.
//
// Setting [GeneratorOptions.Zod] emits a companion `.schema.ts` file for
// every handler group whose request types carry "validate" tags. The
// resulting Zod schemas mirror the server-side validation rules field
// for field — so the TypeScript client can reject bad input before it
// hits the wire, using the same constraints the server will enforce on
// arrival:
//
//	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
//	    OutputDir: "./client/src/api",
//	    Mode:      aprot.OutputReact,
//	    Zod:       true,
//	})
//
// For REST-exposed handlers, [NewOpenAPIGenerator] produces an OpenAPI
// 3.0 document describing every REST endpoint in the registry. Go doc
// comments on handler methods become `summary` / `description`, struct
// and field doc comments flow into JSON Schema descriptions, and
// "validate" tags become JSON Schema constraints:
//
//	oag := aprot.NewOpenAPIGenerator(registry, "My API", "1.0.0")
//	spec, err := oag.Generate()
//	// or: jsonBytes, err := oag.GenerateJSON()
//
// Use [OpenAPIGenerator.WithBasePath] when the API is mounted behind a
// proxy or at a non-root path.
//
// # TypeScript Mutation Patterns (React)
//
// aprot does not generate per-handler mutation hooks (no useXxxMutation()).
// Mutations use one of two patterns:
//
// Pattern 1 — query-scoped mutate(action) (refetch on completion). Every
// useXxx() query hook returns a `mutate` helper alongside data / isLoading /
// error. It accepts either a Promise or a (client: ApiClient) => Promise<unknown>
// thunk; it runs the action, captures any thrown error in `error`, and
// refetches the query on success. Loading / error state is shared with the
// query so a single indicator covers both the action and the refresh:
//
//	const { data, mutate, isLoading, error } = useListTodos();
//	<button onClick={() => mutate((client) => addTodo(client, { title: 'Buy milk' }))} />
//
// The thunk receives the same ApiClient the hook is bound to, so callers
// don't need a separate useApiClient() at the call site. A bare Promise is
// also accepted, useful when composing operations:
//
//	mutate(Promise.all([addTodo(c, a), addTodo(c, b)]));
//
// Pattern 2 — raw async function via useApiClient(). Generated standalone
// functions (addTodo, createUser, etc.) are typed Promise<TRes> that throw
// ApiError / ConnectionError on failure. Use them when there's no surrounding
// query (no list to refetch) or when you need conditional try/catch:
//
//	const client = useApiClient();
//	try {
//	    const todo = await addTodo(client, { title: 'Buy milk' });
//	} catch (err) {
//	    if (err instanceof ApiError && err.isValidationFailed()) { ... }
//	    else { throw err; }
//	}
//
// You manage isLoading / error / AbortController yourself. The trade-off is
// honest: the function does what its type says — no hidden state machine.
//
// Earlier versions of aprot generated useXxxMutation() hooks whose mutate()
// swallowed errors and returned `undefined as TRes` on failure. They were
// removed because the Promise<TRes> type lied at runtime, void mutations
// could not distinguish success from "not yet called", and the only correct
// after-success pattern (useEffect([data])) was non-obvious. See the
// repository's MIGRATION_MUTATION_HOOKS.md for a rewrite prompt.
//
// # Global Error Capture (TypeScript Client)
//
// Wrap a region of the React tree in <ApiClientErrorProvider> to surface every
// API error inside a single hook, instead of wiring per-call try/catch. Errors
// from imperative client.request() / requestStream() / subscribe() calls AND
// from generated query / stream / mutate hooks all flow through the provider,
// because the client reports its own failures through
// ApiClient.onRequestError and the provider subscribes to that. A module that
// imports the client directly and never touches React reports the same way a
// component does:
//
//	import { ApiClientProvider, ApiClientErrorProvider, useApiClient,
//	         useApiClientError } from './api/client';
//
//	<ApiClientProvider value={client}>
//	  <ApiClientErrorProvider>
//	    <App />
//	  </ApiClientErrorProvider>
//	</ApiClientProvider>
//
// Read with useApiClientError():
//
//	const { error, source, clear } = useApiClientError();
//	// source is { struct, method } | null — null exactly when error is null.
//	// A failure from client.request('Todos.CreateTodo', …) yields
//	// source = { struct: 'Todos', method: 'CreateTodo' }, so a banner can
//	// name the failing call without each call site reporting itself.
//
// The source is parsed from the wire name on the first dot. Calls whose wire
// name has no dot set struct to ” and put the full name in method.
//
// Only the latest error is held (newer overrides older); clear() resets both
// error and source. The provider observes errors but does not swallow them —
// calls still throw, so per-hook `error` fields and explicit try/catch keep
// working. Without <ApiClientErrorProvider> above, nothing subscribes and
// useApiClientError() throws — adoption is opt-in.
//
// The same reporting is available without React. client.onRequestError(fn)
// registers a listener for every failed call the client makes — a rejected
// request, a subscription error, or a stream that throws — receiving the error
// and the { struct, method } source. It returns an unsubscribe function, and
// multiple listeners are supported. Use it to route failures to a logger or a
// toast in vanilla output, where the provider is not available:
//
//	const stop = client.onRequestError((err, source) => {
//	    console.error(`${source.struct}.${source.method} failed`, err);
//	});
//
// # React Suspense
//
// In addition to per-handler hooks like `useListUsers()` that return
// `{data, isLoading, error}`, OutputReact also emits `useQuerySuspense` --
// a single generic hook that pairs aprot's promise-returning query
// functions with React 19's `use()` and `<Suspense>` boundaries:
//
//	import { Suspense } from 'react'
//	import { useQuerySuspense } from './api/client'
//	import { listUsers, getUser } from './api/handlers'
//
//	function UsersList() {
//	    const data = useQuerySuspense(listUsers)        // no params
//	    return data.users.map(u => <li key={u.id}>{u.name}</li>)
//	}
//
//	function UserView({ id }: { id: string }) {
//	    const user = useQuerySuspense(getUser, id)      // typed params
//	    return <h1>{user.name}</h1>
//	}
//
//	<Suspense fallback={<Spinner />}>
//	    <ErrorBoundary fallback={<ErrorView />}>
//	        <UsersList />
//	    </ErrorBoundary>
//	</Suspense>
//
// `useQuerySuspense` opens a server subscription on first read (using the
// same TriggerRefresh machinery as `useQuery`), suspends the component
// until the first response arrives, and replaces the cached promise with a
// new resolved one on each subsequent server push -- so live updates flow
// without re-suspending. Errors thrown by the handler are propagated to
// the nearest error boundary.
//
// The hook works directly with the generated query functions because each
// one carries a `.method` property identifying its wire method:
//
//	export function listUsers(client: ApiClient, options?: RequestOptions): Promise<ListUsersResponse> { ... }
//	listUsers.method = 'PublicHandlers.ListUsers' as const;
//
// No per-handler Suspense hook is generated; the single generic hook plus
// the metadata is enough. Requires React 19+. Streams continue to use
// `useStream`, and mutations use the patterns described above (query.mutate
// or the raw async function) — only queries fit the Suspense paradigm
// cleanly.
//
// # Keep Previous Data (React)
//
// Query hooks default to `keepPreviousData: true`: when a param change
// starts a params-keyed reload, the hook keeps returning the previous
// params' data instead of flashing an empty loading state (opt out per
// hook with `{ keepPreviousData: false }`). The pure selector behind the
// option, `selectWithPreviousData`, is exported from the generated client
// along with its `SubscriptionSnapshot<T>` type, so hand-written stores
// that call the generated RPC functions imperatively can reuse the same
// semantics instead of re-deriving them:
//
//	import { selectWithPreviousData, type SubscriptionSnapshot } from './api/client'
//
//	const prev: { current: SubscriptionSnapshot<Draft> | null } = { current: null }
//	const effective = selectWithPreviousData(prev, { data, error, isLoading })
//
// The returned snapshot carries the previous `data` through the reload's
// null gap but always the current `error` and `isLoading` flags — kept
// data never masks the loading or error state.
//
// # Context Helpers
//
// Several functions extract request-scoped values from context:
//
//   - [Progress] — returns the [ProgressReporter] for long-running operations
//   - [Connection] — returns the [Conn] for the current request
//   - [HandlerInfoFromContext] — returns [HandlerInfo] metadata
//   - [RequestFromContext] — returns the [Request] with ID, method, and params
//   - [CancelCause] — returns why the request was canceled (see [ErrClientCanceled], [ErrConnectionClosed], [ErrServerShutdown])
//
// # Graceful Shutdown
//
// [Server.Stop] rejects new connections (503), sends close frames, waits for
// in-flight requests to complete, and runs disconnect hooks. It is safe to
// call multiple times:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	defer cancel()
//	server.Stop(ctx)
//
// # Type Mapping
//
// Go types are mapped to TypeScript during generation:
//
//   - string → string
//   - int, float64, etc. → number
//   - bool → boolean
//   - []T → T[]
//   - [N]T → tuple [T, T, ...] for N ≤ 16, T[] above that ([N]byte → string,
//     base64-encoded on the wire like []byte; Zod schemas emit z.tuple([...])
//     / z.array(...).length(N) to match)
//   - map[K]V → Record<K, V> (map[bool]V → Partial<Record<"true" | "false", V>>)
//   - *T (json:,omitempty) → T (optional field); bare *T → T | null (always sent)
//   - time.Time → string (RFC 3339)
//   - sql.NullString → string | null (all sql.Null* types supported). The
//     generic sql.Null[T] is unwrapped at runtime for the common T (string,
//     int, int64, int32, int16, float64, bool, time.Time); other
//     instantiations fall back to the {"V":…,"Valid":…} object shape.
//   - json.RawMessage → unknown
//   - Blob as a top-level result → DOM Blob (binary delivery; see Binary
//     Blob Responses); nested/streamed Blob → { contentType?: string;
//     data: string }
//   - struct → interface
//   - Registered enum → const object + union type
//
// time.Duration has no default JSON representation in the v2 encoder and is
// rejected at generation time; add a json format option (e.g.
// `json:"d,format:nano"`) or use a different type.
//
// Per-field `format:` tags are supported on every marshal/unmarshal path
// (results, params, push/refresh payloads, stream items): aprot enables
// json/v2's format-tag support internally, so consumers do not need to enable
// it themselves. The tag remains experimental in the standard library, which
// exports no constructor for the opt-in, so aprot passes the one from
// github.com/go-json-experiment/json to the stdlib marshaler; that module is
// aprot's only remaining non-test dependency besides the validator and the
// WebSocket driver, and it will go away when the standard library exports its
// own opt-in.
//
// # Wire Protocol
//
// Messages are JSON objects with a "type" field. Client-to-server: request,
// cancel, subscribe, unsubscribe. Server-to-client: response, error, progress,
// push, config, subscription_patch, connected (SSE only). Streaming adds
// stream_item / stream_chunk / stream_end.
//
// # Design Scope
//
// aprot owns transport concerns — how a call arrives, how a credential
// travels on the wire, and where identity is stashed so middleware reads it
// the same way on every execution path. It does not own policy — what a
// credential means, how to resolve it, how long to trust it, or what it is
// allowed to do; those stay with the consumer, and aprot ships the seam
// that makes the consumer's implementation one obvious line (the principal
// carrier is in; a session cache is not). The rule, and the standing
// decisions made under it, are recorded in docs/scope.md in the repository.
package aprot
