package aprot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
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

// String-based enum for testing
type TaskStatus string

const (
	TaskStatusCreated   TaskStatus = "created"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
)

func TaskStatusValues() []TaskStatus {
	return []TaskStatus{TaskStatusCreated, TaskStatusRunning, TaskStatusCompleted}
}

// Int-based enum for testing (with Stringer interface)
type Priority int

const (
	PriorityLow Priority = iota
	PriorityMedium
	PriorityHigh
)

func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "Low"
	case PriorityMedium:
		return "Medium"
	case PriorityHigh:
		return "High"
	default:
		return fmt.Sprintf("Priority(%d)", p)
	}
}

func PriorityValues() []Priority {
	return []Priority{PriorityLow, PriorityMedium, PriorityHigh}
}

// Request/Response using enums
type CreateTaskRequest struct {
	Name     string     `json:"name"`
	Status   TaskStatus `json:"status"`
	Priority Priority   `json:"priority"`
}

type CreateTaskResponse struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Status   TaskStatus `json:"status"`
	Priority Priority   `json:"priority"`
}

// Void handler test types
type DeleteItemRequest struct {
	ID string `json:"id"`
}

// Test handler
type TestHandlers struct{}

func (h *TestHandlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	return &CreateUserResponse{ID: "123", Name: req.Name}, nil
}

func (h *TestHandlers) GetUser(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return &GetUserResponse{ID: req.ID, Name: "Test User"}, nil
}

// Handler with enum types
type TaskHandlers struct{}

func (h *TaskHandlers) CreateTask(ctx context.Context, req *CreateTaskRequest) (*CreateTaskResponse, error) {
	return &CreateTaskResponse{ID: "task_1", Name: req.Name, Status: req.Status, Priority: req.Priority}, nil
}

func TestRegistry(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})

	methods := registry.Methods()
	if len(methods) != 2 {
		t.Errorf("Expected 2 methods, got %d", len(methods))
	}

	info, ok := registry.Get("TestHandlers.CreateUser")
	if !ok {
		t.Fatal("TestHandlers.CreateUser not found")
	}

	if len(info.Params) != 1 {
		t.Fatalf("Expected 1 param, got %d", len(info.Params))
	}
	if info.Params[0].Type.Elem().Name() != "CreateUserRequest" {
		t.Errorf("Expected *CreateUserRequest param, got %s", info.Params[0].Type)
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
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})

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

	info, _ := registry.Get("TestHandlers.CreateUser")
	result, err := info.Call(context.Background(), []byte(`[{"name":"John","email":"john@example.com"}]`))
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
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})

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

	// Check for standalone functions
	if !strings.Contains(output, "createUser(client: ApiClient, req: CreateUserRequest") {
		t.Error("Missing createUser function")
	}
	if !strings.Contains(output, "getUser(client: ApiClient, req: GetUserRequest") {
		t.Error("Missing getUser function")
	}

	// Check for push handler
	if !strings.Contains(output, "onUserUpdatedEvent(client: ApiClient, handler: PushHandler<UserUpdatedEvent>)") {
		t.Error("Missing onUserUpdatedEvent handler")
	}

	// Check for optional fields
	if !strings.Contains(output, "age?: number") {
		t.Error("Missing optional age field")
	}
	if !strings.Contains(output, "tags?: string[]") {
		t.Error("Missing optional tags field")
	}

	// Check for loading state API
	if !strings.Contains(output, "onLoadingChange(listener: (count: number) => void): () => void") {
		t.Error("Missing onLoadingChange method")
	}
	if !strings.Contains(output, "getLoadingCount(): number") {
		t.Error("Missing getLoadingCount method")
	}
	if !strings.Contains(output, "notifyLoadingChange()") {
		t.Error("Missing notifyLoadingChange calls")
	}
}

func TestGenerateMultipleFiles(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})

	gen := NewGenerator(registry)

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should generate 2 files: client.ts (base) and test-handlers.ts (handler)
	if len(files) != 2 {
		t.Errorf("Expected 2 files, got %d: %v", len(files), mapKeys(files))
	}

	// Check base client file
	baseContent, ok := files["client.ts"]
	if !ok {
		t.Fatalf("Expected client.ts, got files: %v", mapKeys(files))
	}
	if !strings.Contains(baseContent, "export class ApiClient") {
		t.Error("Missing ApiClient in client.ts")
	}
	if !strings.Contains(baseContent, "export const ErrorCode") {
		t.Error("Missing ErrorCode in client.ts")
	}
	if !strings.Contains(baseContent, "export class ApiError") {
		t.Error("Missing ApiError in client.ts")
	}

	// Check handler file
	handlerContent, ok := files["test-handlers.ts"]
	if !ok {
		t.Fatalf("Expected test-handlers.ts, got files: %v", mapKeys(files))
	}
	if !strings.Contains(handlerContent, "import { ApiClient") {
		t.Error("Missing import from client.ts in handler file")
	}
	if !strings.Contains(handlerContent, "export interface CreateUserRequest") {
		t.Error("Missing CreateUserRequest in handler file")
	}
	if !strings.Contains(handlerContent, "export function createUser(client: ApiClient") {
		t.Error("Missing createUser standalone function in handler file")
	}

	// Check loading state API in base client
	if !strings.Contains(baseContent, "onLoadingChange(listener: (count: number) => void): () => void") {
		t.Error("Missing onLoadingChange in multi-file client.ts")
	}
	if !strings.Contains(baseContent, "getLoadingCount(): number") {
		t.Error("Missing getLoadingCount in multi-file client.ts")
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestGenerateReact(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})

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
	if !strings.Contains(output, "import { useState, useEffect, useCallback, useMemo, useRef") {
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
	if !strings.Contains(output, "export function useUserUpdatedEvent") {
		t.Error("Missing useUserUpdatedEvent hook")
	}

	// Check for context
	if !strings.Contains(output, "ApiClientProvider") {
		t.Error("Missing ApiClientProvider")
	}
	if !strings.Contains(output, "useApiClient") {
		t.Error("Missing useApiClient hook")
	}

	// Check for generic hooks
	if !strings.Contains(output, "export function useQuery") {
		t.Error("Missing generic useQuery hook")
	}
	if !strings.Contains(output, "export function useMutation") {
		t.Error("Missing generic useMutation hook")
	}
	if !strings.Contains(output, "export function usePushEvent") {
		t.Error("Missing generic usePushEvent hook")
	}

	// Per-handler hooks should delegate to generic hooks (no useState in wrappers)
	// Split at the generic hooks boundary and check the per-handler section
	parts := strings.SplitN(output, "export function usePushEvent", 2)
	if len(parts) == 2 {
		handlerHooksSection := parts[1]
		// After usePushEvent definition ends, the per-handler wrappers should not use useState
		afterGenericHooks := strings.SplitN(handlerHooksSection, "}\n", 2)
		if len(afterGenericHooks) == 2 && strings.Contains(afterGenericHooks[1], "useState") {
			t.Error("Per-handler hooks should delegate to generic hooks, not use useState directly")
		}
	}

	// Mutation hooks should forward AbortSignal for cancellation
	if !strings.Contains(output, "signal: AbortSignal") {
		t.Error("Mutation hooks should forward AbortSignal for cancellation")
	}

	// useQuery should use AbortController to cancel in-flight requests
	useQuerySection := strings.SplitN(output, "export function useQuery", 2)
	if len(useQuerySection) == 2 {
		useQueryEnd := strings.SplitN(useQuerySection[1], "export function useMutation", 2)
		if len(useQueryEnd) == 2 && !strings.Contains(useQueryEnd[0], "AbortController") {
			t.Error("useQuery should use AbortController for request cancellation")
		}
	}

	// Per-handler query hooks should also forward signal (not just mutation hooks)
	if len(parts) == 2 {
		handlerHooksSection := parts[1]
		// Count occurrences of signal: AbortSignal — should appear in both query and mutation hooks
		signalCount := strings.Count(handlerHooksSection, "signal: AbortSignal")
		// We expect at least 2 per method (one for query hook, one for mutation hook)
		// With 2 methods (GetUser, CreateUser), that's at least 4
		if signalCount < 4 {
			t.Errorf("Expected signal: AbortSignal in both query and mutation per-handler hooks, got %d occurrences", signalCount)
		}
	}

	// Check for loading state hook
	if !strings.Contains(output, "export function useIsLoading(): boolean") {
		t.Error("Missing useIsLoading hook")
	}
	if !strings.Contains(output, "onLoadingChange") {
		t.Error("Missing onLoadingChange in React output")
	}
	if !strings.Contains(output, "getLoadingCount") {
		t.Error("Missing getLoadingCount in React output")
	}
}

