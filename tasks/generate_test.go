package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/marrasen/aprot"
)

// genTestHandler is the minimal handler needed to satisfy the generator
// (at least one registered handler is required for code generation).
type genTestHandler struct{}

func (h *genTestHandler) Ping(ctx context.Context) error { return nil }

// TestGenerateWithTasks verifies single-file vanilla generation with tasks enabled.
// The generator must produce the core task types and the cancelSharedTask function
// but must not include any React hooks.
func TestGenerateWithTasks(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	Enable(registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	mustContain := []struct {
		label string
		want  string
	}{
		{"SharedTaskState interface", "export interface SharedTaskState"},
		{"cancelSharedTask function", "export function cancelSharedTask"},
		{"TaskNodeStatus enum", "export const TaskNodeStatus"},
	}
	for _, tc := range mustContain {
		if !strings.Contains(out, tc.want) {
			t.Errorf("missing %s: expected %q in output", tc.label, tc.want)
		}
	}

	mustNotContain := []struct {
		label string
		want  string
	}{
		{"useSharedTasks React hook", "useSharedTasks"},
		{"useSharedTask React hook", "export function useSharedTask"},
		{"useMyTasks React hook", "useMyTasks"},
		{"useTaskOutput React hook", "useTaskOutput"},
	}
	for _, tc := range mustNotContain {
		if strings.Contains(out, tc.want) {
			t.Errorf("vanilla output must not contain %s", tc.label)
		}
	}
}

// taskRefStarterA and taskRefStarterB are two separate handler groups that both
// return *TaskRef. Because the shared type is used by more than one group, the
// main generator promotes TaskRef to a shared per-package file. That package's
// short name is "tasks", which collides with the reserved task-runtime tasks.ts
// — the convenience hook overwrites tasks.ts wholesale, so before the fix the
// TaskRef declaration was dropped, leaving every consumer with a dangling
// `import type { TaskRef } from './tasks'`.
type taskRefStarterA struct{}

func (h *taskRefStarterA) StartA(ctx context.Context) (*TaskRef, error) { return &TaskRef{}, nil }

type taskRefStarterB struct{}

func (h *taskRefStarterB) StartB(ctx context.Context) (*TaskRef, error) { return &TaskRef{}, nil }

// TestGenerateSharedTaskRefFileRuntimeCollision guards the fix for a shared
// tasks-package type colliding with the reserved task-runtime tasks.ts. TaskRef
// must be declared in exactly one file, that file must not be the overwritten
// tasks.ts, and every import of TaskRef must resolve to the defining file.
func TestGenerateSharedTaskRefFileRuntimeCollision(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&taskRefStarterA{})
	registry.Register(&taskRefStarterB{})
	Enable(registry)

	results, err := aprot.NewGenerator(registry).Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	defFiles := make([]string, 0)
	for name, content := range results {
		if strings.Contains(content, "export interface TaskRef {") {
			defFiles = append(defFiles, name)
		}
	}
	if len(defFiles) != 1 {
		names := make([]string, 0, len(results))
		for name := range results {
			names = append(names, name)
		}
		t.Fatalf("expected TaskRef defined in exactly one file, found in %v (files: %v)", defFiles, names)
	}
	if defFiles[0] == "tasks.ts" {
		t.Fatalf("TaskRef declared in tasks.ts, which the task-runtime hook overwrites wholesale")
	}
	defModule := "./" + strings.TrimSuffix(defFiles[0], ".ts")

	// Every import of TaskRef must resolve to the defining module — no dangling
	// import, no file importing the type from itself.
	for name, content := range results {
		selfModule := "./" + strings.TrimSuffix(name, ".ts")
		for _, line := range strings.Split(content, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "import") || !strings.Contains(line, "TaskRef") {
				continue
			}
			from := "from '"
			i := strings.LastIndex(line, from)
			if i < 0 {
				continue
			}
			module := line[i+len(from):]
			if j := strings.IndexByte(module, '\''); j >= 0 {
				module = module[:j]
			}
			if module == selfModule {
				t.Errorf("%s imports TaskRef from itself (%q)", name, module)
			}
			if module != defModule {
				t.Errorf("%s imports TaskRef from %q, but it is defined in %q", name, module, defModule)
			}
		}
	}
}

