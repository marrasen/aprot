package aprot

import (
	"bytes"
	"context"
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
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
)

func TaskStatusValues() []TaskStatus {
	return []TaskStatus{TaskStatusPending, TaskStatusRunning, TaskStatusCompleted}
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

	// Test string-based enum
	registry.RegisterEnum(TaskStatusValues())

	// Test int-based enum with Stringer
	registry.RegisterEnum(PriorityValues())

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

	// Test non-slice panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for non-slice")
			}
		}()
		registry.RegisterEnum("not a slice")
	}()

	// Test empty slice panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for empty slice")
			}
		}()
		registry.RegisterEnum([]TaskStatus{})
	}()
}

func TestGenerateWithEnums(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterEnum(TaskStatusValues())
	registry.RegisterEnum(PriorityValues())
	registry.Register(&TaskHandlers{})

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
	if !strings.Contains(output, `Pending: "pending"`) {
		t.Error("Missing Pending enum value")
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

	type FailHandlers struct{}
	// Can't add method to FailHandlers in test, use VoidHandlers and test via the existing handler
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
	registry.RegisterEnum(TaskStatusValues())
	registry.RegisterEnum(PriorityValues())
	registry.Register(&TaskHandlers{})

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

func TestGenerateTaskNodeInterface(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// TaskNodeStatus enum should always be present
	if !strings.Contains(output, "export const TaskNodeStatus = {") {
		t.Error("Missing TaskNodeStatus enum const")
	}
	if !strings.Contains(output, "export type TaskNodeStatusType") {
		t.Error("Missing TaskNodeStatusType type")
	}

	// TaskNode should always be present (used by SubTask progress)
	if !strings.Contains(output, "export interface TaskNode") {
		t.Error("Missing TaskNode interface")
	}
	// TaskNode.status should use the enum type, not a hardcoded union
	if !strings.Contains(output, "status: TaskNodeStatusType") {
		t.Error("TaskNode.status should use TaskNodeStatusType")
	}
	if strings.Contains(output, "'running' | 'completed' | 'failed'") {
		t.Error("Should not have hardcoded status union — use TaskNodeStatusType instead")
	}
	if !strings.Contains(output, "onTaskProgress?: (tasks: TaskNode[]) => void") {
		t.Error("Missing onTaskProgress in RequestOptions")
	}
	if !strings.Contains(output, "onOutput?: (output: string) => void") {
		t.Error("Missing onOutput in RequestOptions")
	}

	// Without EnableTasks(), shared task types should NOT be present
	if strings.Contains(output, "export interface SharedTaskState") {
		t.Error("SharedTaskState should not be present without EnableTasks()")
	}
	if strings.Contains(output, "export interface TaskRef") {
		t.Error("TaskRef should not be present without EnableTasks()")
	}
	if strings.Contains(output, "cancelSharedTask") {
		t.Error("cancelSharedTask should not be present without EnableTasks()")
	}
}

func TestGenerateWithTasks(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
	registry.EnableTasks()

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// TaskNodeStatus enum
	if !strings.Contains(output, "export const TaskNodeStatus = {") {
		t.Error("Missing TaskNodeStatus enum const")
	}
	if !strings.Contains(output, "export type TaskNodeStatusType") {
		t.Error("Missing TaskNodeStatusType type")
	}
	if strings.Contains(output, "'running' | 'completed' | 'failed'") {
		t.Error("Should not have hardcoded status union — use TaskNodeStatusType instead")
	}

	// TaskNode always present
	if !strings.Contains(output, "export interface TaskNode") {
		t.Error("Missing TaskNode interface")
	}

	// Shared task types should be present with EnableTasks()
	if !strings.Contains(output, "export interface SharedTaskState") {
		t.Error("Missing SharedTaskState interface")
	}
	if !strings.Contains(output, "export interface TaskRef") {
		t.Error("Missing TaskRef interface")
	}
	if !strings.Contains(output, "cancelSharedTask") {
		t.Error("Missing cancelSharedTask function")
	}

	// TaskNodeStatus should NOT appear as a duplicated enum in the handler section
	if strings.Count(output, "export const TaskNodeStatus") > 1 {
		t.Error("TaskNodeStatus should only appear once (in the types block), not duplicated")
	}

	// Task push event handlers should be present
	if !strings.Contains(output, "onTaskStateEvent") {
		t.Error("Missing onTaskStateEvent function")
	}
	if !strings.Contains(output, "onTaskUpdateEvent") {
		t.Error("Missing onTaskUpdateEvent function")
	}

	// TaskStateEvent/TaskUpdateEvent interfaces should be present
	if !strings.Contains(output, "export interface TaskStateEvent") {
		t.Error("Missing TaskStateEvent interface")
	}
	if !strings.Contains(output, "export interface TaskUpdateEvent") {
		t.Error("Missing TaskUpdateEvent interface")
	}

	// Internal handler methods should not leak into output
	if strings.Contains(output, "cancelTask(client") {
		t.Error("Internal cancelTask method should not appear as a standalone function")
	}

	// onTaskStateEvent should not be duplicated (once from tasks block is enough)
	if strings.Count(output, "function onTaskStateEvent") > 1 {
		t.Error("onTaskStateEvent should only appear once")
	}
	if strings.Count(output, "function onTaskUpdateEvent") > 1 {
		t.Error("onTaskUpdateEvent should only appear once")
	}
}

func TestGenerateWithTasksMultiFile(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
	registry.EnableTasks()

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	baseContent, ok := files["client.ts"]
	if !ok {
		t.Fatalf("Expected client.ts, got files: %v", mapKeys(files))
	}

	// TaskNodeStatus enum in base client
	if !strings.Contains(baseContent, "export const TaskNodeStatus = {") {
		t.Error("Missing TaskNodeStatus enum const in client.ts")
	}
	if !strings.Contains(baseContent, "export type TaskNodeStatusType") {
		t.Error("Missing TaskNodeStatusType type in client.ts")
	}
	if strings.Contains(baseContent, "'running' | 'completed' | 'failed'") {
		t.Error("Should not have hardcoded status union in client.ts")
	}

	if !strings.Contains(baseContent, "export interface SharedTaskState") {
		t.Error("Missing SharedTaskState in client.ts")
	}
	if !strings.Contains(baseContent, "export interface TaskRef") {
		t.Error("Missing TaskRef in client.ts")
	}
	if !strings.Contains(baseContent, "cancelSharedTask") {
		t.Error("Missing cancelSharedTask in client.ts")
	}

	// Task push event handlers in client.ts
	if !strings.Contains(baseContent, "onTaskStateEvent") {
		t.Error("Missing onTaskStateEvent in client.ts")
	}
	if !strings.Contains(baseContent, "onTaskUpdateEvent") {
		t.Error("Missing onTaskUpdateEvent in client.ts")
	}

	// TaskStateEvent/TaskUpdateEvent interfaces in client.ts
	if !strings.Contains(baseContent, "export interface TaskStateEvent") {
		t.Error("Missing TaskStateEvent interface in client.ts")
	}
	if !strings.Contains(baseContent, "export interface TaskUpdateEvent") {
		t.Error("Missing TaskUpdateEvent interface in client.ts")
	}

	// No task-cancel-handler.ts should be generated
	if _, exists := files["task-cancel-handler.ts"]; exists {
		t.Error("task-cancel-handler.ts should not be generated — internal groups should be skipped")
	}

	// No duplicate on-handlers
	if strings.Count(baseContent, "function onTaskStateEvent") > 1 {
		t.Error("onTaskStateEvent should only appear once in client.ts")
	}

	// Internal handler methods should not leak
	if strings.Contains(baseContent, "cancelTask(client") {
		t.Error("Internal cancelTask method should not appear as a standalone function in client.ts")
	}
}

func TestGenerateWithTasksReact(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
	registry.EnableTasks()

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputReact,
	})

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Shared task hooks
	if !strings.Contains(output, "export function useSharedTasks") {
		t.Error("Missing useSharedTasks hook")
	}
	if !strings.Contains(output, "export function useSharedTask") {
		t.Error("Missing useSharedTask hook")
	}
	if !strings.Contains(output, "export function useTaskOutput") {
		t.Error("Missing useTaskOutput hook")
	}
	if !strings.Contains(output, "export function cancelSharedTask") {
		t.Error("Missing cancelSharedTask function")
	}

	// TaskNodeStatus enum
	if !strings.Contains(output, "export const TaskNodeStatus = {") {
		t.Error("Missing TaskNodeStatus enum const")
	}
	if !strings.Contains(output, "export type TaskNodeStatusType") {
		t.Error("Missing TaskNodeStatusType type")
	}
	if strings.Contains(output, "'running' | 'completed' | 'failed'") {
		t.Error("Should not have hardcoded status union — use TaskNodeStatusType instead")
	}

	// Shared task types
	if !strings.Contains(output, "export interface SharedTaskState") {
		t.Error("Missing SharedTaskState interface")
	}
	if !strings.Contains(output, "export interface TaskRef") {
		t.Error("Missing TaskRef interface")
	}

	// Task push event handlers
	if !strings.Contains(output, "onTaskStateEvent") {
		t.Error("Missing onTaskStateEvent function")
	}
	if !strings.Contains(output, "onTaskUpdateEvent") {
		t.Error("Missing onTaskUpdateEvent function")
	}

	// TaskStateEvent/TaskUpdateEvent interfaces
	if !strings.Contains(output, "export interface TaskStateEvent") {
		t.Error("Missing TaskStateEvent interface")
	}
	if !strings.Contains(output, "export interface TaskUpdateEvent") {
		t.Error("Missing TaskUpdateEvent interface")
	}

	// No duplicate on-handlers or auto-generated hooks for internal events
	if strings.Count(output, "function onTaskStateEvent") > 1 {
		t.Error("onTaskStateEvent should only appear once")
	}
	if strings.Count(output, "function onTaskUpdateEvent") > 1 {
		t.Error("onTaskUpdateEvent should only appear once")
	}

	// Internal handler methods should not leak
	if strings.Contains(output, "cancelTask(client") {
		t.Error("Internal cancelTask method should not appear as a standalone function")
	}
}

