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

func TestGoTypeToZod(t *testing.T) {
	tests := []struct {
		name        string
		goKind      string
		tsType      string
		validateTag string
		optional    bool
		sqlNullKind string
		want        string
	}{
		// Baselines
		{"string required min/max", "string", "string", "required,min=3,max=100", false, "", "z.string().min(3).max(100)"},
		{"string email", "string", "string", "email", false, "", "z.string().email()"},
		{"int range", "int", "number", "gte=12,lte=110", false, "", "z.number().int().min(12).max(110)"},
		{"optional string", "string", "string", "", true, "", "z.string().optional()"},
		{"uuid", "string", "string", "uuid", false, "", "z.string().uuid()"},
		{"url", "string", "string", "url", false, "", "z.string().url()"},
		{"bool no validation", "bool", "boolean", "", false, "", "z.boolean()"},
		{"float", "float", "number", "gte=0", false, "", "z.number().min(0)"},

		// Issue 2: required + explicit min dedup (regression coverage below)
		{"string required alone keeps min(1)", "string", "string", "required", false, "", "z.string().min(1)"},
		{"string required + max keeps min(1)", "string", "string", "required,max=50", false, "", "z.string().min(1).max(50)"},
		{"int required no min(1)", "int", "number", "required,gte=1", false, "", "z.number().int().min(1)"},

		// Issue 1: validate-tag omitempty on strings wraps with empty-literal union
		{"string omitempty url max", "string", "string", "omitempty,url,max=500", false, "", `z.union([z.literal(""), z.string().url().max(500)]).optional()`},
		{"string omitempty alone", "string", "string", "omitempty", false, "", `z.union([z.literal(""), z.string()]).optional()`},
		{"string omitempty email", "string", "string", "omitempty,email", false, "", `z.union([z.literal(""), z.string().email()]).optional()`},
		// Non-string kinds just get .optional(), no union wrap
		{"int omitempty gte", "int", "number", "omitempty,gte=1", false, "", "z.number().int().min(1).optional()"},
		{"bool omitempty", "bool", "boolean", "omitempty", false, "", "z.boolean().optional()"},
		// json-omitempty (already sets optional=true) on a string without validate omitempty keeps plain .optional()
		{"pointer string no validate omitempty", "string", "string", "", true, "", "z.string().optional()"},

		// Issue 3: SQLNullKind drives nullable base + constraints
		{"sql NullString", "struct", "string | null", "", false, "string", "z.string().nullable()"},
		{"sql NullString with max", "struct", "string | null", "max=10", false, "string", "z.string().max(10).nullable()"},
		{"sql NullInt64", "struct", "number | null", "", false, "int", "z.number().int().nullable()"},
		{"sql NullInt64 with range", "struct", "number | null", "gte=0,lte=100", false, "int", "z.number().int().min(0).max(100).nullable()"},
		{"sql NullFloat64", "struct", "number | null", "", false, "float", "z.number().nullable()"},
		{"sql NullBool", "struct", "boolean | null", "", false, "bool", "z.boolean().nullable()"},
		// Regression: plain struct with no SQLNullKind still falls through to z.any()
		{"plain struct any", "struct", "SomeStruct", "", false, "", "z.any()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goTypeToZod(tt.goKind, tt.tsType, tt.validateTag, tt.optional, tt.sqlNullKind)
			if got != tt.want {
				t.Errorf("goTypeToZod(%q, %q, %q, %v, %q) = %q, want %q", tt.goKind, tt.tsType, tt.validateTag, tt.optional, tt.sqlNullKind, got, tt.want)
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
