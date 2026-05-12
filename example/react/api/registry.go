package api

import (
	"context"
	"log"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// NewRegistry creates and configures the API registry with all handlers and push events.
func NewRegistry() (*aprot.Registry, *Handlers, *Todos) {
	registry := aprot.NewRegistry()

	// Enable struct validation (go-playground/validator)
	registry.SetValidator(aprot.NewPlaygroundValidator())

	// Register handlers
	handlers := NewHandlers()
	registry.Register(handlers)

	// Register streaming demo (iter.Seq)
	registry.Register(NewStreamingHandlers())

	// Register todo handlers (exposed via both WebSocket and REST)
	todos := NewTodos()
	registry.RegisterREST(todos)

	// Register enums
	registry.RegisterEnumFor(handlers, TaskStatusValues())

	// Register push events
	registry.RegisterPushEventFor(handlers, UserCreatedEvent{})
	registry.RegisterPushEventFor(handlers, UserUpdatedEvent{})
	registry.RegisterPushEventFor(handlers, SystemNotificationEvent{})

	// Enable shared tasks with typed metadata.
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

	return registry, handlers, todos
}
