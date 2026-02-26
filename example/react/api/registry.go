package api

import (
	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/tasks"
)

// NewRegistry creates and configures the API registry with all handlers and push events.
func NewRegistry() (*aprot.Registry, *Handlers) {
	registry := aprot.NewRegistry()

	// Register handlers
	handlers := NewHandlers()
	registry.Register(handlers)

	// Register enums
	registry.RegisterEnumFor(handlers, TaskStatusValues())

	// Register push events
	registry.RegisterPushEventFor(handlers, UserCreatedEvent{})
	registry.RegisterPushEventFor(handlers, UserUpdatedEvent{})
	registry.RegisterPushEventFor(handlers, SystemNotificationEvent{})

	// Enable shared tasks with typed metadata
	tasks.EnableWithMeta[TaskMeta](registry)

	return registry, handlers
}