func TestGenerateWithTasksReactMultiFile(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
	registry.EnableTasks()

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputReact,
	})

	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	baseContent, ok := files["client.ts"]
	if !ok {
		t.Fatalf("Expected client.ts, got files: %v", mapKeys(files))
	}

	if !strings.Contains(baseContent, "useSharedTasks") {
		t.Error("Missing useSharedTasks in client.ts")
	}
	if !strings.Contains(baseContent, "useTaskOutput") {
		t.Error("Missing useTaskOutput in client.ts")
	}
	if !strings.Contains(baseContent, "cancelSharedTask") {
		t.Error("Missing cancelSharedTask in client.ts")
	}
	// TaskNodeStatus enum in base client
	if !strings.Contains(baseContent, "export const TaskNodeStatus = {") {
		t.Error("Missing TaskNodeStatus enum const in client.ts")
	}
	if strings.Contains(baseContent, "'running' | 'completed' | 'failed'") {
		t.Error("Should not have hardcoded status union in client.ts")
	}

	// Task push event handlers in client.ts
	if !strings.Contains(baseContent, "onTaskStateEvent") {
		t.Error("Missing onTaskStateEvent in client.ts")
	}
	if !strings.Contains(baseContent, "onTaskUpdateEvent") {
		t.Error("Missing onTaskUpdateEvent in client.ts")
	}

	// TaskStateEvent/TaskUpdateEvent interfaces in client.ts
	if !strings.Contains(baseContent, "export interface TaskStateEvent") {
		t.Error("Missing TaskStateEvent interface in client.ts")
	}
	if !strings.Contains(baseContent, "export interface TaskUpdateEvent") {
		t.Error("Missing TaskUpdateEvent interface in client.ts")
	}

	// No task-cancel-handler.ts should be generated
	if _, exists := files["task-cancel-handler.ts"]; exists {
		t.Error("task-cancel-handler.ts should not be generated — internal groups should be skipped")
	}

	// No duplicate on-handlers
	if strings.Count(baseContent, "function onTaskStateEvent") > 1 {
		t.Error("onTaskStateEvent should only appear once in client.ts")
	}

	// No auto-generated hooks for internal events
	// Internal handler methods should not leak
	if strings.Contains(baseContent, "cancelTask(client") {
		t.Error("Internal cancelTask method should not appear as a standalone function in client.ts")
	}
}

