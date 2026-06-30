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
// To drive it by hand instead, the HTTP transport needs a connection ID
// from the SSE stream first (see README.md).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

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
	// HTTPTransport() returns a fresh handler with its own connection table,
	// so the GET /sse stream and the POST /rpc that references its connection
	// ID must share one instance — otherwise /rpc reports "unknown connection
	// ID". Reuse a single handler across all HTTP routes.
	httpTransport := server.HTTPTransport()
	mux := http.NewServeMux()
	mux.Handle("/ws", server)            // WebSocket transport
	mux.Handle("/sse", httpTransport)    // GET: open the SSE event stream
	mux.Handle("/rpc", httpTransport)    // POST: JSON-RPC request
	mux.Handle("/cancel", httpTransport) // POST: cancel an in-flight request

	const addr = "127.0.0.1:8080"
	httpServer := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	defer httpServer.Close() //nolint:errcheck

	base := "http://" + addr
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

// driveDemo exercises the server end to end: it opens an SSE stream to obtain
// a connection ID, POSTs an RPC referencing that ID, and waits for the
// handler to run so its middleware log lines flush. This client plumbing is
// only here to make `go run .` self-contained — it is not part of the
// middleware lesson.
func driveDemo(logger *slog.Logger, base, filename string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error("sse connect failed", "err", err)
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	connID := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var msg struct {
				Type         string `json:"type"`
				ConnectionID string `json:"connectionId"`
			}
			if json.Unmarshal([]byte(data), &msg) == nil && msg.Type == "connected" {
				select {
				case connID <- msg.ConnectionID:
				default:
				}
			}
		}
	}()

	var id string
	select {
	case id = <-connID:
	case <-ctx.Done():
		logger.Error("never received connection id from SSE stream")
		return
	}

	body := fmt.Sprintf(`{"connectionId":%q,"id":"1","method":"Demo.Import","params":[%q]}`, id, filename)
	post, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/rpc", strings.NewReader(body))
	post.Header.Set("Content-Type", "application/json")
	pr, err := http.DefaultClient.Do(post)
	if err != nil {
		logger.Error("rpc post failed", "err", err)
		return
	}
	_ = pr.Body.Close()

	// The RPC is dispatched asynchronously; give the handler time to run all
	// three subtasks (~250ms) so the middleware log lines flush before we
	// move on to the next demo.
	time.Sleep(500 * time.Millisecond)
}
