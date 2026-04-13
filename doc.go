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
// [Server.Broadcast], [Server.PushToUser], and [Server.ConnectionCount] work
// across all connections regardless of transport.
//
// Use [ServerOptions] to configure client reconnection behavior. The server
// sends this configuration to clients on connect; TypeScript clients apply it
// automatically.
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
//	    conn.SetUserID(session.UserID)
//	    conn.Set(principalKey{}, session.User)
//	    return nil
//	})
//
// Each [Conn] has a unique ID, HTTP request info captured at connection time
// (via [Conn.Info]), and key-value storage (via [Conn.Set], [Conn.Get],
// [Conn.Load]) for caching authentication state or other per-connection data.
//
// [Conn.SetUserID] / [Conn.UserID] is a routing identity used for push
// targeting ([Server.PushToUser]). It is not a security boundary — use the
// stored principal for authorization decisions.
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
// The generator creates split files: client.ts (base client), one file per
// handler group, and optional shared type files for types used across groups.
// Use [NamingPlugin] to customize TypeScript name conventions.
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
//   - map[K]V → Record<K, V>
//   - *T → T (optional field)
//   - time.Time → string (RFC 3339)
//   - sql.NullString → string | null (all sql.Null* types supported)
//   - struct → interface
//   - Registered enum → const object + union type
//
// # Wire Protocol
//
// Messages are JSON objects with a "type" field. Client-to-server: request,
// cancel, subscribe, unsubscribe. Server-to-client: response, error, progress,
// push, config, connected (SSE only).
package aprot
