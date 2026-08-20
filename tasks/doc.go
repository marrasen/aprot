// Package tasks provides hierarchical task trees with progress tracking,
// output streaming, and both request-scoped and shared (broadcast) task
// systems for the [github.com/marrasen/aprot] framework.
//
// # Overview
//
// Tasks let a server report structured, real-time progress to connected
// clients while a handler executes long-running work. A task is a tree of
// nodes, each with a title, status (Created → Running → Completed/Failed),
// optional numeric progress, typed metadata, and streamed text output.
//
// There are two flavours of task:
//
//   - Request-scoped tasks are tied to a single RPC call and visible only to
//     the client that made the request.
//   - Shared tasks are broadcast to every connected client and survive after
//     the originating handler returns.
//
// # Enabling the task system
//
// Register the task system during server setup:
//
//	tasks.Enable(registry)                   // no typed metadata
//	tasks.EnableWithMeta[MyMeta](registry)   // with typed metadata
//
// This registers the CancelTask and ListTasks handlers, push event types,
// and the middleware that wires task delivery into every request context.
//
// With [EnableWithMeta], the generated client types the meta field on
// TaskNode and SharedTaskState as MyMeta's TypeScript interface (declared
// alongside the task types, and re-exported from tasks.ts) instead of
// `unknown`, so client code can read task.meta.myField without casting.
// With plain [Enable] the meta fields are typed `unknown`. The choice is
// codegen-only — the wire format is identical either way.
//
// # Task middleware
//
// Pass [WithTaskMiddleware] to wrap every task with custom logic — logging,
// tracing, ctx decoration. The middleware is a single function that brackets
// the task body around a next() call, mirroring [aprot.Middleware]:
//
//	tasks.Enable(registry, tasks.WithTaskMiddleware(
//	    func(ctx context.Context, info tasks.TaskInfo, next func(context.Context) error) error {
//	        ctx = ctxlog.With(ctx, slog.With("task_title", info.Title))
//	        slog.InfoContext(ctx, "task started")
//	        err := next(ctx)
//	        if err != nil {
//	            slog.ErrorContext(ctx, "task failed", "err", err)
//	        } else {
//	            slog.InfoContext(ctx, "task completed")
//	        }
//	        return err
//	    },
//	))
//
// The ctx passed to next propagates to the task body and to any nested
// subtasks, so logger / span decorations chain through the task tree
// without manual wiring. Cancellation surfaces as a non-nil err with
// message "canceled". For scope-based tasks ([SubTask], [SharedSubTask])
// the middleware runs synchronously; for manual-lifecycle tasks
// ([StartTask], [OutputWriter], [WriterProgress], [Task.SubTask],
// [TaskSub.SubTask]) the middleware runs in a goroutine and next blocks
// until Close or Fail is called on the returned handle.
//
// # Request-scoped tasks
//
// Create a task inside a handler. It auto-completes when the handler
// returns nil, or auto-fails when the handler returns an error:
//
//	func (h *Handler) Import(ctx context.Context, req ImportReq) (*tasks.TaskRef, error) {
//	    ctx, task := tasks.StartTask[MyMeta](ctx, "Importing data")
//	    task.SetMeta(MyMeta{Filename: req.File})
//
//	    // Nest child work with SubTask:
//	    err := tasks.SubTask(ctx, "Validating", func(ctx context.Context) error {
//	        tasks.TaskProgress(ctx, 0, 100)
//	        // ...
//	        tasks.TaskProgress(ctx, 100, 100)
//	        return nil
//	    })
//	    return nil, err  // task auto-completes / auto-fails
//	}
//
// The client receives RequestTaskTreeEvent, RequestTaskOutputEvent, and
// RequestTaskProgressEvent push events scoped to its request.
//
// # Shared (broadcast) tasks
//
// Pass the [Shared] option to make the task visible to all clients:
//
//	ctx, task := tasks.StartTask[MyMeta](ctx, "Building index", tasks.Shared())
//
// All clients receive [TaskStateEvent] and [TaskUpdateEvent] push events.
// Each client sees an IsOwner flag indicating whether it started the task.
//
// A shared task started on the request context is auto-completed (or
// auto-failed) when the handler returns, like a request-scoped task.
//
// To let a shared task outlive the handler and the client connection —
// the fire-and-forget pattern — start it on a detached context with
// [context.WithoutCancel]:
//
//	ctx, task := tasks.StartTask[MyMeta](
//	    context.WithoutCancel(ctx), "Background job", tasks.Shared(),
//	)
//	go func() {
//	    defer task.Err(doWork(ctx)) // completes or fails the task
//	}()
//	return &tasks.TaskRef{TaskID: task.ID()}, nil
//
// A detached task is not auto-finalized when the handler returns: the
// background goroutine owns its lifecycle and must call [Task.Close],
// [Task.Fail], or [Task.Err] when done.
//
// # SharedSubTask bridge
//
// [SharedSubTask] creates a shared task that also routes nested [SubTask],
// [Output], and [TaskProgress] calls through the shared delivery system.
// If the task system is not enabled (no task manager available), it falls
// back to a regular [SubTask]:
//
//	err := tasks.SharedSubTask(ctx, "Sync accounts", func(ctx context.Context) error {
//	    return tasks.SubTask(ctx, "Fetching", func(ctx context.Context) error {
//	        tasks.Output(ctx, "fetched 42 records")
//	        return nil
//	    })
//	})
//
// # Progress reporting
//
// [TaskProgress] sets absolute progress on the current task node.
// [StepTaskProgress] increments the current value by a delta, which is
// convenient inside loops:
//
//	for i, item := range items {
//	    process(item)
//	    tasks.StepTaskProgress(ctx, 1)
//	}
//
// # Output streaming
//
// [Output] sends a text message attached to the nearest task node.
// [OutputWriter] and [WriterProgress] return an [io.WriteCloser] that
// creates a child task node, sending each Write as output or tracking
// bytes written as progress:
//
//	w := tasks.OutputWriter(ctx, "Build log")
//	cmd.Stdout = w
//	cmd.Run()
//	w.Close()
//
// # Task and TaskSub types
//
// [StartTask] returns a *[Task] handle for the root node. Use it to set
// metadata, report progress, stream output, create sub-tasks, or
// manually close/fail the task. [Task.SubTask] returns a *[TaskSub]
// handle for child nodes with the same capabilities.
//
// # TypeScript client
//
// The generated TypeScript client provides hooks and helpers:
//
//   - React: useSharedTasks, useMyTasks, useSharedTask(id),
//     useTaskOutput(taskId), cancelSharedTask(client, taskId)
//   - Vanilla: TaskStateEvent, TaskUpdateEvent push event types
//   - Request-scoped: onTaskProgress and onOutput callbacks in
//     RequestOptions
//
// TaskStateEvent is broadcast only at lifecycle boundaries, so a client that
// mounts while a task is already running would see nothing until the next
// one. useSharedTasks therefore seeds itself from the ListTasks RPC on mount
// and on every reconnect, and keeps its state in one store per client rather
// than per hook instance. The server likewise pushes a TaskStateEvent on
// connect even when no tasks exist, so a client whose task finished while it
// was disconnected clears its stale state.
//
// # Cancellation
//
// Clients can cancel shared tasks via the generated CancelTask handler.
// On the server side, use [CancelSharedTask] directly. By default
// cancellation is owner-scoped: the task's owner may cancel it, and any other
// caller is refused with CodeForbidden. (Internal callers that already hold
// the task can still cancel it directly via the task handle.)
//
// Ownership is matched on the caller's address ([aprot.UserID]) when the
// owner authenticated, and on the creating connection when it did not. Two
// consequences: cancel rights survive a reconnect, and a caller on a
// request-scoped transport can cancel the task it started, having no
// connection of its own. This is the same rule clients see as
// SharedTaskState.IsOwner, so a rendered cancel button is never refused.
//
// Pass [WithCancelAuthorizer] to install any other policy from a
// [TaskCancelInfo] carrying the owning connection and address. The authorizer
// runs on every transport, so read the caller's identity from the
// request-scoped principal rather than the connection, which is nil over REST
// and MCP:
//
//	tasks.Enable(registry, tasks.WithCancelAuthorizer(
//	    func(ctx context.Context, t tasks.TaskCancelInfo) error {
//	        user, ok := aprot.PrincipalFrom(ctx).(*myauth.User)
//	        if !ok || !user.IsAdmin {
//	            return aprot.ErrForbidden("not permitted")
//	        }
//	        return nil
//	    },
//	))
//
// # Transports without a client channel
//
// The task manager belongs to the [aprot.Server], not to a connection, so a
// shared task started over a request-scoped transport (REST, MCP) registers
// with it and broadcasts to socket watchers exactly like one started over a
// socket. What a connection decides is *delivery*, not registration: with a
// connection, per-request task updates are pushed as usual; without one, no
// updates are pushed and nothing fakes a connection to pretend otherwise.
//
// The practical shape on those transports: the caller gets the task ID back
// in the response and follows the task through the ListTasks RPC, which
// answers on every transport and matches ownership on the caller's address.
// Socket clients watching the shared task list see the task and its progress
// live.
//
// Request-scoped tasks (without [Shared]) have nowhere to go on those
// transports, so they return a usable no-op [Task] rather than nil: handler
// code written for WebSocket (Progress, Output, SetMeta, Close, …) runs
// without panicking, and the state simply isn't delivered.
//
// One lifetime caveat is sharper here than on a socket. A shared task's
// context descends from the caller's, and a request-scoped request ends the
// moment its response is written — so a task meant to outlive the call must
// be started on a detached context, exactly as in the fire-and-forget pattern
// above:
//
//	ctx, task := tasks.StartTask[MyMeta](
//	    context.WithoutCancel(ctx), "Background job", tasks.Shared(),
//	)
//
// Without WithoutCancel the task is canceled as soon as the tool call or HTTP
// request returns.
package tasks
