package api

import (
	"context"
	"log"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// NewRegistry creates and configures the API registry with all handlers and push events.
// Middleware is applied per-handler group for safety - you can't accidentally forget
// to protect an endpoint.
func NewRegistry(state *SharedState, authMiddleware aprot.Middleware) *aprot.Registry {
	registry := aprot.NewRegistry()

	// Register public handlers (no middleware)
	publicHandlers := NewPublicHandlers(state)
	registry.Register(publicHandlers)

	// Register streaming demo handlers (iter.Seq / iter.Seq2)
	registry.Register(NewStreamingHandlers())

	// Register enums
	registry.RegisterEnumFor(publicHandlers, TaskStatusValues())

	// Register protected handlers (with auth middleware)
	protectedHandlers := NewProtectedHandlers(state)
	registry.Register(protectedHandlers, authMiddleware)

	// Register push events
	registry.RegisterPushEventFor(publicHandlers, UserCreatedEvent{})
	registry.RegisterPushEventFor(publicHandlers, UserUpdatedEvent{})
	registry.RegisterPushEventFor(publicHandlers, SystemNotificationEvent{})
	registry.RegisterPushEventFor(protectedHandlers, DirectMessageEvent{})

	// Enable shared tasks with typed metadata
	// (registers TaskStateEvent, TaskUpdateEvent, CancelTask handler).
	// Task middleware logs every task's start and end with title — root
	// tasks and subtasks both fire. Swap log.Printf for slog / zap /
	// zerolog as needed; the middleware can also decorate ctx so
	// downstream handler logs pick up the title automatically.
	tasks.EnableWithMeta[TaskMeta](registry,
		tasks.WithTaskMiddleware(func(ctx context.Context, info tasks.TaskInfo, next func(context.Context) error) error {
			if info.ParentID == "" {
				log.Printf("[task] started id=%s title=%q", info.ID, info.Title)
			} else {
				log.Printf("[task] started id=%s title=%q parent=%s", info.ID, info.Title, info.ParentID)
			}
			err := next(ctx)
			if err != nil {
				log.Printf("[task] failed id=%s title=%q err=%v", info.ID, info.Title, err)
			} else {
				log.Printf("[task] completed id=%s title=%q", info.ID, info.Title)
			}
			return err
		}),
	)

	return registry
}
