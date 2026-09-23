// Package main is a self-contained aprot server demonstrating
// tasks.WithTaskMiddleware.
//
// Run it:
//
//	go run .
//
// The program starts an aprot server, then drives one successful and one
// failing request against itself so the demo works with a single command.
// Watch stderr: every task and subtask reports started -> completed (or
// failed) through the middleware, with task_id / task_title / parent_id
// attached to ctx so any log emitted inside the task body picks them up.
//
// To drive it by hand instead, connect a WebSocket client to /ws and send one
// request frame (see README.md).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// Demo is the one handler exposed in this example.
type Demo struct{}

// Import simulates importing a file in three subtasks: validate, parse, write.
// The middleware logs each one's lifecycle.
//
// Note: the subtasks are created from taskCtx — the context StartTask
// *returns* — not from task.Context(). For a request-scoped task,
// task.Context() falls back to context.Background(), which carries no task
// delivery, so subtasks created from it are silently untracked.
func (h *Demo) Import(ctx context.Context, filename string) (string, error) {
	taskCtx, task := tasks.StartTask[any](ctx, "Import "+filename)
	defer task.Close()

	if err := tasks.SubTask(taskCtx, "Validate", func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		if filename == "" {
			return errors.New("empty filename")
		}
		return nil
	}); err != nil {
		task.Fail(err.Error())
		return "", err
	}

	if err := tasks.SubTask(taskCtx, "Parse", func(ctx context.Context) error {
		time.Sleep(80 * time.Millisecond)
		return nil
	}); err != nil {
		task.Fail(err.Error())
		return "", err
	}

	if err := tasks.SubTask(taskCtx, "Write to DB", func(ctx context.Context) error {
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
	mux.Handle("/ws", server) // WebSocket transport

	const addr = "127.0.0.1:8080"
	httpServer := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	defer httpServer.Close() //nolint:errcheck

	base := "ws://" + addr + "/ws"
	if err := waitForServer(addr); err != nil {
		log.Fatal(err)
	}
	logger.Info("server started; driving demo requests against it", "addr", base)

	// Happy path: all three subtasks complete.
	driveDemo(logger, base, "accounts.csv")
	// Failure path: the Validate subtask returns an error, which cascades.
	driveDemo(logger, base, "")

	logger.Info("demo complete")
}

// waitForServer blocks until the server is accepting connections (or times out).
func waitForServer(addr string) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("server did not start in time")
}

// driveDemo exercises the server end to end: it opens a WebSocket connection,
// sends one request frame, and waits for the response so the handler's
// middleware log lines have flushed before the next demo runs. This client
// plumbing is only here to make `go run .` self-contained — it is not part of
// the middleware lesson.
func driveDemo(logger *slog.Logger, wsURL, filename string) {
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		logger.Error("websocket connect failed", "err", err)
		return
	}
	defer ws.Close() //nolint:errcheck

	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	// The server sends a config frame on connect; discard it.
	if _, _, err := ws.ReadMessage(); err != nil {
		logger.Error("read config frame failed", "err", err)
		return
	}

	req := fmt.Sprintf(`{"type":"request","id":"1","method":"Demo.Import","params":[%q]}`, filename)
	if err := ws.WriteMessage(websocket.TextMessage, []byte(req)); err != nil {
		logger.Error("send request failed", "err", err)
		return
	}

	// Read until the request settles. Progress and task frames arrive first;
	// waiting for the response (or error) is what keeps the demos ordered.
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			logger.Error("read frame failed", "err", err)
			return
		}
		var frame struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(data, &frame) != nil || frame.ID != "1" {
			continue
		}
		if frame.Type == "response" || frame.Type == "error" {
			return
		}
	}
}
