package api

import "aprot"

// NewRegistry creates and configures the API registry with all handlers and push events.
func NewRegistry() *aprot.Registry {
	registry := aprot.NewRegistry()

	// Register handlers
	registry.Register(&Handlers{})

	// Register push events
	registry.RegisterPushEvent("UserCreated", UserCreatedEvent{})
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})
	registry.RegisterPushEvent("SystemNotification", SystemNotification{})

	return registry
}
