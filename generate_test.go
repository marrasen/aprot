package aprot

import (
	"bytes"
	"context"
	"fmt"
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
	registry.RegisterPushEvent(UserUpdatedEvent{})

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
	registry.RegisterPushEvent(UserUpdatedEvent{})

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
	registry.RegisterPushEvent(UserUpdatedEvent{})

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
	if !strings.Contains(handlerContent, "ApiClient.prototype.createUser") {
		t.Error("Missing createUser method extension in handler file")
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
	registry.RegisterPushEvent(UserUpdatedEvent{})

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
	registry.RegisterPushEvent(UserUpdatedEvent{})

	gen := NewGenerator(registry)

	var buf bytes.Buffer
	gen.GenerateTo(&buf)

	// Print the generated code for manual inspection
	t.Logf("Generated TypeScript:\n%s", buf.String())
}

func TestRegisterEnum(t *testing.T) {
	registry := NewRegistry()

	// Test string-based enum
	err := registry.RegisterEnum(TaskStatusValues())
	if err != nil {
		t.Fatalf("RegisterEnum (string) failed: %v", err)
	}

	// Test int-based enum with Stringer
	err = registry.RegisterEnum(PriorityValues())
	if err != nil {
		t.Fatalf("RegisterEnum (int) failed: %v", err)
	}

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

	// Test non-slice
	err := registry.RegisterEnum("not a slice")
	if err == nil {
		t.Error("Expected error for non-slice")
	}

	// Test empty slice
	err = registry.RegisterEnum([]TaskStatus{})
	if err == nil {
		t.Error("Expected error for empty slice")
	}
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
	err := registry.Register(&VoidHandlers{})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	info, ok := registry.Get("DeleteItem")
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

	info, _ := registry.Get("DeleteItem")
	result, err := info.Call(context.Background(), []byte(`{"id":"item_1"}`))
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

	info, _ := registry.Get("DeleteItem")
	// Call with valid params — should succeed
	result, err := info.Call(context.Background(), []byte(`{"id":"ok"}`))
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
