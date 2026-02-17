package api

import "github.com/marrasen/aprot"

// NewRegistry creates and configures the API registry with all handlers and push events.
// Middleware is applied per-handler group for safety - you can't accidentally forget
// to protect an endpoint.
func NewRegistry(state *SharedState, authMiddleware aprot.Middleware) *aprot.Registry {
	registry := aprot.NewRegistry()

	// Register enums
	registry.RegisterEnum(TaskStatusValues())

	// Register public handlers (no middleware)
	publicHandlers := NewPublicHandlers(state)
	registry.Register(publicHandlers)

	// Register protected handlers (with auth middleware)
	protectedHandlers := NewProtectedHandlers(state)
	registry.Register(protectedHandlers, authMiddleware)

	// Register push events
	registry.RegisterPushEventFor(publicHandlers, UserCreatedEvent{})
	registry.RegisterPushEventFor(publicHandlers, UserUpdatedEvent{})
	registry.RegisterPushEventFor(publicHandlers, SystemNotificationEvent{})
	registry.RegisterPushEventFor(protectedHandlers, DirectMessageEvent{})

	// Enable shared tasks with typed metadata
	// (registers TaskStateEvent, TaskOutputEvent, CancelTask handler)
	aprot.EnableTasksWithMeta[TaskMeta](registry)

	return registry
}
