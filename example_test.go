package aprot_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/marrasen/aprot"
)

// Request and response types
type CreateUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type CreateUserResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProcessItemsRequest struct {
	Items []string `json:"items"`
}

type ProcessItemsResponse struct {
	Processed int `json:"processed"`
}

// Push event types
type UserUpdatedEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProcessingCompleteEvent struct {
	Count int `json:"count"`
}

// Handlers struct
type MyHandlers struct {
	server *aprot.Server
}

// CreateUser handles user creation
func (h *MyHandlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	progress := aprot.Progress(ctx)
	progress.Update(1, 2, "Validating...")

	if req.Name == "" {
		return nil, aprot.ErrInvalidParams("name is required")
	}

	progress.Update(2, 2, "Creating user...")

	conn := aprot.Connection(ctx)
	if conn != nil {
		conn.Push(&UserUpdatedEvent{ID: "123", Name: req.Name})
	}

	return &CreateUserResponse{ID: "123", Name: req.Name}, nil
}

// ProcessItems demonstrates progress reporting with cancellation
func (h *MyHandlers) ProcessItems(ctx context.Context, req *ProcessItemsRequest) (*ProcessItemsResponse, error) {
	progress := aprot.Progress(ctx)
	total := len(req.Items)

	for i, item := range req.Items {
		select {
		case <-ctx.Done():
			return nil, aprot.ErrCanceled()
		default:
		}

		progress.Update(i+1, total, fmt.Sprintf("Processing %s...", item))
	}

	h.server.Broadcast(&ProcessingCompleteEvent{Count: total})

	return &ProcessItemsResponse{Processed: total}, nil
}

func Example() {
	// Create registry and register handlers
	registry := aprot.NewRegistry()
	handlers := &MyHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, UserUpdatedEvent{})
	registry.RegisterPushEventFor(handlers, ProcessingCompleteEvent{})

	// Create server
	server := aprot.NewServer(registry)
	handlers.server = server

	// Start HTTP server with WebSocket endpoint
	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	// http.ListenAndServe(":8080", mux)

	fmt.Println("Server ready")
	// Output: Server ready
}

func Example_generate() {
	// Create registry with handlers
	registry := aprot.NewRegistry()
	myHandlers := &MyHandlers{}
	registry.Register(myHandlers)

	// Register push events on the registry
	registry.RegisterPushEventFor(myHandlers, UserUpdatedEvent{})

	// Create generator and generate TypeScript to stdout (or file)
	gen := aprot.NewGenerator(registry)
	gen.GenerateTo(os.Stdout)
}

// This example shows how to set up a server with both WebSocket and SSE
// transports running simultaneously.
func Example_dualTransport() {
	registry := aprot.NewRegistry()
	registry.Register(&MyHandlers{})

	server := aprot.NewServer(registry)

	mux := http.NewServeMux()
	mux.Handle("/ws", server)                   // WebSocket
	mux.Handle("/sse", server.HTTPTransport())  // SSE+HTTP
	mux.Handle("/sse/", server.HTTPTransport()) // SSE sub-routes (rpc, cancel)

	fmt.Println("Dual transport ready")
	// Output: Dual transport ready
}

// PublicHandlers has endpoints that require no authentication.
type PublicHandlers struct{}

func (h *PublicHandlers) Ping(ctx context.Context) (string, error) { return "pong", nil }

// ProtectedHandlers has endpoints that require authentication.
type ProtectedHandlers struct{}

func (h *ProtectedHandlers) Secret(ctx context.Context) (string, error) { return "classified", nil }

// This example shows how to define and apply middleware. Server-level
// middleware applies to all handlers; per-handler middleware applies only
// to the handlers registered in the same Register call.
func ExampleServer_Use() {
	loggingMiddleware := func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			start := time.Now()
			result, err := next(ctx, req)
			log.Printf("[%s] %s took %v", req.ID, req.Method, time.Since(start))
			return result, err
		}
	}

	authMiddleware := func(next aprot.Handler) aprot.Handler {
		return func(ctx context.Context, req *aprot.Request) (any, error) {
			conn := aprot.Connection(ctx)
			if _, ok := conn.Load(struct{}{}); !ok {
				return nil, aprot.ErrUnauthorized("not authenticated")
			}
			return next(ctx, req)
		}
	}

	registry := aprot.NewRegistry()
	registry.Register(&PublicHandlers{})                                      // no handler middleware
	registry.Register(&ProtectedHandlers{}, aprot.Middleware(authMiddleware)) // with auth

	server := aprot.NewServer(registry)
	server.Use(aprot.Middleware(loggingMiddleware)) // applies to all handlers

	fmt.Println("Middleware configured")
	// Output: Middleware configured
}

