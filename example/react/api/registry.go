package api

import "github.com/marrasen/aprot"

// NewRegistry creates and configures the API registry with all handlers and push events.
func NewRegistry() *aprot.Registry {
	registry := aprot.NewRegistry()

	// Register enums
	registry.RegisterEnum(TaskStatusValues())

	// Register handlers
	handlers := &Handlers{}
	registry.Register(handlers)

	// Register push events
	registry.RegisterPushEventFor(handlers, UserCreatedEvent{})
	registry.RegisterPushEventFor(handlers, UserUpdatedEvent{})
	registry.RegisterPushEventFor(handlers, SystemNotificationEvent{})

	return registry
}