func TestGenerateReactMultiFileGenericHooks(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputReact,
	})
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Base client file should contain generic hooks
	clientContent, ok := files["client.ts"]
	if !ok {
		t.Fatal("Missing client.ts in multi-file output")
	}
	if !strings.Contains(clientContent, "export function useQuery") {
		t.Error("client.ts missing generic useQuery hook")
	}
	if !strings.Contains(clientContent, "export function useMutation") {
		t.Error("client.ts missing generic useMutation hook")
	}
	if !strings.Contains(clientContent, "export function usePushEvent") {
		t.Error("client.ts missing generic usePushEvent hook")
	}

	// Handler file should NOT contain useState (proves delegation)
	handlerContent, ok := files["test-handlers.ts"]
	if !ok {
		t.Fatalf("Missing test-handlers.ts in multi-file output, got files: %v", func() []string {
			keys := make([]string, 0, len(files))
			for k := range files {
				keys = append(keys, k)
			}
			return keys
		}())
	}
	if strings.Contains(handlerContent, "useState") {
		t.Error("Handler file should not contain useState — hooks should delegate to generic hooks")
	}
	if !strings.Contains(handlerContent, "useQuery(") {
		t.Error("Handler file should delegate to useQuery")
	}
	if !strings.Contains(handlerContent, "useMutation(") {
		t.Error("Handler file should delegate to useMutation")
	}
	if !strings.Contains(handlerContent, "usePushEvent(") {
		t.Error("Handler file should delegate to usePushEvent")
	}

	// Mutation hooks should forward AbortSignal for cancellation
	if !strings.Contains(handlerContent, "signal: AbortSignal") {
		t.Error("Mutation hooks should forward AbortSignal for cancellation")
	}

	// useQuery in client.ts should use AbortController to cancel in-flight requests
	useQuerySection := strings.SplitN(clientContent, "export function useQuery", 2)
	if len(useQuerySection) == 2 {
		useQueryEnd := strings.SplitN(useQuerySection[1], "export function useMutation", 2)
		if len(useQueryEnd) == 2 && !strings.Contains(useQueryEnd[0], "AbortController") {
			t.Error("useQuery should use AbortController for request cancellation")
		}
	}

	// Per-handler query hooks should also forward signal (not just mutation hooks)
	signalCount := strings.Count(handlerContent, "signal: AbortSignal")
	// Each method should have signal in both query and mutation hooks
	if signalCount < 4 {
		t.Errorf("Expected signal: AbortSignal in both query and mutation per-handler hooks, got %d occurrences", signalCount)
	}

	// Check loading state hook in multi-file React client.ts
	if !strings.Contains(clientContent, "export function useIsLoading(): boolean") {
		t.Error("client.ts missing useIsLoading hook")
	}
	if !strings.Contains(clientContent, "onLoadingChange") {
		t.Error("client.ts missing onLoadingChange method")
	}
	if !strings.Contains(clientContent, "getLoadingCount") {
		t.Error("client.ts missing getLoadingCount method")
	}
}

func TestGenerateOutput(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	gen.GenerateTo(&buf)

	// Print the generated code for manual inspection
	t.Logf("Generated TypeScript:\n%s", buf.String())
}

func TestRegisterEnum(t *testing.T) {
	registry := NewRegistry()
	handler := &TaskHandlers{}
	registry.Register(handler)

	// Test string-based enum
	registry.RegisterEnumFor(handler, TaskStatusValues())

	// Test int-based enum with Stringer
	registry.RegisterEnumFor(handler, PriorityValues())

	enums := registry.Enums()
	if len(enums) != 2 {
		t.Errorf("Expected 2 enums, got %d", len(enums))
	}

	// Verify enum info
	for _, e := range enums {
		if e.Name == "TaskStatus" {
			if !e.IsString {
				t.Error("TaskStatus should be IsString=true")
			}
			if len(e.Values) != 3 {
				t.Errorf("TaskStatus should have 3 values, got %d", len(e.Values))
			}
			// Check value names are capitalized
			for _, v := range e.Values {
				if v.Name == "" {
					t.Error("Enum value name should not be empty")
				}
				// First char should be uppercase
				if v.Name[0] < 'A' || v.Name[0] > 'Z' {
					t.Errorf("Enum value name should be capitalized, got %s", v.Name)
				}
			}
		}
		if e.Name == "Priority" {
			if e.IsString {
				t.Error("Priority should be IsString=false")
			}
			if len(e.Values) != 3 {
				t.Errorf("Priority should have 3 values, got %d", len(e.Values))
			}
			// Check int values use Stringer names
			expectedNames := []string{"Low", "Medium", "High"}
			for i, v := range e.Values {
				if v.Name != expectedNames[i] {
					t.Errorf("Expected name %s, got %s", expectedNames[i], v.Name)
				}
			}
		}
	}
}

func TestRegisterEnumErrors(t *testing.T) {
	registry := NewRegistry()
	handler := &TaskHandlers{}
	registry.Register(handler)

	// Test non-slice panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for non-slice")
			}
		}()
		registry.RegisterEnumFor(handler, "not a slice")
	}()

	// Test empty slice panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for empty slice")
			}
		}()
		registry.RegisterEnumFor(handler, []TaskStatus{})
	}()

	// Test unregistered handler panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for unregistered handler")
			}
		}()
		registry.RegisterEnumFor(&TestHandlers{}, TaskStatusValues())
	}()
}

func TestGenerateWithEnums(t *testing.T) {
	registry := NewRegistry()
	handler := &TaskHandlers{}
	registry.Register(handler)
	registry.RegisterEnumFor(handler, TaskStatusValues())
	registry.RegisterEnumFor(handler, PriorityValues())

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Check for string enum const object
	if !strings.Contains(output, "export const TaskStatus = {") {
		t.Error("Missing TaskStatus enum const")
	}
	if !strings.Contains(output, `Created: "created"`) {
		t.Error("Missing Created enum value")
	}
	if !strings.Contains(output, "export type TaskStatusType = typeof TaskStatus[keyof typeof TaskStatus]") {
		t.Error("Missing TaskStatusType type alias")
	}

	// Check for int enum const object
	if !strings.Contains(output, "export const Priority = {") {
		t.Error("Missing Priority enum const")
	}
	if !strings.Contains(output, "Low: 0") {
		t.Error("Missing Low enum value")
	}
	if !strings.Contains(output, "export type PriorityType = typeof Priority[keyof typeof Priority]") {
		t.Error("Missing PriorityType type alias")
	}

	// Check that struct fields use enum types
	if !strings.Contains(output, "status: TaskStatusType") {
		t.Error("Field should use TaskStatusType instead of string")
	}
	if !strings.Contains(output, "priority: PriorityType") {
		t.Error("Field should use PriorityType instead of number")
	}

	// Print for inspection
	t.Logf("Generated TypeScript with enums:\n%s", output)
}

// Void handlers (error-only return)
type VoidHandlers struct{}

func (h *VoidHandlers) DeleteItem(ctx context.Context, req *DeleteItemRequest) error {
	return nil
}

// Mixed handlers (void and non-void in same group)
type MixedHandlers struct{}

func (h *MixedHandlers) GetUser(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return &GetUserResponse{ID: req.ID, Name: "Test"}, nil
}

func (h *MixedHandlers) DeleteItem(ctx context.Context, req *DeleteItemRequest) error {
	return nil
}

func TestVoidHandlerRegistration(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&VoidHandlers{})

	info, ok := registry.Get("VoidHandlers.DeleteItem")
	if !ok {
		t.Fatal("DeleteItem not found")
	}

	if !info.IsVoid {
		t.Error("Expected IsVoid to be true")
	}
	if info.ResponseType != voidResponseType {
		t.Errorf("Expected voidResponseType, got %s", info.ResponseType.Name())
	}
}

func TestVoidHandlerCall(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&VoidHandlers{})

	info, _ := registry.Get("VoidHandlers.DeleteItem")
	result, err := info.Call(context.Background(), []byte(`[{"id":"item_1"}]`))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result != nil {
		t.Errorf("Expected nil result, got %v", result)
	}
}

func TestVoidHandlerCallError(t *testing.T) {
	registry := NewRegistry()

	// Can't add method to a test-local type, use VoidHandlers and test via the existing handler
	registry.Register(&VoidHandlers{})

	info, _ := registry.Get("VoidHandlers.DeleteItem")
	// Call with valid params — should succeed
	result, err := info.Call(context.Background(), []byte(`[{"id":"ok"}]`))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result != nil {
		t.Errorf("Expected nil result, got %v", result)
	}
}

func TestGenerateVoidResponse(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&VoidHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Should NOT generate a voidResponse interface
	if strings.Contains(output, "export interface voidResponse") {
		t.Error("Should not generate voidResponse interface")
	}

	// Should use Promise<void>
	if !strings.Contains(output, "Promise<void>") {
		t.Error("Should use Promise<void>")
	}

	// Should still generate request type
	if !strings.Contains(output, "export interface DeleteItemRequest") {
		t.Error("Missing DeleteItemRequest interface")
	}

	t.Logf("Generated TypeScript (void):\n%s", output)
}

func TestGenerateVoidResponseMultipleFiles(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&VoidHandlers{})

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	handlerContent, ok := files["void-handlers.ts"]
	if !ok {
		t.Fatalf("Expected void-handlers.ts, got files: %v", mapKeys(files))
	}

	if strings.Contains(handlerContent, "export interface voidResponse") {
		t.Error("Should not generate voidResponse interface")
	}
	if !strings.Contains(handlerContent, "Promise<void>") {
		t.Error("Should use Promise<void>")
	}
}

func TestGenerateVoidResponseReact(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&VoidHandlers{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputReact,
	})

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	if strings.Contains(output, "export interface voidResponse") {
		t.Error("Should not generate voidResponse interface")
	}
	if !strings.Contains(output, "Promise<void>") {
		t.Error("Should use Promise<void>")
	}
	if !strings.Contains(output, "useDeleteItem") {
		t.Error("Missing useDeleteItem hook")
	}
	if !strings.Contains(output, "useDeleteItemMutation") {
		t.Error("Missing useDeleteItemMutation hook")
	}

	t.Logf("Generated TypeScript (void, React):\n%s", output)
}

func TestGenerateMixedVoidAndNonVoid(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&MixedHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Should generate GetUserResponse interface
	if !strings.Contains(output, "export interface GetUserResponse") {
		t.Error("Missing GetUserResponse interface")
	}

	// Should NOT generate voidResponse interface
	if strings.Contains(output, "export interface voidResponse") {
		t.Error("Should not generate voidResponse interface")
	}

	// One method uses Promise<GetUserResponse>, the other Promise<void>
	if !strings.Contains(output, "Promise<GetUserResponse>") {
		t.Error("Missing Promise<GetUserResponse>")
	}
	if !strings.Contains(output, "Promise<void>") {
		t.Error("Missing Promise<void>")
	}

	t.Logf("Generated TypeScript (mixed):\n%s", output)
}

// Complex nested types for testing type collection
type Tag struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type GetDashboardRequest struct{}

type GetDashboardResponse struct {
	UsersByRole   map[string][]GetUserResponse `json:"usersByRole"`
	FeaturedUsers []*GetUserResponse           `json:"featuredUsers"`
	TagsByID      map[int]Tag                  `json:"tagsById"`
}

type DashboardHandlers struct{}

