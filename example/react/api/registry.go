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

	return registry, handlers, todos
}