// TestGenerateWithTasksMultiFile verifies multi-file generation with tasks enabled.
// The generator must create a tasks.ts file with task convenience code and must
// not modify client.ts with any task-specific code.
func TestGenerateWithTasksMultiFile(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	Enable(registry)

	gen := aprot.NewGenerator(registry)
	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	tasksCode, ok := results["tasks.ts"]
	if !ok {
		t.Fatal("tasks.ts was not created by multi-file generation")
	}

	mustContain := []struct {
		label string
		want  string
	}{
		{"cancelSharedTask function", "export function cancelSharedTask"},
		{"import from ./client", "from './client'"},
		{"import from ./tasks-handler", "from './tasks-handler'"},
	}
	for _, tc := range mustContain {
		if !strings.Contains(tasksCode, tc.want) {
			t.Errorf("tasks.ts missing %s: expected %q", tc.label, tc.want)
		}
	}

	// client.ts must not have task convenience code appended to it
	clientCode := results["client.ts"]
	if strings.Contains(clientCode, "cancelSharedTask") {
		t.Error("client.ts must not contain cancelSharedTask in multi-file mode; it should be in tasks.ts only")
	}
}

// TestGenerateWithTasksReact verifies single-file React generation with tasks enabled.
// All shared-task hooks must be present in the output.
func TestGenerateWithTasksReact(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	Enable(registry)

	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		Mode: aprot.OutputReact,
	})
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	mustContain := []struct {
		label string
		want  string
	}{
		{"cancelSharedTask function", "cancelSharedTask"},
		{"useSharedTasks hook", "export function useSharedTasks"},
		{"useSharedTask hook", "export function useSharedTask"},
		{"useMyTasks hook", "export function useMyTasks"},
		{"useTaskOutput hook", "export function useTaskOutput"},
	}
	for _, tc := range mustContain {
		if !strings.Contains(out, tc.want) {
			t.Errorf("React output missing %s: expected %q", tc.label, tc.want)
		}
	}
}

// TestGenerateWithTasksReactMultiFile verifies multi-file React generation with tasks enabled.
// The tasks.ts file must contain React imports and all shared-task hooks.
func TestGenerateWithTasksReactMultiFile(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	Enable(registry)

	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		Mode: aprot.OutputReact,
	})
	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	tasksCode, ok := results["tasks.ts"]
	if !ok {
		t.Fatal("tasks.ts was not created by multi-file React generation")
	}

	mustContain := []struct {
		label string
		want  string
	}{
		{"React imports", "import { useState, useEffect, useCallback, useSyncExternalStore } from 'react'"},
		{"useApiClient import", "import { useApiClient } from './client'"},
		{"cancelSharedTask function", "export function cancelSharedTask"},
		{"useSharedTasks hook", "export function useSharedTasks"},
		{"useSharedTask hook", "export function useSharedTask"},
		{"useMyTasks hook", "export function useMyTasks"},
		{"useTaskOutput hook", "export function useTaskOutput"},
	}
	for _, tc := range mustContain {
		if !strings.Contains(tasksCode, tc.want) {
			t.Errorf("tasks.ts missing %s: expected %q", tc.label, tc.want)
		}
	}
}

// TaskMeta is the typed metadata struct used in meta-related tests.
type TaskMeta struct {
	UserName string `json:"userName,omitempty"`
	Error    string `json:"error,omitempty"`
}