func (h *DashboardHandlers) GetDashboard(ctx context.Context, req *GetDashboardRequest) (*GetDashboardResponse, error) {
	return nil, nil
}

func TestGenerateComplexTypes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&DashboardHandlers{})

	gen := NewGenerator(registry)

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	handlerContent, ok := files["dashboard-handlers.ts"]
	if !ok {
		t.Fatalf("Expected dashboard-handlers.ts, got files: %v", mapKeys(files))
	}

	// map[int]Tag → Tag must be collected as an interface
	if !strings.Contains(handlerContent, "export interface Tag") {
		t.Error("Missing Tag interface (map value struct not collected)")
	}

	// []*GetUserResponse → GetUserResponse must be collected
	if !strings.Contains(handlerContent, "export interface GetUserResponse") {
		t.Error("Missing GetUserResponse interface (slice-of-pointer struct not collected)")
	}

	// map[string][]GetUserResponse → GetUserResponse already checked above,
	// verify the type renders correctly
	if !strings.Contains(handlerContent, "Record<string, GetUserResponse[]>") {
		t.Error("Missing Record<string, GetUserResponse[]> for map-of-slice field")
	}

	if !strings.Contains(handlerContent, "Record<number, Tag>") {
		t.Error("Missing Record<number, Tag> for map-of-struct field")
	}

	if !strings.Contains(handlerContent, "GetUserResponse[]") {
		t.Error("Missing GetUserResponse[] for slice-of-pointer field")
	}

	t.Logf("Generated TypeScript (complex types):\n%s", handlerContent)
}

// Time types for testing time.Time support
type CreateEventRequest struct {
	Name    string     `json:"name"`
	StartAt time.Time  `json:"startAt"`
	EndAt   *time.Time `json:"endAt,omitempty"`
}

type CreateEventResponse struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

type EventSchedule struct {
	Events []time.Time `json:"events"`
}

type GetScheduleRequest struct{}

type GetScheduleResponse struct {
	Schedule EventSchedule `json:"schedule"`
}

type TimeHandlers struct{}

func (h *TimeHandlers) CreateEvent(ctx context.Context, req *CreateEventRequest) (*CreateEventResponse, error) {
	return &CreateEventResponse{ID: "evt_1", CreatedAt: time.Now()}, nil
}

func (h *TimeHandlers) GetSchedule(ctx context.Context, req *GetScheduleRequest) (*GetScheduleResponse, error) {
	return nil, nil
}

func TestGenerateTimeFields(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TimeHandlers{})

	gen := NewGenerator(registry)

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	handlerContent, ok := files["time-handlers.ts"]
	if !ok {
		t.Fatalf("Expected time-handlers.ts, got files: %v", mapKeys(files))
	}

	// time.Time should generate as string
	if !strings.Contains(handlerContent, "startAt: string") {
		t.Error("time.Time field should generate as string")
	}

	// *time.Time should generate as optional string
	if !strings.Contains(handlerContent, "endAt?: string") {
		t.Error("*time.Time field should generate as optional string")
	}

	// time.Time in response should also be string
	if !strings.Contains(handlerContent, "createdAt: string") {
		t.Error("time.Time in response should generate as string")
	}

	// []time.Time should generate as string[]
	if !strings.Contains(handlerContent, "events: string[]") {
		t.Error("[]time.Time should generate as string[]")
	}

	// Should NOT generate an empty Time interface
	if strings.Contains(handlerContent, "export interface Time") {
		t.Error("Should not generate a Time interface for time.Time")
	}

	// Nested struct should still be collected
	if !strings.Contains(handlerContent, "export interface EventSchedule") {
		t.Error("Missing EventSchedule interface")
	}

	t.Logf("Generated TypeScript (time):\n%s", handlerContent)
}

func TestGenerateTimeFieldsSingleFile(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TimeHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// time.Time should generate as string
	if !strings.Contains(output, "startAt: string") {
		t.Error("time.Time field should generate as string (single file)")
	}

	// Should NOT generate an empty Time interface
	if strings.Contains(output, "export interface Time") {
		t.Error("Should not generate a Time interface for time.Time (single file)")
	}
}

func TestGenerateMultipleFilesWithEnums(t *testing.T) {
	registry := NewRegistry()
	handler := &TaskHandlers{}
	registry.Register(handler)
	registry.RegisterEnumFor(handler, TaskStatusValues())
	registry.RegisterEnumFor(handler, PriorityValues())

	gen := NewGenerator(registry)

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Check handler file has enums
	handlerContent, ok := files["task-handlers.ts"]
	if !ok {
		t.Fatalf("Expected task-handlers.ts, got files: %v", mapKeys(files))
	}

	if !strings.Contains(handlerContent, "export const TaskStatus = {") {
		t.Error("Missing TaskStatus in handler file")
	}
	if !strings.Contains(handlerContent, "export const Priority = {") {
		t.Error("Missing Priority in handler file")
	}
	if !strings.Contains(handlerContent, "status: TaskStatusType") {
		t.Error("Handler file should use TaskStatusType for status field")
	}
}

// No-request handler test types

type ListItemsResponse struct {
	Items []string `json:"items"`
}

type NoRequestHandlers struct{}

func (h *NoRequestHandlers) ListItems(ctx context.Context) (*ListItemsResponse, error) {
	return &ListItemsResponse{Items: []string{"a", "b"}}, nil
}

func (h *NoRequestHandlers) Cleanup(ctx context.Context) error {
	return nil
}

func TestNoRequestHandlerRegistration(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestHandlers{})

	methods := registry.Methods()
	if len(methods) != 2 {
		t.Errorf("Expected 2 methods, got %d", len(methods))
	}

	info, ok := registry.Get("NoRequestHandlers.ListItems")
	if !ok {
		t.Fatal("ListItems not found")
	}
	if len(info.Params) != 0 {
		t.Errorf("Expected 0 params for ListItems, got %d", len(info.Params))
	}
	if info.IsVoid {
		t.Error("Expected IsVoid to be false for ListItems")
	}
	if info.ResponseType.Name() != "ListItemsResponse" {
		t.Errorf("Expected ListItemsResponse, got %s", info.ResponseType.Name())
	}

	info2, ok := registry.Get("NoRequestHandlers.Cleanup")
	if !ok {
		t.Fatal("Cleanup not found")
	}
	if len(info2.Params) != 0 {
		t.Errorf("Expected 0 params for Cleanup, got %d", len(info2.Params))
	}
	if !info2.IsVoid {
		t.Error("Expected IsVoid to be true for Cleanup")
	}
}

func TestNoRequestHandlerCall(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestHandlers{})

	info, _ := registry.Get("NoRequestHandlers.ListItems")
	result, err := info.Call(context.Background(), nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	resp, ok := result.(*ListItemsResponse)
	if !ok {
		t.Fatal("Result is not *ListItemsResponse")
	}

	if len(resp.Items) != 2 || resp.Items[0] != "a" {
		t.Errorf("Unexpected result: %v", resp.Items)
	}
}

func TestNoRequestVoidHandlerCall(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestHandlers{})

	info, _ := registry.Get("NoRequestHandlers.Cleanup")
	result, err := info.Call(context.Background(), nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result != nil {
		t.Errorf("Expected nil result, got %v", result)
	}
}

func TestGenerateNoRequest(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Should NOT generate a noRequest interface
	if strings.Contains(output, "export interface noRequest") {
		t.Error("Should not generate noRequest interface")
	}

	// ListItems should have no request param
	if !strings.Contains(output, "listItems(client: ApiClient, options?: RequestOptions): Promise<ListItemsResponse>") {
		t.Error("Missing parameter-less listItems method")
	}

	// Cleanup should have no request param and return void
	if !strings.Contains(output, "cleanup(client: ApiClient, options?: RequestOptions): Promise<void>") {
		t.Error("Missing parameter-less cleanup method")
	}

	// Should still generate response type
	if !strings.Contains(output, "export interface ListItemsResponse") {
		t.Error("Missing ListItemsResponse interface")
	}

	t.Logf("Generated TypeScript (no-request):\n%s", output)
}

func TestGenerateNoRequestReact(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestHandlers{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputReact,
	})

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Should have hooks
	if !strings.Contains(output, "export function useListItems") {
		t.Error("Missing useListItems hook")
	}
	if !strings.Contains(output, "export function useCleanup") {
		t.Error("Missing useCleanup hook")
	}

	// Query hook should not require params
	if strings.Contains(output, "UseQueryOptions<ListItemsResponse>") {
		t.Error("No-request query hook should not use UseQueryOptions with request type")
	}

	// Mutation hook should use void for request type
	if !strings.Contains(output, "UseMutationResult<[], ListItemsResponse>") {
		t.Error("Missing UseMutationResult<[], ListItemsResponse>")
	}

	t.Logf("Generated TypeScript (no-request, React):\n%s", output)
}

func TestGenerateNoRequestMultipleFiles(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestHandlers{})

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	handlerContent, ok := files["no-request-handlers.ts"]
	if !ok {
		t.Fatalf("Expected no-request-handlers.ts, got files: %v", mapKeys(files))
	}

	if strings.Contains(handlerContent, "export interface noRequest") {
		t.Error("Should not generate noRequest interface")
	}
	if !strings.Contains(handlerContent, "listItems(client: ApiClient, options?: RequestOptions): Promise<ListItemsResponse>") {
		t.Error("Missing parameter-less listItems method in handler file")
	}
	if !strings.Contains(handlerContent, "cleanup(client: ApiClient, options?: RequestOptions): Promise<void>") {
		t.Error("Missing parameter-less cleanup method in handler file")
	}

	t.Logf("Generated TypeScript (no-request, multi-file):\n%s", handlerContent)
}