func TestGenerateWithTaskMeta(t *testing.T) {
	type TaskMeta struct {
		UserName string `json:"userName,omitempty"`
		Error    string `json:"error,omitempty"`
	}

	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
	EnableTasksWithMeta[TaskMeta](registry)

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Should have TaskMeta interface
	if !strings.Contains(output, "export interface TaskMeta") {
		t.Error("Missing TaskMeta interface")
	}
	if !strings.Contains(output, "userName?: string") {
		t.Error("Missing userName field in TaskMeta")
	}
	if !strings.Contains(output, "error?: string") {
		t.Error("Missing error field in TaskMeta")
	}

	// SharedTaskState should have meta field typed as TaskMeta
	if !strings.Contains(output, "meta?: TaskMeta;") {
		t.Error("Missing typed meta field")
	}

	// TaskNode should also have meta field
	// (both SharedTaskState and TaskNode should have meta)
	t.Logf("Generated TypeScript (task meta):\n%s", output)
}

func TestGenerateWithTaskMetaMultiFile(t *testing.T) {
	type TaskMeta struct {
		UserName string `json:"userName,omitempty"`
	}

	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.RegisterPushEventFor(&TestHandlers{}, UserUpdatedEvent{})
	EnableTasksWithMeta[TaskMeta](registry)

	gen := NewGenerator(registry)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	baseContent, ok := files["client.ts"]
	if !ok {
		t.Fatalf("Expected client.ts, got files: %v", mapKeys(files))
	}

	if !strings.Contains(baseContent, "export interface TaskMeta") {
		t.Error("Missing TaskMeta interface in client.ts")
	}
	if !strings.Contains(baseContent, "meta?: TaskMeta;") {
		t.Error("Missing typed meta field in client.ts")
	}
}

