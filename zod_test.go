package aprot

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// PilotQuirksRequest exercises the three Zod codegen fixes from issue #163:
// Issue 1 (validate omitempty), Issue 2 (required+min dedup), Issue 3 (sql.Null*).
type PilotQuirksRequest struct {
	Name     string         `json:"name"     validate:"required,min=2,max=50"` // Issue 2: no leading .min(1)
	ImageURL string         `json:"imageUrl" validate:"omitempty,url,max=500"` // Issue 1: empty-literal union
	ParentID sql.NullInt64  `json:"parentId"`                                  // Issue 3: nullable number
	Bio      sql.NullString `json:"bio"      validate:"max=500"`               // Issue 3 + constraint
}

type PilotQuirksHandlers struct{}

func (h *PilotQuirksHandlers) Submit(ctx context.Context, req *PilotQuirksRequest) error {
	return nil
}

// SliceElemRequest exercises issue #169: parent schemas should substitute
// element schemas into z.array(...) / z.record(...) instead of falling through
// to z.any(). The parent has at least one validate tag so it gets a schema;
// EventTag also has a validate tag so its leaf schema is generated and the
// parent's Tags field can reference it.
type SliceElemRequest struct {
	Title    string              `json:"title"    validate:"required,min=1,max=100"`
	Aliases  []string            `json:"aliases"  validate:"max=10"` // primitive slice
	Counts   []int               `json:"counts"`                     // primitive slice, no constraint on parent
	Tags     []EventTag          `json:"tags"     validate:"dive"`   // struct slice → element schema
	Metadata map[string]EventTag `json:"metadata"`                   // struct map → element schema
}

type EventTag struct {
	Name  string `json:"name"  validate:"required,min=1,max=50"`
	Color string `json:"color" validate:"omitempty,max=7"`
}

type SliceElemHandlers struct{}

func (h *SliceElemHandlers) Submit(ctx context.Context, req *SliceElemRequest) error {
	return nil
}