// TestGenerateWithTasksMeta verifies single-file generation with typed metadata.
// The output must include a TypeScript interface that mirrors the Go struct shape,
// with optional fields for json-tagged fields marked omitempty.
func TestGenerateWithTasksMeta(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[TaskMeta](registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	mustContain := []struct {
		label string
		want  string
	}{
		{"TaskMeta interface declaration", "interface TaskMeta"},
		{"userName optional field", "userName?: string"},
		{"error optional field", "error?: string"},
	}
	for _, tc := range mustContain {
		if !strings.Contains(out, tc.want) {
			t.Errorf("missing %s: expected %q in output", tc.label, tc.want)
		}
	}
}

// TestGenerateWithTasksMetaMultiFile verifies multi-file generation with typed
// metadata. The TaskMeta interface is declared in tasks-handler.ts — where the
// meta fields reference it — and re-exported from tasks.ts so pre-existing
// imports keep resolving.
func TestGenerateWithTasksMetaMultiFile(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[TaskMeta](registry)

	gen := aprot.NewGenerator(registry)
	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	handlerCode, ok := results["tasks-handler.ts"]
	if !ok {
		t.Fatal("tasks-handler.ts was not created by multi-file generation")
	}

	mustContain := []struct {
		label string
		want  string
	}{
		{"TaskMeta interface declaration", "export interface TaskMeta"},
		{"userName optional field", "userName?: string"},
		{"error optional field", "error?: string"},
		{"typed meta field", "meta?: TaskMeta"},
	}
	for _, tc := range mustContain {
		if !strings.Contains(handlerCode, tc.want) {
			t.Errorf("tasks-handler.ts missing %s: expected %q", tc.label, tc.want)
		}
	}
	if strings.Contains(handlerCode, "meta?: any") || strings.Contains(handlerCode, "meta?: unknown") {
		t.Error("meta fields must use the registered TaskMeta type, not any/unknown")
	}

	tasksCode, ok := results["tasks.ts"]
	if !ok {
		t.Fatal("tasks.ts was not created by multi-file generation")
	}
	if !strings.Contains(tasksCode, "export type { TaskMeta } from './tasks-handler';") {
		t.Error("tasks.ts must re-export TaskMeta from tasks-handler for backward compatibility")
	}
}

// TestGenerateWithoutMetaHasAnyMetaField verifies that when Enable (not EnableWithMeta)
// is called, the SharedTaskState.meta field is typed as 'any' in the generated output,
// meaning no specific meta interface is generated.
func TestGenerateWithoutMetaHasAnyMetaField(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	Enable(registry) // no meta type

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// The meta field on SharedTaskState must use any (or unknown) since no typed meta was provided
	if !strings.Contains(out, "meta?: any") && !strings.Contains(out, "meta?: unknown") {
		t.Error("expected SharedTaskState.meta field to be typed as 'any' or 'unknown' when no meta type is registered")
	}

	// No specific meta interface should be generated
	if strings.Contains(out, "interface TaskMeta") {
		t.Error("output must not contain a TaskMeta interface when Enable (not EnableWithMeta) is used")
	}
}

// TestEnableRegistersEnum verifies that Enable registers the TaskNodeStatus enum
// with the registry so it is accessible via GetEnum reflection lookup.
func TestEnableRegistersEnum(t *testing.T) {
	registry := aprot.NewRegistry()
	Enable(registry)

	enumInfo := registry.GetEnum(reflect.TypeOf(TaskNodeStatus("")))
	if enumInfo == nil {
		t.Fatal("TaskNodeStatus enum was not registered: GetEnum returned nil")
	}

	if enumInfo.Name != "TaskNodeStatus" {
		t.Errorf("expected enum name %q, got %q", "TaskNodeStatus", enumInfo.Name)
	}

	const wantValues = 4
	if len(enumInfo.Values) != wantValues {
		t.Errorf("expected %d enum values, got %d", wantValues, len(enumInfo.Values))
	}

	// Verify all expected status values are present
	wantStatuses := map[string]bool{
		"created":   false,
		"running":   false,
		"completed": false,
		"failed":    false,
	}
	for _, v := range enumInfo.Values {
		val, ok := v.Value.(string)
		if !ok {
			t.Errorf("enum value %q has non-string value type %T", v.Name, v.Value)
			continue
		}
		if _, known := wantStatuses[val]; known {
			wantStatuses[val] = true
		} else {
			t.Errorf("unexpected enum value: %q", val)
		}
	}
	for status, found := range wantStatuses {
		if !found {
			t.Errorf("missing expected enum value: %q", status)
		}
	}
}

// CustomID is a type with a custom JSON marshaler that produces a string.
type CustomID [16]byte

func (c CustomID) MarshalJSON() ([]byte, error) {
	return json.Marshal("custom-id-value")
}

// MetaWithCustomMarshaler uses a custom marshaler field in the task meta type.
type MetaWithCustomMarshaler struct {
	RequestID CustomID `json:"requestId"`
	Label     string   `json:"label"`
}

func TestGenerateWithMetaCustomMarshal(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[MetaWithCustomMarshaler](registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// Extract just the MetaWithCustomMarshaler interface from the output
	metaStart := strings.Index(out, "interface MetaWithCustomMarshaler")
	if metaStart == -1 {
		t.Fatal("MetaWithCustomMarshaler interface not found in output")
	}
	metaBlock := out[metaStart:]
	// Find the closing brace of the interface
	braceEnd := strings.Index(metaBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for MetaWithCustomMarshaler")
	}
	metaBlock = metaBlock[:braceEnd+1]

	// The requestId field should be typed as "string", not as "any"
	if !strings.Contains(metaBlock, "requestId: string") {
		t.Errorf("expected MetaWithCustomMarshaler.requestId to be 'string' (from CustomID marshaler), got:\n%s", metaBlock)
	}
	// CustomID should not generate its own interface
	if strings.Contains(out, "interface CustomID") {
		t.Error("CustomID should not generate an interface — it marshals to string")
	}
	// label field should still be string
	if !strings.Contains(metaBlock, "label: string") {
		t.Error("expected MetaWithCustomMarshaler.label to be 'string'")
	}
}

// MetaNestedInfo is a plain struct with no custom marshaler.
type MetaNestedInfo struct {
	Source   string `json:"source"`
	Priority int    `json:"priority"`
}

// MetaWithMixedFields has both a plain struct field and a custom marshaler field.
type MetaWithMixedFields struct {
	Info      MetaNestedInfo   `json:"info"`
	Items     []MetaNestedInfo `json:"items"`
	RequestID CustomID         `json:"requestId"`
}

func TestGenerateWithMetaMixedFields(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[MetaWithMixedFields](registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// MetaNestedInfo (non-marshaler struct) MUST generate an interface
	if !strings.Contains(out, "interface MetaNestedInfo") {
		t.Error("expected MetaNestedInfo interface to be generated (plain struct used as field type)")
	}

	// Extract MetaWithMixedFields interface block
	metaStart := strings.Index(out, "interface MetaWithMixedFields")
	if metaStart == -1 {
		t.Fatal("MetaWithMixedFields interface not found in output")
	}
	metaBlock := out[metaStart:]
	braceEnd := strings.Index(metaBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for MetaWithMixedFields")
	}
	metaBlock = metaBlock[:braceEnd+1]

	// info field should resolve to MetaNestedInfo, not any
	if !strings.Contains(metaBlock, "info: MetaNestedInfo") {
		t.Errorf("expected MetaWithMixedFields.info to be 'MetaNestedInfo', got:\n%s", metaBlock)
	}
	// items field should resolve to MetaNestedInfo[], not any[]
	if !strings.Contains(metaBlock, "items: MetaNestedInfo[]") {
		t.Errorf("expected MetaWithMixedFields.items to be 'MetaNestedInfo[]', got:\n%s", metaBlock)
	}
	// requestId field should resolve to string (CustomID marshaler)
	if !strings.Contains(metaBlock, "requestId: string") {
		t.Errorf("expected MetaWithMixedFields.requestId to be 'string' (from CustomID marshaler), got:\n%s", metaBlock)
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

// MetaWithWrappedSlice uses NonNilSlice wrapper fields in the task meta type.
type MetaWithWrappedSlice struct {
	Items     NonNilSlice[MetaNestedInfo] `json:"items"`
	Tags      NonNilSlice[string]         `json:"tags"`
	RequestID CustomID                    `json:"requestId"`
}

func TestGenerateWithMetaSliceMarshalerWrapper(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[MetaWithWrappedSlice](registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// MetaNestedInfo MUST generate an interface even when only used inside NonNilSlice
	if !strings.Contains(out, "interface MetaNestedInfo") {
		t.Error("expected MetaNestedInfo interface to be generated (used as element type in NonNilSlice)")
	}

	// Extract MetaWithWrappedSlice interface block
	metaStart := strings.Index(out, "interface MetaWithWrappedSlice")
	if metaStart == -1 {
		t.Fatal("MetaWithWrappedSlice interface not found in output")
	}
	metaBlock := out[metaStart:]
	braceEnd := strings.Index(metaBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for MetaWithWrappedSlice")
	}
	metaBlock = metaBlock[:braceEnd+1]

	// items field: NonNilSlice[MetaNestedInfo] should resolve to MetaNestedInfo[], not any[]
	if !strings.Contains(metaBlock, "items: MetaNestedInfo[]") {
		t.Errorf("expected MetaWithWrappedSlice.items to be 'MetaNestedInfo[]', got:\n%s", metaBlock)
	}
	// tags field: NonNilSlice[string] should resolve to string[], not any[]
	if !strings.Contains(metaBlock, "tags: string[]") {
		t.Errorf("expected MetaWithWrappedSlice.tags to be 'string[]', got:\n%s", metaBlock)
	}
	// requestId field should still resolve to string (CustomID marshaler)
	if !strings.Contains(metaBlock, "requestId: string") {
		t.Errorf("expected MetaWithWrappedSlice.requestId to be 'string', got:\n%s", metaBlock)
	}
}

// MetaWithFixedArray uses fixed-size array fields in the task meta type.
type MetaWithFixedArray struct {
	WhiteBalance [4]float64 `json:"whiteBalance"`
	Matrix       [2][2]int  `json:"matrix"`
	Simple       [3]string  `json:"simple"`
	Hash         [32]byte   `json:"hash"`
	Big          [20]int    `json:"big"`
}

func TestGenerateWithMetaFixedArray(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[MetaWithFixedArray](registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	// Extract MetaWithFixedArray interface block
	metaStart := strings.Index(out, "interface MetaWithFixedArray")
	if metaStart == -1 {
		t.Fatal("MetaWithFixedArray interface not found in output")
	}
	metaBlock := out[metaStart:]
	braceEnd := strings.Index(metaBlock, "}")
	if braceEnd == -1 {
		t.Fatal("could not find closing brace for MetaWithFixedArray")
	}
	metaBlock = metaBlock[:braceEnd+1]

	// whiteBalance: [4]float64 should resolve to [number, number, number, number] tuple
	if !strings.Contains(metaBlock, "whiteBalance: [number, number, number, number]") {
		t.Errorf("expected MetaWithFixedArray.whiteBalance to be '[number, number, number, number]' tuple, got:\n%s", metaBlock)
	}

	// matrix: [2][2]int should resolve to [[number, number], [number, number]] tuple
	if !strings.Contains(metaBlock, "matrix: [[number, number], [number, number]]") {
		t.Errorf("expected MetaWithFixedArray.matrix to be '[[number, number], [number, number]]' tuple, got:\n%s", metaBlock)
	}

	// simple: [3]string should resolve to [string, string, string] tuple
	if !strings.Contains(metaBlock, "simple: [string, string, string]") {
		t.Errorf("expected MetaWithFixedArray.simple to be '[string, string, string]' tuple, got:\n%s", metaBlock)
	}

	// hash: [32]byte is base64-encoded by jsonv2 → string, not a number tuple
	if !strings.Contains(metaBlock, "hash: string") {
		t.Errorf("expected MetaWithFixedArray.hash to be 'string' (base64 wire shape), got:\n%s", metaBlock)
	}

	// big: [20]int is above the tuple cap → plain number[]
	if !strings.Contains(metaBlock, "big: number[]") {
		t.Errorf("expected MetaWithFixedArray.big to be 'number[]' (above tuple cap), got:\n%s", metaBlock)
	}
}
