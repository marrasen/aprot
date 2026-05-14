// Package main is a minimal aprot server demonstrating tasks.WithTaskMiddleware.
//
// Run it:
//
//	go run .
//
// Trigger the demo handler from another terminal:
//
//	curl -sX POST http://localhost:8080/rpc \
//	    -H 'Content-Type: application/json' \
//	    -d '{"id":"1","method":"Demo.Import","params":["accounts.csv"]}'
//
// Watch the server log: every task and subtask reports started → completed
// (or failed) through the middleware, with the request_id attribute attached
// to ctx by the start-of-task hook flowing into all downstream slog calls.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// Demo is the one handler exposed in this example.
type Demo struct{}

// Import simulates importing a file in three subtasks: validate, parse, write.
// The middleware logs each one's lifecycle.
func (h *Demo) Import(ctx context.Context, filename string) (string, error) {
	_, task := tasks.StartTask[any](ctx, "Import "+filename)
	defer task.Close()

	if err := tasks.SubTask(task.Context(), "Validate", func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		if filename == "" {
			return errors.New("empty filename")
		}
		return nil
	}); err != nil {
		task.Fail(err.Error())
		return "", err
	}

	if err := tasks.SubTask(task.Context(), "Parse", func(ctx context.Context) error {
		time.Sleep(80 * time.Millisecond)
		return nil
	}); err != nil {
		task.Fail(err.Error())
		return "", err
	}

	if err := tasks.SubTask(task.Context(), "Write to DB", func(ctx context.Context) error {
		time.Sleep(120 * time.Millisecond)
		return nil
	}); err != nil {
		task.Fail(err.Error())
		return "", err
	}

	return "imported " + filename, nil
}

// requestIDKey is a ctx key used to thread a per-task request_id through
// downstream slog calls.
type requestIDKey struct{}

// taskLoggingMiddleware logs every task lifecycle event with structured
// fields, and decorates ctx with task_id / task_title / parent_id so any
// log emitted inside the task body picks them up automatically.
func taskLoggingMiddleware(logger *slog.Logger) tasks.TaskMiddleware {
	return func(ctx context.Context, info tasks.TaskInfo, next func(context.Context) error) error {
		attrs := []any{
			"task_id", info.ID,
			"task_title", info.Title,
		}
		if info.ParentID != "" {
			attrs = append(attrs, "parent_id", info.ParentID)
		}
		ctx = context.WithValue(ctx, requestIDKey{}, info.ID)
		taskLogger := logger.With(attrs...)
		taskLogger.InfoContext(ctx, "task started")

		start := time.Now()
		err := next(ctx)
		dur := time.Since(start)

		if err != nil {
			taskLogger.ErrorContext(ctx, "task failed",
				"err", err, "duration_ms", dur.Milliseconds())
		} else {
			taskLogger.InfoContext(ctx, "task completed",
				"duration_ms", dur.Milliseconds())
		}
		return err
	}
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	registry := aprot.NewRegistry()
	registry.Register(&Demo{})
	tasks.Enable(registry, tasks.WithTaskMiddleware(taskLoggingMiddleware(logger)))

	server := aprot.NewServer(registry)
	mux := http.NewServeMux()
	mux.Handle("/ws", server)                   // WebSocket transport
	mux.Handle("/rpc", server.HTTPTransport())  // JSON-RPC over POST
	mux.Handle("/sse", server.HTTPTransport())  // SSE for push events
	mux.Handle("/sse/", server.HTTPTransport()) // SSE sub-routes

	addr := ":8080"
	logger.Info("server starting", "addr", "http://localhost"+addr)
	logger.Info("trigger the demo",
		"curl", `curl -sX POST http://localhost:8080/rpc -H 'Content-Type: application/json' -d '{"id":"1","method":"Demo.Import","params":["accounts.csv"]}'`)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
