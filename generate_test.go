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

	// Check struct name is recorded
	if info.StructName != "TestHandlers" {
		t.Errorf("Expected StructName TestHandlers, got %s", info.StructName)
	}
}

func TestRegistryGroups(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	groups := registry.Groups()
	if len(groups) != 1 {
		t.Errorf("Expected 1 group, got %d", len(groups))
	}

	group, ok := groups["TestHandlers"]
	if !ok {
		t.Fatal("TestHandlers group not found")
	}

	if len(group.Handlers) != 2 {
		t.Errorf("Expected 2 handlers in group, got %d", len(group.Handlers))
	}

	if len(group.PushEvents) != 1 {
		t.Errorf("Expected 1 push event in group, got %d", len(group.PushEvents))
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
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
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

func TestGenerateMultipleFiles(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	gen := NewGenerator(registry)

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(files))
	}

	content, ok := files["test-handlers.ts"]
	if !ok {
		t.Fatalf("Expected test-handlers.ts, got files: %v", files)
	}

	if !strings.Contains(content, "export class ApiClient") {
		t.Error("Missing ApiClient in generated file")
	}
}

func TestGenerateReact(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputReact,
	})

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Check for React imports
	if !strings.Contains(output, "import { useState, useEffect") {
		t.Error("Missing React imports")
	}

	// Check for hooks
	if !strings.Contains(output, "export function useCreateUser") {
		t.Error("Missing useCreateUser hook")
	}
	if !strings.Contains(output, "export function useGetUser") {
		t.Error("Missing useGetUser hook")
	}

	// Check for mutation hooks
	if !strings.Contains(output, "export function useCreateUserMutation") {
		t.Error("Missing useCreateUserMutation hook")
	}

	// Check for push event hooks
	if !strings.Contains(output, "export function useUserUpdated") {
		t.Error("Missing useUserUpdated hook")
	}

	// Check for context
	if !strings.Contains(output, "ApiClientProvider") {
		t.Error("Missing ApiClientProvider")
	}
	if !strings.Contains(output, "useApiClient") {
		t.Error("Missing useApiClient hook")
	}
}

func TestGenerateOutput(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEvent("UserUpdated", UserUpdatedEvent{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	gen.GenerateTo(&buf)

	// Print the generated code for manual inspection
	t.Logf("Generated TypeScript:\n%s", buf.String())
}
