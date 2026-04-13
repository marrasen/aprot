package api

import (
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

	// Enable shared tasks with typed metadata
	tasks.EnableWithMeta[TaskMeta](registry)

	return registry, handlers, todos
}
