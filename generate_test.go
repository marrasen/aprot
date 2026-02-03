package aprot

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Test types
type CreateUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age,omitempty"`
}

type CreateUserResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type GetUserRequest struct {
	ID string `json:"id"`
}

type GetUserResponse struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Email string   `json:"email"`
	Tags  []string `json:"tags,omitempty"`
}

type UserUpdatedEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Test handler
type TestHandlers struct{}

func (h *TestHandlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	return &CreateUserResponse{ID: "123", Name: req.Name}, nil
}

func (h *TestHandlers) GetUser(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return &GetUserResponse{ID: req.ID, Name: "Test User"}, nil
}

func TestRegistry(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(&TestHandlers{})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	methods := registry.Methods()
	if len(methods) != 2 {
		t.Errorf("Expected 2 methods, got %d", len(methods))
	}

	info, ok := registry.Get("CreateUser")
	if !ok {
		t.Fatal("CreateUser not found")
	}

	if info.RequestType.Name() != "CreateUserRequest" {
		t.Errorf("Expected CreateUserRequest, got %s", info.RequestType.Name())
	}

	if info.ResponseType.Name() != "CreateUserResponse" {
		t.Errorf("Expected CreateUserResponse, got %s", info.ResponseType.Name())
	}
}

func TestHandlerCall(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})

	info, _ := registry.Get("CreateUser")
	result, err := info.Call(context.Background(), []byte(`{"name":"John","email":"john@example.com"}`))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	resp, ok := result.(*CreateUserResponse)
	if !ok {
		t.Fatal("Result is not *CreateUserResponse")
	}

	if resp.Name != "John" {
		t.Errorf("Expected John, got %s", resp.Name)
	}
}

func TestGenerate(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})

	gen := NewGenerator(registry)
	gen.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	var buf bytes.Buffer
	err := gen.Generate(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Check for interfaces
	if !strings.Contains(output, "export interface CreateUserRequest") {
		t.Error("Missing CreateUserRequest interface")
	}
	if !strings.Contains(output, "export interface CreateUserResponse") {
		t.Error("Missing CreateUserResponse interface")
	}
	if !strings.Contains(output, "export interface UserUpdatedEvent") {
		t.Error("Missing UserUpdatedEvent interface")
	}

	// Check for client class
	if !strings.Contains(output, "export class ApiClient") {
		t.Error("Missing ApiClient class")
	}

	// Check for methods
	if !strings.Contains(output, "createUser(req: CreateUserRequest") {
		t.Error("Missing createUser method")
	}
	if !strings.Contains(output, "getUser(req: GetUserRequest") {
		t.Error("Missing getUser method")
	}

	// Check for push handler
	if !strings.Contains(output, "onUserUpdated(handler: PushHandler<UserUpdatedEvent>)") {
		t.Error("Missing onUserUpdated handler")
	}

	// Check for optional fields
	if !strings.Contains(output, "age?: number") {
		t.Error("Missing optional age field")
	}
	if !strings.Contains(output, "tags?: string[]") {
		t.Error("Missing optional tags field")
	}
}

func TestGenerateOutput(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})

	gen := NewGenerator(registry)
	gen.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	var buf bytes.Buffer
	gen.Generate(&buf)

	// Print the generated code for manual inspection
	t.Logf("Generated TypeScript:\n%s", buf.String())
}