func TestGenerateWithoutTasksHasNoTaskTypes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Without Enable(), no task types should be present
	mustNotContain := []struct {
		label string
		want  string
	}{
		{"TaskNodeStatus enum", "export const TaskNodeStatus = {"},
		{"TaskNodeStatusType type", "export type TaskNodeStatusType"},
		{"TaskNode interface", "export interface TaskNode"},
		{"SharedTaskState interface", "export interface SharedTaskState"},
		{"TaskRef interface", "export interface TaskRef"},
		{"cancelSharedTask function", "cancelSharedTask"},
		{"onTaskProgress field", "onTaskProgress"},
		{"onOutput field", "onOutput"},
	}
	for _, tc := range mustNotContain {
		if strings.Contains(output, tc.want) {
			t.Errorf("%s should not be present without tasks.Enable(): found %q", tc.label, tc.want)
		}
	}
}

// Handler groups for deterministic push-event test.
type PushGroupA struct{}

func (h *PushGroupA) MethodA(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return nil, nil
}

type PushGroupB struct{}

func (h *PushGroupB) MethodB(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return nil, nil
}

type PushGroupC struct{}

func (h *PushGroupC) MethodC(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return nil, nil
}

type PushGroupD struct{}

func (h *PushGroupD) MethodD(ctx context.Context, req *GetUserRequest) (*GetUserResponse, error) {
	return nil, nil
}

func TestPushEventDeterministicAssignment(t *testing.T) {
	const iterations = 10
	var firstFiles map[string]string

	for i := 0; i < iterations; i++ {
		registry := NewRegistry()
		registry.Register(&PushGroupA{})
		registry.Register(&PushGroupB{})
		registry.Register(&PushGroupC{})
		registry.Register(&PushGroupD{})
		registry.RegisterPushEventFor(&PushGroupB{}, UserUpdatedEvent{})

		gen := NewGenerator(registry)
		files, err := gen.Generate()
		if err != nil {
			t.Fatalf("iteration %d: Generate failed: %v", i, err)
		}

		if i == 0 {
			firstFiles = files
			// Verify the event lands in push-group-b.ts
			content, ok := files["push-group-b.ts"]
			if !ok {
				t.Fatalf("expected push-group-b.ts, got files: %v", mapKeys(files))
			}
			if !strings.Contains(content, "UserUpdatedEvent") {
				t.Fatal("expected UserUpdatedEvent in push-group-b.ts")
			}
		} else {
			for name, content := range files {
				if firstFiles[name] != content {
					t.Fatalf("iteration %d: file %s differs from first run", i, name)
				}
			}
		}
	}
}

func TestRegisterPushEventForPanicsOnUnregisteredHandler(t *testing.T) {
	registry := NewRegistry()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for unregistered handler")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "unregistered handler") {
			t.Fatalf("expected panic message about unregistered handler, got: %s", msg)
		}
	}()

	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
}

func TestGenerateDeterministic(t *testing.T) {
	const iterations = 10

	// Test single-file output (GenerateTo)
	t.Run("SingleFile", func(t *testing.T) {
		var first string
		for i := 0; i < iterations; i++ {
			registry := NewRegistry()
			handler := &TaskHandlers{}
			registry.Register(handler)
			registry.RegisterEnumFor(handler, TaskStatusValues())
			registry.RegisterEnumFor(handler, PriorityValues())

			gen := NewGenerator(registry)
			var buf bytes.Buffer
			if err := gen.GenerateTo(&buf); err != nil {
				t.Fatalf("iteration %d: GenerateTo failed: %v", i, err)
			}
			output := buf.String()
			if i == 0 {
				first = output
			} else if output != first {
				t.Fatalf("iteration %d: output differs from first run", i)
			}
		}
	})

	// Test multi-file output (Generate)
	t.Run("MultiFile", func(t *testing.T) {
		var firstFiles map[string]string
		for i := 0; i < iterations; i++ {
			registry := NewRegistry()
			handler := &TaskHandlers{}
			registry.Register(handler)
			registry.RegisterEnumFor(handler, TaskStatusValues())
			registry.RegisterEnumFor(handler, PriorityValues())

			gen := NewGenerator(registry)
			files, err := gen.Generate()
			if err != nil {
				t.Fatalf("iteration %d: Generate failed: %v", i, err)
			}
			if i == 0 {
				firstFiles = files
			} else {
				for name, content := range files {
					if firstFiles[name] != content {
						t.Fatalf("iteration %d: file %s differs from first run", i, name)
					}
				}
			}
		}
	})
}

// --- Bug fix tests: untagged field casing and embedded struct flattening ---

// Types for testing untagged fields (Bug 1: should use Go field name as-is)
type UntaggedFieldsRequest struct {
	ID        int64
	StationID string
	Name      string
	RtuIp     string `json:"RtuIp"`
}

type UntaggedFieldsResponse struct {
	OK bool
}

type UntaggedHandlers struct{}

func (h *UntaggedHandlers) GetStation(ctx context.Context, req *UntaggedFieldsRequest) (*UntaggedFieldsResponse, error) {
	return &UntaggedFieldsResponse{OK: true}, nil
}

func TestGenerateUntaggedFields(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&UntaggedHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Untagged fields should use the exact Go field name (PascalCase), not camelCase
	if !strings.Contains(output, "ID: number") {
		t.Error("Expected 'ID: number' (PascalCase), got camelCase or missing")
	}
	if !strings.Contains(output, "StationID: string") {
		t.Error("Expected 'StationID: string' (PascalCase), got camelCase or missing")
	}
	if !strings.Contains(output, "Name: string") {
		t.Error("Expected 'Name: string' (PascalCase), got camelCase or missing")
	}
	// Explicitly tagged field should still use the tag value
	if !strings.Contains(output, "RtuIp: string") {
		t.Error("Expected 'RtuIp: string' from json tag")
	}

	// Verify camelCase versions are NOT present
	if strings.Contains(output, "iD:") || strings.Contains(output, "iD?:") {
		t.Error("Should not have camelCase 'iD'")
	}
	if strings.Contains(output, "stationID:") {
		t.Error("Should not have camelCase 'stationID'")
	}

	t.Logf("Generated TypeScript (untagged fields):\n%s", output)
}

// Types for testing embedded struct flattening (Bug 2)
type EmbeddedBase struct {
	ID   int64  `json:"ID"`
	Name string `json:"Name"`
}

type EmbeddedOuter struct {
	EmbeddedBase
	Extra string `json:"Extra"`
}

type EmbeddedOuterResponse struct {
	OK bool `json:"OK"`
}

type EmbeddedHandlers struct{}

func (h *EmbeddedHandlers) CreateOuter(ctx context.Context, req *EmbeddedOuter) (*EmbeddedOuterResponse, error) {
	return &EmbeddedOuterResponse{OK: true}, nil
}

func TestGenerateEmbeddedStructFlattening(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&EmbeddedHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// EmbeddedOuter should have flat fields from EmbeddedBase, not a nested property
	if !strings.Contains(output, "ID: number") {
		t.Error("Expected flattened 'ID: number' from embedded struct")
	}
	if !strings.Contains(output, "Name: string") {
		t.Error("Expected flattened 'Name: string' from embedded struct")
	}
	if !strings.Contains(output, "Extra: string") {
		t.Error("Expected 'Extra: string' field")
	}

	// Should NOT have a nested property for the embedded struct
	if strings.Contains(output, "embeddedBase:") || strings.Contains(output, "EmbeddedBase:") {
		t.Error("Should not have nested property for embedded struct")
	}

	// EmbeddedBase should NOT be generated as a separate interface
	// (its fields are flattened into EmbeddedOuter)
	if strings.Contains(output, "export interface EmbeddedBase") {
		t.Error("EmbeddedBase should not be a separate interface (fields are flattened)")
	}

	t.Logf("Generated TypeScript (embedded struct):\n%s", output)
}

// Pointer-embedded struct test
type EmbeddedPointerOuter struct {
	*EmbeddedBase
	Extra string `json:"Extra"`
}

type EmbeddedPointerResponse struct {
	OK bool `json:"OK"`
}

type EmbeddedPointerHandlers struct{}

func (h *EmbeddedPointerHandlers) CreatePointerOuter(ctx context.Context, req *EmbeddedPointerOuter) (*EmbeddedPointerResponse, error) {
	return &EmbeddedPointerResponse{OK: true}, nil
}

func TestGenerateEmbeddedPointerStruct(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&EmbeddedPointerHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Pointer-embedded struct should also be flattened
	if !strings.Contains(output, "ID: number") {
		t.Error("Expected flattened 'ID: number' from pointer-embedded struct")
	}
	if !strings.Contains(output, "Name: string") {
		t.Error("Expected flattened 'Name: string' from pointer-embedded struct")
	}
	if !strings.Contains(output, "Extra: string") {
		t.Error("Expected 'Extra: string' field")
	}

	// Should NOT have a nested property
	if strings.Contains(output, "embeddedBase:") || strings.Contains(output, "EmbeddedBase:") {
		t.Error("Should not have nested property for pointer-embedded struct")
	}

	t.Logf("Generated TypeScript (pointer-embedded struct):\n%s", output)
}

// Embedded struct with nested type that should still be collected
type EmbeddedWithNestedType struct {
	ID   int64 `json:"ID"`
	Tags []Tag `json:"Tags"`
}

type OuterWithNestedFromEmbed struct {
	EmbeddedWithNestedType
	Extra string `json:"Extra"`
}

type NestedEmbedResponse struct {
	OK bool `json:"OK"`
}

type NestedEmbedHandlers struct{}

func (h *NestedEmbedHandlers) Create(ctx context.Context, req *OuterWithNestedFromEmbed) (*NestedEmbedResponse, error) {
	return &NestedEmbedResponse{OK: true}, nil
}

func TestGenerateEmbeddedStructNestedTypeCollection(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NestedEmbedHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Tag type referenced from embedded struct should still be collected
	if !strings.Contains(output, "export interface Tag") {
		t.Error("Tag interface should be collected from embedded struct's fields")
	}

	// Fields from embedded struct should be flattened
	if !strings.Contains(output, "ID: number") {
		t.Error("Expected flattened 'ID: number'")
	}
	if !strings.Contains(output, "Tags: Tag[]") {
		t.Error("Expected flattened 'Tags: Tag[]'")
	}

	t.Logf("Generated TypeScript (nested type from embed):\n%s", output)
}

