package api

import "github.com/marrasen/aprot"

// NewRegistry creates and configures the API registry with all handlers and push events.
func NewRegistry() *aprot.Registry {
	registry := aprot.NewRegistry()

	// Register handlers
	registry.Register(&Handlers{})

	// Register push events
	registry.RegisterPushEvent(UserCreatedEvent{})
	registry.RegisterPushEvent(UserUpdatedEvent{})
	registry.RegisterPushEvent(SystemNotificationEvent{})

	return registry
}