func TestFieldToZod(t *testing.T) {
	// knownSchemas for the slice/map element-substitution cases (#169).
	known := map[string]bool{
		"EventLinkInput": true,
		"NestedItem":     true,
	}

	tests := []struct {
		name  string
		field fieldData
		want  string
	}{
		// Baselines
		{"string required min/max", fieldData{GoType: "string", Type: "string", ValidateTag: "required,min=3,max=100"}, "z.string().min(3).max(100)"},
		{"string email", fieldData{GoType: "string", Type: "string", ValidateTag: "email"}, "z.string().email()"},
		{"int range", fieldData{GoType: "int", Type: "number", ValidateTag: "gte=12,lte=110"}, "z.number().int().min(12).max(110)"},
		{"optional string", fieldData{GoType: "string", Type: "string", Optional: true}, "z.string().optional()"},
		{"uuid", fieldData{GoType: "string", Type: "string", ValidateTag: "uuid"}, "z.string().uuid()"},
		{"url", fieldData{GoType: "string", Type: "string", ValidateTag: "url"}, "z.string().url()"},
		{"bool no validation", fieldData{GoType: "bool", Type: "boolean"}, "z.boolean()"},
		{"float", fieldData{GoType: "float", Type: "number", ValidateTag: "gte=0"}, "z.number().min(0)"},

		// Issue 2 (#163): required + explicit min dedup
		{"string required alone keeps min(1)", fieldData{GoType: "string", Type: "string", ValidateTag: "required"}, "z.string().min(1)"},
		{"string required + max keeps min(1)", fieldData{GoType: "string", Type: "string", ValidateTag: "required,max=50"}, "z.string().min(1).max(50)"},
		{"int required no min(1)", fieldData{GoType: "int", Type: "number", ValidateTag: "required,gte=1"}, "z.number().int().min(1)"},

		// Issue 1 (#163): validate-tag omitempty on strings wraps with empty-literal union
		{"string omitempty url max", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty,url,max=500"}, `z.union([z.literal(""), z.string().url().max(500)]).optional()`},
		{"string omitempty alone", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty"}, `z.union([z.literal(""), z.string()]).optional()`},
		{"string omitempty email", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty,email"}, `z.union([z.literal(""), z.string().email()]).optional()`},
		// Non-string kinds just get .optional(), no union wrap
		{"int omitempty gte", fieldData{GoType: "int", Type: "number", ValidateTag: "omitempty,gte=1"}, "z.number().int().min(1).optional()"},
		{"bool omitempty", fieldData{GoType: "bool", Type: "boolean", ValidateTag: "omitempty"}, "z.boolean().optional()"},
		// json-omitempty (already sets optional=true) on a string without validate omitempty keeps plain .optional()
		{"pointer string no validate omitempty", fieldData{GoType: "string", Type: "string", Optional: true}, "z.string().optional()"},

		// Issue 3 (#163): SQLNullKind drives nullable base + constraints
		{"sql NullString", fieldData{GoType: "struct", Type: "string | null", SQLNullKind: "string"}, "z.string().nullable()"},
		{"sql NullString with max", fieldData{GoType: "struct", Type: "string | null", ValidateTag: "max=10", SQLNullKind: "string"}, "z.string().max(10).nullable()"},
		{"sql NullInt64", fieldData{GoType: "struct", Type: "number | null", SQLNullKind: "int"}, "z.number().int().nullable()"},
		{"sql NullInt64 with range", fieldData{GoType: "struct", Type: "number | null", ValidateTag: "gte=0,lte=100", SQLNullKind: "int"}, "z.number().int().min(0).max(100).nullable()"},
		{"sql NullFloat64", fieldData{GoType: "struct", Type: "number | null", SQLNullKind: "float"}, "z.number().nullable()"},
		{"sql NullBool", fieldData{GoType: "struct", Type: "boolean | null", SQLNullKind: "bool"}, "z.boolean().nullable()"},
		// Regression: plain struct with no SQLNullKind still falls through to z.any()
		{"plain struct any", fieldData{GoType: "struct", Type: "SomeStruct"}, "z.any()"},

		// Issue #169: slice element substitution
		{"slice of string", fieldData{GoType: "slice", Type: "string[]", ElemGoKind: "string"}, "z.array(z.string())"},
		{"slice of int", fieldData{GoType: "slice", Type: "number[]", ElemGoKind: "int"}, "z.array(z.number().int())"},
		{"slice of float", fieldData{GoType: "slice", Type: "number[]", ElemGoKind: "float"}, "z.array(z.number())"},
		{"slice of bool", fieldData{GoType: "slice", Type: "boolean[]", ElemGoKind: "bool"}, "z.array(z.boolean())"},
		{"slice of known struct", fieldData{GoType: "slice", Type: "EventLinkInput[]", ElemGoKind: "struct", ElemTypeName: "EventLinkInput"}, "z.array(EventLinkInputSchema)"},
		{"slice of known struct with constraint", fieldData{GoType: "slice", Type: "EventLinkInput[]", ValidateTag: "dive,required", ElemGoKind: "struct", ElemTypeName: "EventLinkInput"}, "z.array(EventLinkInputSchema)"},
		{"slice of unknown struct", fieldData{GoType: "slice", Type: "UnknownThing[]", ElemGoKind: "struct", ElemTypeName: "UnknownThing"}, "z.array(z.any())"},
		// Regression: slice with no element info (e.g., recursive types) still falls through
		{"slice no elem info", fieldData{GoType: "slice", Type: "any[]"}, "z.array(z.any())"},

		// Issue #169: map element substitution
		{"map of string", fieldData{GoType: "map", Type: "Record<string, string>", ElemGoKind: "string"}, "z.record(z.string(), z.string())"},
		{"map of int", fieldData{GoType: "map", Type: "Record<string, number>", ElemGoKind: "int"}, "z.record(z.string(), z.number().int())"},
		{"map of known struct", fieldData{GoType: "map", Type: "Record<string, NestedItem>", ElemGoKind: "struct", ElemTypeName: "NestedItem"}, "z.record(z.string(), NestedItemSchema)"},
		{"map no elem info", fieldData{GoType: "map", Type: "Record<string, any>"}, "z.record(z.string(), z.any())"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fieldToZod(tt.field, known)
			if got != tt.want {
				t.Errorf("fieldToZod(%+v) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

func TestBuildZodSchemas(t *testing.T) {
	interfaces := []interfaceData{
		{
			Name: "SetAgeRequest",
			Fields: []fieldData{
				{Name: "userId", Type: "number", GoType: "int", ValidateTag: "required,min=1"},
				{Name: "age", Type: "number", GoType: "int", ValidateTag: "required,gte=12,lte=110"},
			},
		},
		{
			Name: "NoValidation",
			Fields: []fieldData{
				{Name: "name", Type: "string", GoType: "string"},
			},
		},
	}

	schemas := buildZodSchemas(interfaces)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema (only validated structs), got %d", len(schemas))
	}
	if schemas[0].Name != "SetAgeRequest" {
		t.Errorf("expected schema name 'SetAgeRequest', got %q", schemas[0].Name)
	}
	if len(schemas[0].Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(schemas[0].Fields))
	}
}

func TestZodGeneration_Integration(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ValidatedHandlers{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputVanilla,
		Zod:  true,
	})

	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Check that a schema file was generated
	var schemaFile string
	for name, content := range results {
		if strings.HasSuffix(name, ".schema.ts") {
			schemaFile = content
			break
		}
	}

	if schemaFile == "" {
		t.Fatal("expected a .schema.ts file to be generated")
	}

	// Verify key content
	if !strings.Contains(schemaFile, "import { z } from 'zod'") {
		t.Error("schema file should import zod")
	}
	if !strings.Contains(schemaFile, "SetAgeRequestSchema") {
		t.Error("schema file should contain SetAgeRequestSchema")
	}
	if !strings.Contains(schemaFile, "z.number().int()") {
		t.Error("schema file should contain z.number().int() for int fields")
	}
}

func TestZodGeneration_SliceAndMapElements(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&SliceElemHandlers{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputVanilla,
		Zod:  true,
	})

	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	var schemaFile string
	for name, content := range results {
		if strings.HasSuffix(name, ".schema.ts") {
			schemaFile = content
			break
		}
	}
	if schemaFile == "" {
		t.Fatal("expected a .schema.ts file to be generated")
	}

	expectations := []struct {
		substr string
		desc   string
	}{
		// Primitive slice → typed array
		{"aliases: z.array(z.string()).max(10)", "Aliases: typed string array with parent constraint"},
		{"counts: z.array(z.number().int())", "Counts: typed int array"},
		// Struct slice with known schema → element schema reference
		{"tags: z.array(EventTagSchema)", "Tags: substituted with EventTagSchema"},
		// Struct map with known schema → element schema reference
		{"metadata: z.record(z.string(), EventTagSchema)", "Metadata: record with substituted element"},
		// Leaf schema must also be emitted
		{"EventTagSchema", "EventTag leaf schema is generated"},
	}
	for _, exp := range expectations {
		if !strings.Contains(schemaFile, exp.substr) {
			t.Errorf("%s — expected schema to contain %q\n---\n%s", exp.desc, exp.substr, schemaFile)
		}
	}

	// Regression: parent slice fields should never fall through to z.array(z.any())
	// when an element type is known.
	if strings.Contains(schemaFile, "tags: z.array(z.any())") {
		t.Error("Tags should not fall through to z.array(z.any()) when EventTagSchema exists")
	}
	if strings.Contains(schemaFile, "aliases: z.array(z.any())") {
		t.Error("Aliases should not fall through to z.array(z.any()) for primitive elements")
	}
}

func TestZodGeneration_PilotQuirks(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&PilotQuirksHandlers{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode: OutputVanilla,
		Zod:  true,
	})

	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	var schemaFile string
	for name, content := range results {
		if strings.HasSuffix(name, ".schema.ts") {
			schemaFile = content
			break
		}
	}
	if schemaFile == "" {
		t.Fatal("expected a .schema.ts file to be generated")
	}

	expectations := []struct {
		substr string
		desc   string
	}{
		// Issue 2: required,min=2,max=50 emits .min(2).max(50), not .min(1).min(2).max(50)
		{"name: z.string().min(2).max(50)", "Name field: no redundant .min(1)"},
		// Issue 1: omitempty,url,max=500 on string wraps in empty-literal union and appends .optional()
		{`imageUrl: z.union([z.literal(""), z.string().url().max(500)]).optional()`, "ImageURL: empty-literal union + optional"},
		// Issue 3: sql.NullInt64 → z.number().int().nullable()
		{"parentId: z.number().int().nullable()", "ParentID: nullable int base"},
		// Issue 3 + existing constraint: sql.NullString with validate max=500 → z.string().max(500).nullable()
		{"bio: z.string().max(500).nullable()", "Bio: sql.NullString with constraint + nullable"},
	}
	for _, exp := range expectations {
		if !strings.Contains(schemaFile, exp.substr) {
			t.Errorf("%s — expected schema to contain %q\n---\n%s", exp.desc, exp.substr, schemaFile)
		}
	}

	// Regression: no .min(1).min(2) sequence anywhere (Issue 2 fix applies globally)
	if strings.Contains(schemaFile, ".min(1).min(2)") {
		t.Error("schema should not emit redundant .min(1) before explicit .min(N)")
	}
	// Regression: no .any() fallthrough for sql.Null fields (Issue 3 fix applies)
	if strings.Contains(schemaFile, "parentId: z.any()") || strings.Contains(schemaFile, "bio: z.any()") {
		t.Error("sql.Null* fields should not fall through to z.any()")
	}
}