// Multi-file test for embedded struct flattening
func TestGenerateEmbeddedStructMultiFile(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&EmbeddedHandlers{})

	gen := NewGenerator(registry)

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	handlerContent, ok := files["embedded-handlers.ts"]
	if !ok {
		t.Fatalf("Expected embedded-handlers.ts, got files: %v", mapKeys(files))
	}

	// Same assertions as single-file test
	if !strings.Contains(handlerContent, "ID: number") {
		t.Error("Expected flattened 'ID: number' in multi-file output")
	}
	if !strings.Contains(handlerContent, "Name: string") {
		t.Error("Expected flattened 'Name: string' in multi-file output")
	}
	if strings.Contains(handlerContent, "export interface EmbeddedBase") {
		t.Error("EmbeddedBase should not be a separate interface in multi-file output")
	}
}

// --- Arbitrary return type tests ---

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ArbitraryReturnHandlers struct{}

func (h *ArbitraryReturnHandlers) ListUsers(ctx context.Context) ([]User, error) {
	return []User{{ID: 1, Name: "Alice"}}, nil
}

func (h *ArbitraryReturnHandlers) GetScores(ctx context.Context) (map[string]int, error) {
	return map[string]int{"alice": 100}, nil
}

func (h *ArbitraryReturnHandlers) GetName(ctx context.Context) (string, error) {
	return "Alice", nil
}

func (h *ArbitraryReturnHandlers) GetCount(ctx context.Context) (int, error) {
	return 42, nil
}

func (h *ArbitraryReturnHandlers) GetActive(ctx context.Context) (bool, error) {
	return true, nil
}

func (h *ArbitraryReturnHandlers) GetUser(ctx context.Context) (*User, error) {
	return &User{ID: 1, Name: "Alice"}, nil
}

func TestArbitraryReturnTypeRegistration(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ArbitraryReturnHandlers{})

	tests := []struct {
		method string
		isVoid bool
	}{
		{"ArbitraryReturnHandlers.ListUsers", false},
		{"ArbitraryReturnHandlers.GetScores", false},
		{"ArbitraryReturnHandlers.GetName", false},
		{"ArbitraryReturnHandlers.GetCount", false},
		{"ArbitraryReturnHandlers.GetActive", false},
		{"ArbitraryReturnHandlers.GetUser", false},
	}

	for _, tt := range tests {
		info, ok := registry.Get(tt.method)
		if !ok {
			t.Errorf("%s not found", tt.method)
			continue
		}
		if info.IsVoid != tt.isVoid {
			t.Errorf("%s: expected IsVoid=%v, got %v", tt.method, tt.isVoid, info.IsVoid)
		}
	}
}

func TestArbitraryReturnTypeCall(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ArbitraryReturnHandlers{})

	t.Run("slice return", func(t *testing.T) {
		info, _ := registry.Get("ArbitraryReturnHandlers.ListUsers")
		result, err := info.Call(context.Background(), nil)
		if err != nil {
			t.Fatalf("Call failed: %v", err)
		}
		users, ok := result.([]User)
		if !ok {
			t.Fatalf("Result is not []User, got %T", result)
		}
		if len(users) != 1 || users[0].Name != "Alice" {
			t.Errorf("Unexpected result: %v", users)
		}
	})

	t.Run("map return", func(t *testing.T) {
		info, _ := registry.Get("ArbitraryReturnHandlers.GetScores")
		result, err := info.Call(context.Background(), nil)
		if err != nil {
			t.Fatalf("Call failed: %v", err)
		}
		scores, ok := result.(map[string]int)
		if !ok {
			t.Fatalf("Result is not map[string]int, got %T", result)
		}
		if scores["alice"] != 100 {
			t.Errorf("Unexpected result: %v", scores)
		}
	})

	t.Run("string return", func(t *testing.T) {
		info, _ := registry.Get("ArbitraryReturnHandlers.GetName")
		result, err := info.Call(context.Background(), nil)
		if err != nil {
			t.Fatalf("Call failed: %v", err)
		}
		name, ok := result.(string)
		if !ok {
			t.Fatalf("Result is not string, got %T", result)
		}
		if name != "Alice" {
			t.Errorf("Expected Alice, got %s", name)
		}
	})

	t.Run("int return", func(t *testing.T) {
		info, _ := registry.Get("ArbitraryReturnHandlers.GetCount")
		result, err := info.Call(context.Background(), nil)
		if err != nil {
			t.Fatalf("Call failed: %v", err)
		}
		count, ok := result.(int)
		if !ok {
			t.Fatalf("Result is not int, got %T", result)
		}
		if count != 42 {
			t.Errorf("Expected 42, got %d", count)
		}
	})

	t.Run("pointer to struct return", func(t *testing.T) {
		info, _ := registry.Get("ArbitraryReturnHandlers.GetUser")
		result, err := info.Call(context.Background(), nil)
		if err != nil {
			t.Fatalf("Call failed: %v", err)
		}
		user, ok := result.(*User)
		if !ok {
			t.Fatalf("Result is not *User, got %T", result)
		}
		if user.Name != "Alice" {
			t.Errorf("Expected Alice, got %s", user.Name)
		}
	})
}

func TestArbitraryReturnTypeGenerate(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ArbitraryReturnHandlers{})

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, ok := files["arbitrary-return-handlers.ts"]
	if !ok {
		t.Fatalf("Expected arbitrary-return-handlers.ts, got files: %v", mapKeys(files))
	}

	t.Logf("Generated TypeScript:\n%s", content)

	// Slice return → User[]
	if !strings.Contains(content, "Promise<User[]>") {
		t.Error("Expected Promise<User[]> for slice return type")
	}

	// Map return → Record<string, number>
	if !strings.Contains(content, "Promise<Record<string, number>>") {
		t.Error("Expected Promise<Record<string, number>> for map return type")
	}

	// String return → string
	if !strings.Contains(content, "Promise<string>") {
		t.Error("Expected Promise<string> for string return type")
	}

	// Int return → number
	if !strings.Contains(content, "Promise<number>") {
		t.Error("Expected Promise<number> for int return type")
	}

	// Bool return → boolean
	if !strings.Contains(content, "Promise<boolean>") {
		t.Error("Expected Promise<boolean> for bool return type")
	}

	// Pointer-to-struct return → User (same as before)
	if !strings.Contains(content, "Promise<User>") {
		t.Error("Expected Promise<User> for pointer-to-struct return type")
	}

	// User interface should be generated (used by both slice and pointer returns)
	if !strings.Contains(content, "export interface User") {
		t.Error("Expected User interface to be generated")
	}

	// No interface should be generated for primitive return types
	for _, bad := range []string{"export interface string", "export interface number", "export interface boolean"} {
		if strings.Contains(content, bad) {
			t.Errorf("Should not generate %s", bad)
		}
	}
}

// --- Custom marshaler test types ---

// StringMarshaler is a type whose JSON representation is always a string.
type StringMarshaler struct{ Value string }

func (s StringMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Value)
}

// NumberMarshaler is a type whose JSON representation is always a number.
type NumberMarshaler struct{ Value float64 }

func (n NumberMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(n.Value)
}

// BoolMarshaler is a type whose JSON representation is always a boolean.
type BoolMarshaler struct{ Value bool }

func (b BoolMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(b.Value)
}

// TextMarshalerType implements only encoding.TextMarshaler (not json.Marshaler).
type TextMarshalerType struct{ Text string }

func (t TextMarshalerType) MarshalText() ([]byte, error) {
	return []byte(t.Text), nil
}

// UUIDLike is a [16]byte that marshals to a string, similar to google/uuid.
type UUIDLike [16]byte

func (u UUIDLike) MarshalJSON() ([]byte, error) {
	return json.Marshal("00000000-0000-0000-0000-000000000000")
}

// ObjectMarshaler always marshals to a JSON object — should fall through.
type ObjectMarshaler struct{ X int }

func (o ObjectMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]int{"x": o.X})
}

// ArrayOfStringsMarshaler always marshals to a JSON array of strings.
type ArrayOfStringsMarshaler struct{}

func (a ArrayOfStringsMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"a", "b"})
}

// ArrayOfNumbersMarshaler always marshals to a JSON array of numbers.
type ArrayOfNumbersMarshaler struct{}

func (a ArrayOfNumbersMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal([]float64{1, 2, 3})
}

// EmptyArrayMarshaler always marshals to an empty JSON array.
type EmptyArrayMarshaler struct{}

func (a EmptyArrayMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{})
}

// HomogeneousObjectMarshaler always marshals to a JSON object with number values.
type HomogeneousObjectMarshaler struct{}

func (o HomogeneousObjectMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]int{"a": 1, "b": 2})
}

// EmptyObjectMarshaler always marshals to an empty JSON object.
type EmptyObjectMarshaler struct{}

func (o EmptyObjectMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{})
}

// HeterogeneousObjectMarshaler always marshals to a JSON object with mixed value types.
type HeterogeneousObjectMarshaler struct{}

func (o HeterogeneousObjectMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"a": 1, "b": "x"})
}

// PtrReceiverMarshaler has MarshalJSON on *T, not T.
type PtrReceiverMarshaler struct{ Value string }

func (p *PtrReceiverMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.Value)
}