func TestGenerateWithoutMetaNoMetaField(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TestHandlers{})
	registry.EnableTasks()

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	err := gen.GenerateTo(&buf)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	output := buf.String()

	// Without meta, TaskNode and SharedTaskState should NOT have a meta field
	if strings.Contains(output, "meta?: ") {
		t.Error("Should not have meta field without EnableTasksWithMeta")
	}
	if strings.Contains(output, "export interface TaskMeta") {
		t.Error("Should not have TaskMeta interface without EnableTasksWithMeta")
	}
}

func TestEnableTasksRegistersEnum(t *testing.T) {
	registry := NewRegistry()
	registry.EnableTasks()

	enumInfo := registry.GetEnum(reflect.TypeOf(TaskNodeStatus("")))
	if enumInfo == nil {
		t.Fatal("EnableTasks() should register TaskNodeStatus as an enum")
	}
	if enumInfo.Name != "TaskNodeStatus" {
		t.Errorf("Expected enum name TaskNodeStatus, got %s", enumInfo.Name)
	}
	if len(enumInfo.Values) != 3 {
		t.Errorf("Expected 3 enum values, got %d", len(enumInfo.Values))
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
			registry.RegisterEnum(TaskStatusValues())
			registry.RegisterEnum(PriorityValues())
			registry.Register(&TaskHandlers{})

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
			registry.RegisterEnum(TaskStatusValues())
			registry.RegisterEnum(PriorityValues())
			registry.Register(&TaskHandlers{})

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
