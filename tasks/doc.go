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
// This registers the CancelTask handler, push event types, and the
// middleware that wires task delivery into every request context.
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
// To let a shared task outlive the client connection, use
// [context.WithoutCancel]:
//
//	ctx, task := tasks.StartTask[MyMeta](
//	    context.WithoutCancel(ctx), "Background job", tasks.Shared(),
//	)
//
// # SharedSubTask bridge
//
// [SharedSubTask] creates a shared task that also routes nested [SubTask],
// [Output], and [TaskProgress] calls through the shared delivery system.
// If no connection or task manager is available, it falls back to a
// regular [SubTask]:
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
// # Cancellation
//
// Clients can cancel shared tasks via the generated CancelTask handler.
// On the server side, use [CancelSharedTask] directly.
package tasks