func TestInferTypeFromMarshal(t *testing.T) {
	tests := []struct {
		name     string
		typ      reflect.Type
		wantNil  bool
		wantType string
	}{
		{
			name:     "StringMarshaler → string",
			typ:      reflect.TypeOf(StringMarshaler{}),
			wantType: "string",
		},
		{
			name:     "NumberMarshaler → number",
			typ:      reflect.TypeOf(NumberMarshaler{}),
			wantType: "number",
		},
		{
			name:     "BoolMarshaler → boolean",
			typ:      reflect.TypeOf(BoolMarshaler{}),
			wantType: "boolean",
		},
		{
			name:     "TextMarshalerType → string",
			typ:      reflect.TypeOf(TextMarshalerType{}),
			wantType: "string",
		},
		{
			name:     "UUIDLike → string",
			typ:      reflect.TypeOf(UUIDLike{}),
			wantType: "string",
		},
		{
			name:     "ObjectMarshaler → Record<string, number>",
			typ:      reflect.TypeOf(ObjectMarshaler{}),
			wantType: "Record<string, number>",
		},
		{
			name:     "HomogeneousObjectMarshaler → Record<string, number>",
			typ:      reflect.TypeOf(HomogeneousObjectMarshaler{}),
			wantType: "Record<string, number>",
		},
		{
			name:     "EmptyObjectMarshaler → Record<string, any>",
			typ:      reflect.TypeOf(EmptyObjectMarshaler{}),
			wantType: "Record<string, any>",
		},
		{
			name:     "HeterogeneousObjectMarshaler → Record<string, any>",
			typ:      reflect.TypeOf(HeterogeneousObjectMarshaler{}),
			wantType: "Record<string, any>",
		},
		{
			name:     "ArrayOfStringsMarshaler → string[]",
			typ:      reflect.TypeOf(ArrayOfStringsMarshaler{}),
			wantType: "string[]",
		},
		{
			name:     "ArrayOfNumbersMarshaler → number[]",
			typ:      reflect.TypeOf(ArrayOfNumbersMarshaler{}),
			wantType: "number[]",
		},
		{
			name:     "EmptyArrayMarshaler → any[]",
			typ:      reflect.TypeOf(EmptyArrayMarshaler{}),
			wantType: "any[]",
		},
		{
			name:     "PtrReceiverMarshaler → string",
			typ:      reflect.TypeOf(PtrReceiverMarshaler{}),
			wantType: "string",
		},
		{
			name:     "time.Time → string",
			typ:      reflect.TypeOf(time.Time{}),
			wantType: "string",
		},
		{
			name:    "plain struct (no marshaler) → nil",
			typ:     reflect.TypeOf(CreateUserRequest{}),
			wantNil: true,
		},
		{
			name:    "interface{} / any → nil",
			typ:     reflect.TypeOf((*interface{})(nil)).Elem(),
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := InferTypeFromMarshal(tc.typ)
			if tc.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result, got nil")
			}
			if result.TSType != tc.wantType {
				t.Errorf("expected TSType=%q, got %q", tc.wantType, result.TSType)
			}
		})
	}
}

func TestGoTypeToTSCustomMarshalers(t *testing.T) {
	registry := NewRegistry()
	g := NewGenerator(registry)

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"StringMarshaler", reflect.TypeOf(StringMarshaler{}), "string"},
		{"NumberMarshaler", reflect.TypeOf(NumberMarshaler{}), "number"},
		{"BoolMarshaler", reflect.TypeOf(BoolMarshaler{}), "boolean"},
		{"TextMarshalerType", reflect.TypeOf(TextMarshalerType{}), "string"},
		{"UUIDLike", reflect.TypeOf(UUIDLike{}), "string"},
		{"PtrReceiverMarshaler", reflect.TypeOf(PtrReceiverMarshaler{}), "string"},
		{"ArrayOfStringsMarshaler", reflect.TypeOf(ArrayOfStringsMarshaler{}), "string[]"},
		{"ArrayOfNumbersMarshaler", reflect.TypeOf(ArrayOfNumbersMarshaler{}), "number[]"},
		{"EmptyArrayMarshaler", reflect.TypeOf(EmptyArrayMarshaler{}), "any[]"},
		{"HomogeneousObjectMarshaler", reflect.TypeOf(HomogeneousObjectMarshaler{}), "Record<string, number>"},
		{"EmptyObjectMarshaler", reflect.TypeOf(EmptyObjectMarshaler{}), "Record<string, any>"},
		{"HeterogeneousObjectMarshaler", reflect.TypeOf(HeterogeneousObjectMarshaler{}), "Record<string, any>"},
		{"time.Time still string", reflect.TypeOf(time.Time{}), "string"},
		{"plain struct unchanged", reflect.TypeOf(CreateUserRequest{}), "CreateUserRequest"},
		{"int unchanged", reflect.TypeOf(0), "number"},
		{"string unchanged", reflect.TypeOf(""), "string"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := g.goTypeToTS(tc.typ)
			if got != tc.want {
				t.Errorf("goTypeToTS(%v) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}

// Types for TestGenerateCustomMarshalerTypes

type MarshalerRequest struct {
	ID   UUIDLike        `json:"id"`
	Name StringMarshaler `json:"name"`
}

type MarshalerResponse struct {
	ID    UUIDLike        `json:"id"`
	Score NumberMarshaler `json:"score"`
}

type MarshalerHandlers struct{}

func (h *MarshalerHandlers) DoThing(ctx context.Context, req *MarshalerRequest) (*MarshalerResponse, error) {
	return nil, nil
}

func TestGenerateCustomMarshalerTypes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&MarshalerHandlers{})

	gen := NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// The MarshalerRequest should have string fields, not interfaces for UUIDLike/StringMarshaler
	if strings.Contains(out, "export interface UUIDLike") {
		t.Error("UUIDLike should not generate an interface — it marshals to string")
	}
	if strings.Contains(out, "export interface StringMarshaler") {
		t.Error("StringMarshaler should not generate an interface — it marshals to string")
	}
	if strings.Contains(out, "export interface NumberMarshaler") {
		t.Error("NumberMarshaler should not generate an interface — it marshals to number")
	}

	// The fields in MarshalerRequest/MarshalerResponse should be primitives
	if !strings.Contains(out, "id: string") {
		t.Error("Expected MarshalerRequest.id to be 'string' (from UUIDLike marshaler)")
	}
	if !strings.Contains(out, "name: string") {
		t.Error("Expected MarshalerRequest.name to be 'string' (from StringMarshaler)")
	}
	if !strings.Contains(out, "score: number") {
		t.Error("Expected MarshalerResponse.score to be 'number' (from NumberMarshaler)")
	}
}

// Types for TestGenerateMixedMarshalerAndStructFields

// NestedDetail is a plain exported struct (no marshaler).
type NestedDetail struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type MixedFieldsRequest struct {
	ID      UUIDLike        `json:"id"`
	Score   NumberMarshaler `json:"score"`
	Detail  NestedDetail    `json:"detail"`
	Details []NestedDetail  `json:"details"`
}

type MixedFieldsResponse struct {
	ID      UUIDLike        `json:"id"`
	Score   NumberMarshaler `json:"score"`
	Detail  NestedDetail    `json:"detail"`
	Details []NestedDetail  `json:"details"`
}

type MixedFieldsHandlers struct{}

func (h *MixedFieldsHandlers) ProcessMixed(ctx context.Context, req *MixedFieldsRequest) (*MixedFieldsResponse, error) {
	return nil, nil
}

func TestGenerateMixedMarshalerAndStructFields(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&MixedFieldsHandlers{})

	gen := NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// NestedDetail (non-marshaler struct used as field type) MUST generate an interface
	if !strings.Contains(out, "export interface NestedDetail") {
		t.Error("expected NestedDetail interface to be generated (plain struct used as field type)")
	}

	// Extract MixedFieldsRequest interface block to check field types
	reqStart := strings.Index(out, "interface MixedFieldsRequest")
	if reqStart == -1 {
		t.Fatal("MixedFieldsRequest interface not found in output")
	}
	reqBlock := out[reqStart:]
	braceEnd := strings.Index(reqBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for MixedFieldsRequest")
	}
	reqBlock = reqBlock[:braceEnd+1]

	// detail field should resolve to NestedDetail, not any
	if !strings.Contains(reqBlock, "detail: NestedDetail") {
		t.Errorf("expected MixedFieldsRequest.detail to be 'NestedDetail', got:\n%s", reqBlock)
	}
	// details field should resolve to NestedDetail[], not any[]
	if !strings.Contains(reqBlock, "details: NestedDetail[]") {
		t.Errorf("expected MixedFieldsRequest.details to be 'NestedDetail[]', got:\n%s", reqBlock)
	}
	// id field should resolve to string (UUIDLike marshaler)
	if !strings.Contains(reqBlock, "id: string") {
		t.Errorf("expected MixedFieldsRequest.id to be 'string' (from UUIDLike marshaler), got:\n%s", reqBlock)
	}
	// score field should resolve to number (NumberMarshaler)
	if !strings.Contains(reqBlock, "score: number") {
		t.Errorf("expected MixedFieldsRequest.score to be 'number' (from NumberMarshaler), got:\n%s", reqBlock)
	}

	// Marshaler types should NOT generate interfaces
	if strings.Contains(out, "export interface UUIDLike") {
		t.Error("UUIDLike should not generate an interface — it marshals to string")
	}
	if strings.Contains(out, "export interface NumberMarshaler") {
		t.Error("NumberMarshaler should not generate an interface — it marshals to number")
	}
}

// NonNilSlice is a generic wrapper that ensures nil slices marshal as [] not null.
type NonNilSlice[T any] []T

func (s NonNilSlice[T]) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]T(s))
}

// Types for TestGenerateSliceMarshalerWrapper

type WrappedSliceRequest struct {
	ID      UUIDLike                  `json:"id"`
	Details NonNilSlice[NestedDetail] `json:"details"`
	Tags    NonNilSlice[string]       `json:"tags"`
}

type WrappedSliceResponse struct {
	Items NonNilSlice[NestedDetail] `json:"items"`
}

type WrappedSliceHandlers struct{}

func (h *WrappedSliceHandlers) ListItems(ctx context.Context, req *WrappedSliceRequest) (*WrappedSliceResponse, error) {
	return nil, nil
}

