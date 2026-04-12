package api

import (
	"context"
	"fmt"
	"sync"

	"github.com/marrasen/aprot"
)

// Todos implements todo-related API methods using validated struct params.
// These handlers are exposed via both WebSocket and REST.
type Todos struct {
	todos  map[string]*Todo
	mu     sync.RWMutex
	nextID int
}

// NewTodos creates a new Todos handler instance.
func NewTodos() *Todos {
	return &Todos{
		todos:  make(map[string]*Todo),
		nextID: 1,
	}
}

// CreateTodo creates a new todo item.
// The CreateTodoRequest struct is validated automatically before this handler runs.
func (t *Todos) CreateTodo(ctx context.Context, req *CreateTodoRequest) (*Todo, error) {
	t.mu.Lock()
	id := fmt.Sprintf("todo_%d", t.nextID)
	t.nextID++
	todo := &Todo{
		ID:          id,
		Title:       req.Title,
		Description: req.Description,
		Priority:    req.Priority,
		Done:        false,
	}
	t.todos[id] = todo
	t.mu.Unlock()

	aprot.TriggerRefresh(ctx, "todos")
	return todo, nil
}

// ListTodos returns all todo items.
func (t *Todos) ListTodos(ctx context.Context) (*ListTodosResponse, error) {
	aprot.RegisterRefreshTrigger(ctx, "todos")

	t.mu.RLock()
	defer t.mu.RUnlock()

	todos := make([]Todo, 0, len(t.todos))
	for _, todo := range t.todos {
		todos = append(todos, *todo)
	}

	return &ListTodosResponse{Todos: todos, Total: len(todos)}, nil
}

// GetTodo retrieves a single todo by ID.
func (t *Todos) GetTodo(ctx context.Context, id string) (*Todo, error) {
	aprot.RegisterRefreshTrigger(ctx, "todos")

	t.mu.RLock()
	todo, ok := t.todos[id]
	t.mu.RUnlock()

	if !ok {
		return nil, aprot.NewError(404, "todo not found: "+id)
	}
	return todo, nil
}

// UpdateTodo updates an existing todo item.
func (t *Todos) UpdateTodo(ctx context.Context, id string, req *UpdateTodoRequest) (*Todo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	todo, ok := t.todos[id]
	if !ok {
		return nil, aprot.NewError(404, "todo not found: "+id)
	}

	if req.Title != "" {
		todo.Title = req.Title
	}
	if req.Description != "" {
		todo.Description = req.Description
	}
	if req.Priority != 0 {
		todo.Priority = req.Priority
	}
	if req.Done != nil {
		todo.Done = *req.Done
	}

	aprot.TriggerRefresh(ctx, "todos")
	return todo, nil
}

// DeleteTodo deletes a todo by ID.
func (t *Todos) DeleteTodo(ctx context.Context, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.todos[id]; !ok {
		return aprot.NewError(404, "todo not found: "+id)
	}
	delete(t.todos, id)

	aprot.TriggerRefresh(ctx, "todos")
	return nil
}
