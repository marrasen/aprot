package api

import "github.com/marrasen/aprot"

// NewRegistry creates and configures the API registry with all handlers and push events.
// The handlers parameter is used to set up method options.
func NewRegistry(handlers *Handlers) *aprot.Registry {
	registry := aprot.NewRegistry()

	// Register handlers with per-method options
	registry.RegisterWithOptions(handlers, map[string][]aprot.Option{
		// Public methods (no auth required)
		"CreateUser":       {},
		"GetUser":          {},
		"ListUsers":        {},
		"ProcessBatch":     {},
		"SendNotification": {},
		"Login":            {},

		// Protected methods (require authentication)
		"GetProfile":  {aprot.WithAuth()},
		"SendMessage": {aprot.WithAuth(), aprot.WithTags("messaging")},
	})

	// Register push events
	registry.RegisterPushEvent("UserCreated", UserCreatedEvent{})
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})
	registry.RegisterPushEvent("SystemNotification", SystemNotification{})
	registry.RegisterPushEvent("DirectMessage", DirectMessage{})

	return registry
}