func TestGenerateSliceMarshalerWrapper(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&WrappedSliceHandlers{})

	gen := NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// NestedDetail MUST generate an interface even when only used inside NonNilSlice
	if !strings.Contains(out, "export interface NestedDetail") {
		t.Error("expected NestedDetail interface to be generated (used as element type in NonNilSlice)")
	}

	// NonNilSlice itself should NOT generate an interface
	if strings.Contains(out, "interface NonNilSlice") {
		t.Error("NonNilSlice should not generate an interface — it's a slice wrapper with a marshaler")
	}

	// Extract WrappedSliceRequest interface block
	reqStart := strings.Index(out, "interface WrappedSliceRequest")
	if reqStart == -1 {
		t.Fatal("WrappedSliceRequest interface not found in output")
	}
	reqBlock := out[reqStart:]
	braceEnd := strings.Index(reqBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for WrappedSliceRequest")
	}
	reqBlock = reqBlock[:braceEnd+1]

	// details field: NonNilSlice[NestedDetail] should resolve to NestedDetail[], not any[]
	if !strings.Contains(reqBlock, "details: NestedDetail[]") {
		t.Errorf("expected WrappedSliceRequest.details to be 'NestedDetail[]', got:\n%s", reqBlock)
	}
	// tags field: NonNilSlice[string] should resolve to string[], not any[]
	if !strings.Contains(reqBlock, "tags: string[]") {
		t.Errorf("expected WrappedSliceRequest.tags to be 'string[]', got:\n%s", reqBlock)
	}
	// id field should still resolve to string (UUIDLike marshaler)
	if !strings.Contains(reqBlock, "id: string") {
		t.Errorf("expected WrappedSliceRequest.id to be 'string', got:\n%s", reqBlock)
	}

	// Extract WrappedSliceResponse interface block
	respStart := strings.Index(out, "interface WrappedSliceResponse")
	if respStart == -1 {
		t.Fatal("WrappedSliceResponse interface not found in output")
	}
	respBlock := out[respStart:]
	braceEnd = strings.Index(respBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for WrappedSliceResponse")
	}
	respBlock = respBlock[:braceEnd+1]

	// items field: NonNilSlice[NestedDetail] should resolve to NestedDetail[], not any[]
	if !strings.Contains(respBlock, "items: NestedDetail[]") {
		t.Errorf("expected WrappedSliceResponse.items to be 'NestedDetail[]', got:\n%s", respBlock)
	}
}

// --- sql.Null type tests ---