// This example shows how to validate connections and reject unauthorized
// clients using OnConnect. The stored principal can be checked in
// per-handler middleware.
func ExampleServer_OnConnect() {
	registry := aprot.NewRegistry()
	registry.Register(&MyHandlers{})

	server := aprot.NewServer(registry)

	type principalKey struct{}

	server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		for _, cookie := range conn.Info().Cookies {
			if cookie.Name == "session" {
				// validate the session...
				conn.SetUserID("user_123")
				conn.Set(principalKey{}, "authenticated-user")
				return nil
			}
		}
		return nil // allow unauthenticated for public endpoints
	})

	server.OnDisconnect(func(ctx context.Context, conn *aprot.Conn) {
		log.Printf("Client %d disconnected", conn.ID())
	})

	fmt.Println("Lifecycle hooks configured")
	// Output: Lifecycle hooks configured
}

// This example shows graceful shutdown with a timeout.
func ExampleServer_Stop() {
	registry := aprot.NewRegistry()
	server := aprot.NewServer(registry)

	// In production, this would be triggered by a signal handler.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Stop(ctx); err != nil {
		log.Printf("shutdown timed out: %v", err)
	}

	fmt.Println("Server stopped")
	// Output: Server stopped
}

// This example shows how to configure client reconnection behavior.
// The server sends this configuration to clients on connect.
func ExampleNewServer_options() {
	registry := aprot.NewRegistry()
	server := aprot.NewServer(registry, aprot.ServerOptions{
		ReconnectInterval:    2000,  // initial delay (ms)
		ReconnectMaxInterval: 60000, // max delay (ms)
		ReconnectMaxAttempts: 10,    // 0 = unlimited
	})

	_ = server
	fmt.Println("Server with options")
	// Output: Server with options
}

// This example shows how to register Go errors for automatic conversion
// to typed TypeScript error codes.
func ExampleRegistry_RegisterError() {
	registry := aprot.NewRegistry()
	handlers := &MyHandlers{}
	registry.Register(handlers)

	// Registered errors are auto-converted when returned from handlers.
	// Codes are auto-assigned starting at 1000.
	registry.RegisterError(context.DeadlineExceeded, "Timeout")

	// Register code-only for manual use with NewError.
	_ = registry.RegisterErrorCode("InsufficientBalance")

	fmt.Println("Errors registered")
	// Output: Errors registered
}

// This example shows how to register enums for TypeScript generation.
// String-based enums derive names by capitalizing values; int-based
// enums use the String() method.
func ExampleRegistry_RegisterEnumFor() {
	type Status string
	const (
		StatusActive  Status = "active"
		StatusExpired Status = "expired"
	)

	registry := aprot.NewRegistry()
	handlers := &MyHandlers{}
	registry.Register(handlers)
	registry.RegisterEnumFor(handlers, []Status{StatusActive, StatusExpired})

	fmt.Println("Enum registered")
	// Output: Enum registered
}

// This example shows how to use connection-scoped state to store and
// retrieve per-connection data such as authenticated principals.
func ExampleConn_Set() {
	type principalKey struct{}

	// In an OnConnect hook or middleware:
	// conn.Set(principalKey{}, &Principal{ID: "user_123", Role: "admin"})

	// Later, in any handler or middleware:
	// principal, _ := conn.Get(principalKey{}).(*Principal)

	// Use Load to distinguish "not set" from "set to nil":
	// if v, ok := conn.Load(principalKey{}); ok {
	//     principal = v.(*Principal)
	// }

	fmt.Println("Connection state example")
	// Output: Connection state example
}
