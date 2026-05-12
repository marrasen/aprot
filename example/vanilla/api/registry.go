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
	// Lifecycle hooks log every task's start and end with title — root tasks
	// and subtasks both fire. Swap log.Printf for slog / zap / zerolog as
	// needed; the start hook can also decorate ctx so downstream handler
	// logs pick up the title automatically.
	tasks.EnableWithMeta[TaskMeta](registry,
		tasks.WithTaskStartHook(func(ctx context.Context, id, title, parentID string) context.Context {
			if parentID == "" {
				log.Printf("[task] started id=%s title=%q", id, title)
			} else {
				log.Printf("[task] started id=%s title=%q parent=%s", id, title, parentID)
			}
			return ctx
		}),
		tasks.WithTaskEndHook(func(ctx context.Context, id, title, parentID string, err error) {
			if err != nil {
				log.Printf("[task] failed id=%s title=%q err=%v", id, title, err)
				return
			}
			log.Printf("[task] completed id=%s title=%q", id, title)
		}),
	)

	return registry
}