func TestSQLNullTSType(t *testing.T) {
	resolver := func(t reflect.Type) string {
		switch t.Kind() {
		case reflect.String:
			return "string"
		case reflect.Int, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Float32, reflect.Float64, reflect.Uint8:
			return "number"
		case reflect.Bool:
			return "boolean"
		default:
			return "any"
		}
	}

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"NullString", reflect.TypeOf(sql.NullString{}), "string | null"},
		{"NullInt64", reflect.TypeOf(sql.NullInt64{}), "number | null"},
		{"NullInt32", reflect.TypeOf(sql.NullInt32{}), "number | null"},
		{"NullInt16", reflect.TypeOf(sql.NullInt16{}), "number | null"},
		{"NullFloat64", reflect.TypeOf(sql.NullFloat64{}), "number | null"},
		{"NullBool", reflect.TypeOf(sql.NullBool{}), "boolean | null"},
		{"NullByte", reflect.TypeOf(sql.NullByte{}), "number | null"},
		{"NullTime", reflect.TypeOf(sql.NullTime{}), "string | null"},
		{"Null[string]", reflect.TypeOf(sql.Null[string]{}), "string | null"},
		{"Null[int]", reflect.TypeOf(sql.Null[int]{}), "number | null"},
		{"Null[bool]", reflect.TypeOf(sql.Null[bool]{}), "boolean | null"},
		{"Null[float64]", reflect.TypeOf(sql.Null[float64]{}), "number | null"},
		{"plain struct → empty", reflect.TypeOf(CreateUserRequest{}), ""},
		{"time.Time → empty", reflect.TypeOf(time.Time{}), ""},
		{"int → empty", reflect.TypeOf(0), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SQLNullTSType(tc.typ, resolver)
			if got != tc.want {
				t.Errorf("SQLNullTSType(%v) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}

func TestGoTypeToTSSQLNull(t *testing.T) {
	registry := NewRegistry()
	g := NewGenerator(registry)

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"NullString", reflect.TypeOf(sql.NullString{}), "string | null"},
		{"NullInt64", reflect.TypeOf(sql.NullInt64{}), "number | null"},
		{"NullInt32", reflect.TypeOf(sql.NullInt32{}), "number | null"},
		{"NullInt16", reflect.TypeOf(sql.NullInt16{}), "number | null"},
		{"NullFloat64", reflect.TypeOf(sql.NullFloat64{}), "number | null"},
		{"NullBool", reflect.TypeOf(sql.NullBool{}), "boolean | null"},
		{"NullByte", reflect.TypeOf(sql.NullByte{}), "number | null"},
		{"NullTime", reflect.TypeOf(sql.NullTime{}), "string | null"},
		{"Null[string]", reflect.TypeOf(sql.Null[string]{}), "string | null"},
		{"Null[int]", reflect.TypeOf(sql.Null[int]{}), "number | null"},
		{"slice of NullString", reflect.TypeOf([]sql.NullString{}), "(string | null)[]"},
		{"pointer to NullString", reflect.TypeOf((*sql.NullString)(nil)), "string | null"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := g.goTypeToTS(tc.typ)
			if got != tc.want {
				t.Errorf("goTypeToTS(%v) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}

// Types for TestGenerateSQLNullTypes

type SQLNullRequest struct {
	Name   sql.NullString   `json:"name"`
	Age    sql.NullInt64    `json:"age"`
	Active sql.NullBool     `json:"active"`
	Score  sql.NullFloat64  `json:"score"`
	Rank   sql.NullInt32    `json:"rank"`
	Level  sql.NullInt16    `json:"level"`
	Code   sql.NullByte     `json:"code"`
	Born   sql.NullTime     `json:"born"`
	Tags   []sql.NullString `json:"tags"`
	Note   sql.Null[string] `json:"note"`
}

type SQLNullResponse struct {
	ID string `json:"id"`
}

type SQLNullHandlers struct{}

func (h *SQLNullHandlers) DoThing(ctx context.Context, req *SQLNullRequest) (*SQLNullResponse, error) {
	return nil, nil
}

func TestGenerateSQLNullTypes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&SQLNullHandlers{})

	gen := NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// Must NOT generate interfaces for sql.Null types
	for _, bad := range []string{
		"export interface NullString",
		"export interface NullInt64",
		"export interface NullInt32",
		"export interface NullInt16",
		"export interface NullFloat64",
		"export interface NullBool",
		"export interface NullByte",
		"export interface NullTime",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("Should not generate %s — sql.Null types are primitives", bad)
		}
	}

	// Extract SQLNullRequest interface block
	reqStart := strings.Index(out, "interface SQLNullRequest")
	if reqStart == -1 {
		t.Fatal("SQLNullRequest interface not found in output")
	}
	reqBlock := out[reqStart:]
	braceEnd := strings.Index(reqBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for SQLNullRequest")
	}
	reqBlock = reqBlock[:braceEnd+1]

	// Fields should be nullable primitives, not optional
	expectations := []struct {
		field string
		want  string
	}{
		{"name", "name: string | null;"},
		{"age", "age: number | null;"},
		{"active", "active: boolean | null;"},
		{"score", "score: number | null;"},
		{"rank", "rank: number | null;"},
		{"level", "level: number | null;"},
		{"code", "code: number | null;"},
		{"born", "born: string | null;"},
		{"tags", "tags: (string | null)[];"},
		{"note", "note: string | null;"},
	}

	for _, exp := range expectations {
		if !strings.Contains(reqBlock, exp.want) {
			t.Errorf("Expected field %q to be %q in:\n%s", exp.field, exp.want, reqBlock)
		}
	}

	// Fields should NOT be optional (no ? marker)
	for _, field := range []string{"name", "age", "active", "score", "rank", "level", "code", "born", "tags", "note"} {
		if strings.Contains(reqBlock, field+"?:") {
			t.Errorf("sql.Null field %q should not be optional", field)
		}
	}
}

// --- Shared type deduplication tests ---

type SharedResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type GroupAOnlyResp struct {
	ValueA string `json:"value_a"`
}

type GroupBOnlyResp struct {
	ValueB string `json:"value_b"`
}

type DedupeGroupA struct{}

func (h *DedupeGroupA) MethodA(ctx context.Context) (*SharedResp, error) {
	return nil, nil
}

func (h *DedupeGroupA) MethodA2(ctx context.Context) (*GroupAOnlyResp, error) {
	return nil, nil
}

type DedupeGroupB struct{}

func (h *DedupeGroupB) MethodB(ctx context.Context) (*SharedResp, error) {
	return nil, nil
}

func (h *DedupeGroupB) MethodB2(ctx context.Context) (*GroupBOnlyResp, error) {
	return nil, nil
}

func TestGenerateSharedTypeDedup(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&DedupeGroupA{})
	registry.Register(&DedupeGroupB{})

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Shared types go into a per-package file (aprot.ts since test types are in package aprot)
	sharedContent, ok := files["aprot.ts"]
	if !ok {
		t.Fatalf("Expected aprot.ts for shared types, got files: %v", mapKeys(files))
	}
	groupAContent := files["dedupe-group-a.ts"]
	groupBContent := files["dedupe-group-b.ts"]
	baseContent := files["client.ts"]

	// SharedResp should be in the shared package file, not in handler files or client.ts
	if !strings.Contains(sharedContent, "export interface SharedResp") {
		t.Error("Expected SharedResp in aprot.ts")
	}
	if strings.Contains(groupAContent, "export interface SharedResp") {
		t.Error("SharedResp should not be in dedupe-group-a.ts")
	}
	if strings.Contains(groupBContent, "export interface SharedResp") {
		t.Error("SharedResp should not be in dedupe-group-b.ts")
	}
	if strings.Contains(baseContent, "export interface SharedResp") {
		t.Error("SharedResp should not be in client.ts")
	}

	// Handler files should import SharedResp from the package file
	if !strings.Contains(groupAContent, "SharedResp") {
		t.Error("Expected SharedResp reference in dedupe-group-a.ts")
	}
	if !strings.Contains(groupBContent, "SharedResp") {
		t.Error("Expected SharedResp reference in dedupe-group-b.ts")
	}
	if !strings.Contains(groupAContent, "from './aprot'") {
		t.Error("Expected import from './aprot' in dedupe-group-a.ts")
	}
	if !strings.Contains(groupBContent, "from './aprot'") {
		t.Error("Expected import from './aprot' in dedupe-group-b.ts")
	}

	// Group-specific types should stay in their handler files
	if !strings.Contains(groupAContent, "export interface GroupAOnlyResp") {
		t.Error("Expected GroupAOnlyResp in dedupe-group-a.ts")
	}
	if !strings.Contains(groupBContent, "export interface GroupBOnlyResp") {
		t.Error("Expected GroupBOnlyResp in dedupe-group-b.ts")
	}
	if strings.Contains(sharedContent, "GroupAOnlyResp") {
		t.Error("GroupAOnlyResp should not be in aprot.ts")
	}
	if strings.Contains(sharedContent, "GroupBOnlyResp") {
		t.Error("GroupBOnlyResp should not be in aprot.ts")
	}
}

func TestGenerateSharedTypeDedupWithNestedTypes(t *testing.T) {
	// PushGroupA and PushGroupB both use GetUserRequest/GetUserResponse — these should be shared
	registry := NewRegistry()
	registry.Register(&PushGroupA{})
	registry.Register(&PushGroupB{})
	registry.RegisterPushEventFor(&PushGroupB{}, UserUpdatedEvent{})

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	sharedContent, ok := files["aprot.ts"]
	if !ok {
		t.Fatalf("Expected aprot.ts for shared types, got files: %v", mapKeys(files))
	}
	groupAContent := files["push-group-a.ts"]
	groupBContent := files["push-group-b.ts"]

	// GetUserRequest and GetUserResponse are used by both groups — should be in shared file
	if !strings.Contains(sharedContent, "export interface GetUserRequest") {
		t.Error("Expected GetUserRequest in aprot.ts")
	}
	if !strings.Contains(sharedContent, "export interface GetUserResponse") {
		t.Error("Expected GetUserResponse in aprot.ts")
	}

	// Should NOT be in handler files
	if strings.Contains(groupAContent, "export interface GetUserRequest") {
		t.Error("GetUserRequest should not be in push-group-a.ts")
	}
	if strings.Contains(groupBContent, "export interface GetUserResponse") {
		t.Error("GetUserResponse should not be in push-group-b.ts")
	}

	// Handler files should import shared types from the package file
	if !strings.Contains(groupAContent, "GetUserRequest") || !strings.Contains(groupAContent, "from './aprot'") {
		t.Error("Expected GetUserRequest import from './aprot' in push-group-a.ts")
	}
}

func TestContainsTypeName(t *testing.T) {
	tests := []struct {
		typeStr string
		name    string
		want    bool
	}{
		{"StationHistoryActionType", "HistoryAction", false},
		{"HistoryAction[]", "HistoryAction", true},
		{"HistoryAction", "HistoryAction", true},
		{"Record<string, HistoryAction>", "HistoryAction", true},
		{"HistoryAction | null", "HistoryAction", true},
		{"SomeHistoryAction", "HistoryAction", false},
		{"HistoryActionExtra", "HistoryAction", false},
		{"User", "User", true},
		{"User[]", "User", true},
		{"UserProfile", "User", false},
		{"AdminUser", "User", false},
	}
	for _, tt := range tests {
		got := containsTypeName(tt.typeStr, tt.name)
		if got != tt.want {
			t.Errorf("containsTypeName(%q, %q) = %v, want %v", tt.typeStr, tt.name, got, tt.want)
		}
	}
}

// PhantomResp is shared between two groups. It has a field whose type name
// is a superstring of PhantomResp's name (PhantomRespDetailType enum).
type PhantomResp struct {
	ID     string                `json:"id"`
	Detail PhantomRespDetailType `json:"detail"`
}

type PhantomRespDetailType string

type PhantomGroupSpecific struct {
	Value string `json:"value"`
}

type PhantomGroupA struct{}

func (h *PhantomGroupA) DoA(ctx context.Context) (*PhantomResp, error) { return nil, nil }

type PhantomGroupB struct{}

func (h *PhantomGroupB) DoB(ctx context.Context) (*PhantomResp, error)           { return nil, nil }
func (h *PhantomGroupB) DoB2(ctx context.Context) (*PhantomGroupSpecific, error) { return nil, nil }

func TestGenerateSharedTypeNoPhantomImports(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&PhantomGroupA{})
	registry.Register(&PhantomGroupB{})

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// PhantomResp is shared, goes to aprot.ts
	sharedContent, ok := files["aprot.ts"]
	if !ok {
		t.Fatalf("Expected aprot.ts, got files: %v", mapKeys(files))
	}
	if !strings.Contains(sharedContent, "export interface PhantomResp") {
		t.Error("Expected PhantomResp in aprot.ts")
	}

	// PhantomGroupB has DoB2 returning PhantomGroupSpecific — it references PhantomResp
	// only via method return type, not PhantomRespDetailType. The handler should NOT
	// import PhantomResp (it's not used directly in the handler file — only in the shared file's fields).
	groupBContent := files["phantom-group-b.ts"]

	// PhantomGroupSpecific should be in handler file (only used by group B)
	if !strings.Contains(groupBContent, "export interface PhantomGroupSpecific") {
		t.Error("Expected PhantomGroupSpecific in phantom-group-b.ts")
	}

	// PhantomResp SHOULD be imported (it's the return type of DoB)
	if !strings.Contains(groupBContent, "PhantomResp") {
		t.Error("Expected PhantomResp import in phantom-group-b.ts")
	}

	// PhantomRespDetailType should NOT be imported (it's only used inside PhantomResp in the shared file)
	// This is the phantom import bug — "PhantomResp" is a substring of "PhantomRespDetailType"
	if strings.Contains(groupBContent, "PhantomRespDetailType") {
		t.Error("PhantomRespDetailType should NOT be imported in phantom-group-b.ts (phantom import from substring match)")
	}
}

// --- Shared enum tests (RegisterEnum without handler) ---

type SharedEnumGroupA struct{}

func (h *SharedEnumGroupA) GetItems(ctx context.Context) (*SharedResp, error) {
	return nil, nil
}

type SharedEnumGroupB struct{}

func (h *SharedEnumGroupB) GetOtherItems(ctx context.Context) (*SharedResp, error) {
	return nil, nil
}

type SharedVisibility string

const (
	SharedVisibilityPublic  SharedVisibility = "public"
	SharedVisibilityPrivate SharedVisibility = "private"
	SharedVisibilityFriends SharedVisibility = "friends"
)

func SharedVisibilityValues() []SharedVisibility {
	return []SharedVisibility{SharedVisibilityPublic, SharedVisibilityPrivate, SharedVisibilityFriends}
}

func TestRegisterEnum_SharedFile(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&SharedEnumGroupA{})
	registry.Register(&SharedEnumGroupB{})
	registry.RegisterEnum(SharedVisibilityValues())

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Shared enum should be in the per-package shared file
	sharedContent, ok := files["aprot.ts"]
	if !ok {
		t.Fatalf("Expected aprot.ts for shared enums, got files: %v", mapKeys(files))
	}

	if !strings.Contains(sharedContent, "SharedVisibility") {
		t.Error("Expected SharedVisibility in aprot.ts")
	}
	if !strings.Contains(sharedContent, `Public: "public"`) {
		t.Errorf("Expected Public enum value in aprot.ts, got:\n%s", sharedContent)
	}
	if !strings.Contains(sharedContent, `Private: "private"`) {
		t.Errorf("Expected Private enum value in aprot.ts, got:\n%s", sharedContent)
	}
	if !strings.Contains(sharedContent, `Friends: "friends"`) {
		t.Errorf("Expected Friends enum value in aprot.ts, got:\n%s", sharedContent)
	}

	// Shared enum should NOT appear in handler files
	groupAContent := files["shared-enum-group-a.ts"]
	groupBContent := files["shared-enum-group-b.ts"]

	if strings.Contains(groupAContent, "SharedVisibility") {
		t.Error("SharedVisibility should not be defined in shared-enum-group-a.ts")
	}
	if strings.Contains(groupBContent, "SharedVisibility") {
		t.Error("SharedVisibility should not be defined in shared-enum-group-b.ts")
	}
}

func TestRegisterEnum_SingleFile(t *testing.T) {
	// Test that RegisterEnum works with GenerateTo (single-file mode)
	registry := NewRegistry()
	registry.Register(&SharedEnumGroupA{})
	registry.RegisterEnum(SharedVisibilityValues())

	gen := NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "SharedVisibility") {
		t.Error("Expected SharedVisibility in single-file output")
	}
	if !strings.Contains(out, `Public: "public"`) {
		t.Errorf("Expected Public enum value, got:\n%s", out)
	}
}

func TestRegisterEnum_InEnumTypes(t *testing.T) {
	// Verify that RegisterEnum adds to the global enumTypes map for goTypeToTS resolution
	registry := NewRegistry()
	registry.RegisterEnum(SharedVisibilityValues())

	enumInfo := registry.GetEnum(reflect.TypeOf(SharedVisibility("")))
	if enumInfo == nil {
		t.Fatal("Expected SharedVisibility in enumTypes after RegisterEnum")
	}
	if enumInfo.Name != "SharedVisibility" {
		t.Errorf("Name = %q, want SharedVisibility", enumInfo.Name)
	}
	if len(enumInfo.Values) != 3 {
		t.Errorf("Values count = %d, want 3", len(enumInfo.Values))
	}
}

func TestRegisterEnum_SharedEnumsAccessor(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterEnum(SharedVisibilityValues())

	shared := registry.SharedEnums()
	if len(shared) != 1 {
		t.Fatalf("SharedEnums() = %d, want 1", len(shared))
	}
	if shared[0].Name != "SharedVisibility" {
		t.Errorf("Name = %q, want SharedVisibility", shared[0].Name)
	}

	// Also verify Enums() includes shared enums
	all := registry.Enums()
	found := false
	for _, e := range all {
		if e.Name == "SharedVisibility" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Enums() should include shared enums")
	}
}
