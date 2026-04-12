package aprot

import (
	"strings"
	"testing"
)

func TestGoTypeToZod(t *testing.T) {
	tests := []struct {
		name        string
		goKind      string
		tsType      string
		validateTag string
		optional    bool
		want        string
	}{
		{"string required min/max", "string", "string", "required,min=3,max=100", false, "z.string().min(1).min(3).max(100)"},
		{"string email", "string", "string", "email", false, "z.string().email()"},
		{"int range", "int", "number", "gte=12,lte=110", false, "z.number().int().min(12).max(110)"},
		{"optional string", "string", "string", "", true, "z.string().optional()"},
		{"uuid", "string", "string", "uuid", false, "z.string().uuid()"},
		{"url", "string", "string", "url", false, "z.string().url()"},
		{"bool no validation", "bool", "boolean", "", false, "z.boolean()"},
		{"float", "float", "number", "gte=0", false, "z.number().min(0)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goTypeToZod(tt.goKind, tt.tsType, tt.validateTag, tt.optional)
			if got != tt.want {
				t.Errorf("goTypeToZod(%q, %q, %q, %v) = %q, want %q", tt.goKind, tt.tsType, tt.validateTag, tt.optional, got, tt.want)
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
