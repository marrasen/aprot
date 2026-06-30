package aprot

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// stringEnumFixture builds an EnumInfo for unit tests that exercise enum
// Zod codegen without going through reflect-based registration. Type is
// left nil — fieldToZod doesn't consult it.
func stringEnumFixture(name string, values ...string) *EnumInfo {
	info := &EnumInfo{Name: name, IsString: true}
	for _, v := range values {
		info.Values = append(info.Values, EnumValueInfo{Name: v, Value: v})
	}
	return info
}

// intEnumFixture builds an int-based EnumInfo for unit tests.
func intEnumFixture(name string, values ...int64) *EnumInfo {
	info := &EnumInfo{Name: name, IsString: false}
	for i, v := range values {
		info.Values = append(info.Values, EnumValueInfo{Name: fmt.Sprintf("V%d", i), Value: v})
	}
	return info
}

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

		// Issue 1 (#163): validate-tag omitempty on strings wraps with empty-literal union.
		// Issue #178: validate-omitempty alone does NOT force .optional() — it only tells
		// the Go validator to skip rules on the zero value. The wire still always carries
		// the field, matching the TS interface generator's view. Optionality is driven by
		// f.Optional (pointer / json:,omitempty), which isOptional also uses.
		{"string omitempty url max", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty,url,max=500"}, `z.union([z.literal(""), z.string().url().max(500)])`},
		{"string omitempty alone", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty"}, `z.union([z.literal(""), z.string()])`},
		{"string omitempty email", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty,email"}, `z.union([z.literal(""), z.string().email()])`},
		// Non-string kinds: no union wrap, no .optional() unless f.Optional is set separately.
		{"int omitempty gte", fieldData{GoType: "int", Type: "number", ValidateTag: "omitempty,gte=1"}, "z.number().int().min(1)"},
		{"bool omitempty", fieldData{GoType: "bool", Type: "boolean", ValidateTag: "omitempty"}, "z.boolean()"},
		// f.Optional (pointer or json:,omitempty) still drives .optional() on its own.
		{"pointer string no validate omitempty", fieldData{GoType: "string", Type: "string", Optional: true}, "z.string().optional()"},
		// Issue #178 regression: pointer + validate-omitempty stacks both the empty-literal
		// wrap and .optional(). The pointer carries json-level optionality (f.Optional=true),
		// the validate tag still contributes the empty-string tolerance.
		{"pointer string with validate omitempty", fieldData{GoType: "string", Type: "string", Optional: true, ValidateTag: "omitempty,url,max=500"}, `z.union([z.literal(""), z.string().url().max(500)]).optional()`},
		// Same story for json:,omitempty on a non-pointer field — f.Optional is true because
		// isOptional sees the json tag, so .optional() is retained.
		{"json omitempty string with validate omitempty", fieldData{GoType: "string", Type: "string", Optional: true, ValidateTag: "omitempty,email"}, `z.union([z.literal(""), z.string().email()]).optional()`},

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

		// Issue #176: registered enum fields emit z.enum / z.union instead of
		// plain z.string() / z.number().int() so z.infer matches the branded
		// TS enum type.
		{
			"string enum",
			fieldData{GoType: "string", Type: "TargetTypeType", Enum: stringEnumFixture("TargetType", "event", "post", "comment")},
			`z.enum(["event", "post", "comment"])`,
		},
		{
			"string enum required (no redundant min(1))",
			fieldData{GoType: "string", Type: "TargetTypeType", ValidateTag: "required", Enum: stringEnumFixture("TargetType", "event", "post")},
			`z.enum(["event", "post"])`,
		},
		{
			"string enum optional",
			fieldData{GoType: "string", Type: "TargetTypeType", Optional: true, Enum: stringEnumFixture("TargetType", "event", "post")},
			`z.enum(["event", "post"]).optional()`,
		},
		{
			"string enum with omitempty",
			fieldData{GoType: "string", Type: "TargetTypeType", ValidateTag: "omitempty", Enum: stringEnumFixture("TargetType", "event", "post")},
			`z.union([z.literal(""), z.enum(["event", "post"])])`,
		},
		{
			"pointer string enum with omitempty",
			fieldData{GoType: "string", Type: "TargetTypeType", Optional: true, ValidateTag: "omitempty", Enum: stringEnumFixture("TargetType", "event", "post")},
			`z.union([z.literal(""), z.enum(["event", "post"])]).optional()`,
		},
		{
			"string enum nullable via sql.Null",
			fieldData{GoType: "struct", Type: "TargetTypeType | null", SQLNullKind: "string", Enum: stringEnumFixture("TargetType", "event", "post")},
			`z.enum(["event", "post"]).nullable()`,
		},
		{
			"int enum",
			fieldData{GoType: "int", Type: "StatusType", Enum: intEnumFixture("Status", 0, 1, 2)},
			`z.union([z.literal(0), z.literal(1), z.literal(2)])`,
		},
		{
			"int enum optional",
			fieldData{GoType: "int", Type: "StatusType", Optional: true, Enum: intEnumFixture("Status", 0, 1)},
			`z.union([z.literal(0), z.literal(1)]).optional()`,
		},
		{
			"slice of string enum",
			fieldData{GoType: "slice", Type: "TargetTypeType[]", ElemGoKind: "string", ElemEnum: stringEnumFixture("TargetType", "event", "post")},
			`z.array(z.enum(["event", "post"]))`,
		},
		{
			"map of int enum",
			fieldData{GoType: "map", Type: "Record<string, StatusType>", ElemGoKind: "int", ElemEnum: intEnumFixture("Status", 0, 1)},
			`z.record(z.string(), z.union([z.literal(0), z.literal(1)]))`,
		},
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

// TestFieldToZodOneof verifies that the oneof validate rule is honored rather
// than silently dropped: a string oneof becomes z.enum([...]) and a numeric
// oneof becomes a z.union of literals (or a single z.literal for one value),
// matching go-playground's "value must be one of" semantics.
func TestFieldToZodOneof(t *testing.T) {
	tests := []struct {
		name  string
		field fieldData
		want  string
	}{
		{"string oneof", fieldData{GoType: "string", Type: "string", ValidateTag: "oneof=red green blue"}, `z.enum(["red", "green", "blue"])`},
		{"string oneof optional", fieldData{GoType: "string", Type: "string", Optional: true, ValidateTag: "oneof=red green"}, `z.enum(["red", "green"]).optional()`},
		{"string oneof omitempty", fieldData{GoType: "string", Type: "string", ValidateTag: "omitempty,oneof=red green"}, `z.union([z.literal(""), z.enum(["red", "green"])])`},
		{"string oneof nullable", fieldData{GoType: "struct", Type: "string | null", SQLNullKind: "string", ValidateTag: "oneof=red green"}, `z.enum(["red", "green"]).nullable()`},
		{"string oneof with quote", fieldData{GoType: "string", Type: "string", ValidateTag: `oneof=a"b`}, `z.enum(["a\"b"])`},
		{"int oneof", fieldData{GoType: "int", Type: "number", ValidateTag: "oneof=1 2 3"}, `z.union([z.literal(1), z.literal(2), z.literal(3)])`},
		{"int oneof single", fieldData{GoType: "int", Type: "number", ValidateTag: "oneof=5"}, `z.literal(5)`},
		{"int oneof optional", fieldData{GoType: "int", Type: "number", Optional: true, ValidateTag: "oneof=1 2"}, `z.union([z.literal(1), z.literal(2)]).optional()`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fieldToZod(tt.field, nil)
			if got != tt.want {
				t.Errorf("fieldToZod(%+v) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

// TestFieldToZodKindAwareConstraints guards against emitting Zod chain methods
// that don't exist for the base type: strings have no numeric .gt()/.lt()
// comparators (only length via .min()/.max()), and z.record() exposes no
// length/size constraint at all. Slices keep array-length .min()/.max().
func TestFieldToZodKindAwareConstraints(t *testing.T) {
	tests := []struct {
		name  string
		field fieldData
		want  string
	}{
		// String gt/lt translate to inclusive length bounds, not invalid .gt()/.lt().
		{"string gt", fieldData{GoType: "string", Type: "string", ValidateTag: "gt=5"}, "z.string().min(6)"},
		{"string lt", fieldData{GoType: "string", Type: "string", ValidateTag: "lt=5"}, "z.string().max(4)"},
		// Maps: size constraints have no z.record() equivalent — drop them.
		{"map min dropped", fieldData{GoType: "map", Type: "Record<string, string>", ElemGoKind: "string", ValidateTag: "min=1"}, "z.record(z.string(), z.string())"},
		{"map max dropped", fieldData{GoType: "map", Type: "Record<string, number>", ElemGoKind: "int", ValidateTag: "max=5"}, "z.record(z.string(), z.number().int())"},
		// Slices keep array-length min/max (valid Zod) and translate gt/lt.
		{"slice max kept", fieldData{GoType: "slice", Type: "string[]", ElemGoKind: "string", ValidateTag: "max=10"}, "z.array(z.string()).max(10)"},
		{"slice gt", fieldData{GoType: "slice", Type: "number[]", ElemGoKind: "int", ValidateTag: "gt=2"}, "z.array(z.number().int()).min(3)"},
		{"slice lt", fieldData{GoType: "slice", Type: "number[]", ElemGoKind: "int", ValidateTag: "lt=4"}, "z.array(z.number().int()).max(3)"},
		// Numbers keep the real .gt()/.lt() comparators.
		{"int gt kept", fieldData{GoType: "int", Type: "number", ValidateTag: "gt=2"}, "z.number().int().gt(2)"},
		{"int lt kept", fieldData{GoType: "int", Type: "number", ValidateTag: "lt=9"}, "z.number().int().lt(9)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fieldToZod(tt.field, nil)
			if got != tt.want {
				t.Errorf("fieldToZod(%+v) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

// TestFieldToZodEscaping guards against unescaped string injection into the
// generated TypeScript. Enum string values and the params of refine-based
// rules (contains/startswith/endswith) flow straight into a TS string literal;
// a value containing a double quote or backslash must be escaped or it breaks
// the generated module at parse time.
func TestFieldToZodEscaping(t *testing.T) {
	tests := []struct {
		name  string
		field fieldData
		want  string
	}{
		// Enum value containing a double quote must be escaped.
		{
			"string enum with quote",
			fieldData{GoType: "string", Type: "QuoteType", Enum: stringEnumFixture("Quote", `a"b`, "c")},
			`z.enum(["a\"b", "c"])`,
		},
		// Enum value containing a backslash must be escaped.
		{
			"string enum with backslash",
			fieldData{GoType: "string", Type: "PathType", Enum: stringEnumFixture("Path", `a\b`)},
			`z.enum(["a\\b"])`,
		},
		// contains= param with a double quote must not break the refine literal.
		{
			"contains with quote",
			fieldData{GoType: "string", Type: "string", ValidateTag: `contains=a"b`},
			`z.string().refine(v => v.includes("a\"b"), { message: "must contain a\"b" })`,
		},
		// Plain contains stays byte-for-byte identical (backward compat).
		{
			"contains plain",
			fieldData{GoType: "string", Type: "string", ValidateTag: `contains=foo`},
			`z.string().refine(v => v.includes("foo"), { message: "must contain foo" })`,
		},
		{
			"startswith with quote",
			fieldData{GoType: "string", Type: "string", ValidateTag: `startswith=x"y`},
			`z.string().refine(v => v.startsWith("x\"y"), { message: "must start with x\"y" })`,
		},
		{
			"endswith with quote",
			fieldData{GoType: "string", Type: "string", ValidateTag: `endswith=x"y`},
			`z.string().refine(v => v.endsWith("x\"y"), { message: "must end with x\"y" })`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fieldToZod(tt.field, nil)
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

func TestBuildZodSchemas_RecursiveLazy(t *testing.T) {
	// Self-referential and mutually recursive schemas must wrap the cyclic
	// element reference in z.lazy(() => XSchema). Without it, the generated
	// `const XSchema = z.object({ ... XSchema ... })` hits a temporal-dead-zone
	// ReferenceError at module load. Non-cyclic references stay direct (and are
	// ordered by the topological sort).
	fieldZod := func(schemas []zodSchemaData, schema, field string) string {
		for _, s := range schemas {
			if s.Name != schema {
				continue
			}
			for _, f := range s.Fields {
				if f.Name == field {
					return f.ZodType
				}
			}
		}
		return ""
	}

	t.Run("self reference", func(t *testing.T) {
		interfaces := []interfaceData{
			{
				Name: "TreeNode",
				Fields: []fieldData{
					{Name: "label", GoType: "string", Type: "string", ValidateTag: "required,min=1"},
					{Name: "children", GoType: "slice", Type: "TreeNode[]", ElemGoKind: "struct", ElemTypeName: "TreeNode", ValidateTag: "dive"},
				},
			},
		}
		got := fieldZod(buildZodSchemas(interfaces), "TreeNode", "children")
		want := "z.array(z.lazy(() => TreeNodeSchema))"
		if got != want {
			t.Errorf("children = %q, want %q", got, want)
		}
	})

	t.Run("mutual recursion", func(t *testing.T) {
		interfaces := []interfaceData{
			{
				Name: "Category",
				Fields: []fieldData{
					{Name: "name", GoType: "string", Type: "string", ValidateTag: "required"},
					{Name: "items", GoType: "slice", Type: "Item[]", ElemGoKind: "struct", ElemTypeName: "Item", ValidateTag: "dive"},
				},
			},
			{
				Name: "Item",
				Fields: []fieldData{
					{Name: "title", GoType: "string", Type: "string", ValidateTag: "required"},
					{Name: "category", GoType: "map", Type: "Record<string, Category>", ElemGoKind: "struct", ElemTypeName: "Category"},
				},
			},
		}
		schemas := buildZodSchemas(interfaces)
		if got, want := fieldZod(schemas, "Category", "items"), "z.array(z.lazy(() => ItemSchema))"; got != want {
			t.Errorf("Category.items = %q, want %q", got, want)
		}
		if got, want := fieldZod(schemas, "Item", "category"), "z.record(z.string(), z.lazy(() => CategorySchema))"; got != want {
			t.Errorf("Item.category = %q, want %q", got, want)
		}
	})

	t.Run("non-cyclic stays direct", func(t *testing.T) {
		interfaces := []interfaceData{
			{
				Name: "Parent",
				Fields: []fieldData{
					{Name: "tags", GoType: "slice", Type: "Leaf[]", ElemGoKind: "struct", ElemTypeName: "Leaf", ValidateTag: "dive"},
				},
			},
			{
				Name: "Leaf",
				Fields: []fieldData{
					{Name: "name", GoType: "string", Type: "string", ValidateTag: "required"},
				},
			},
		}
		got := fieldZod(buildZodSchemas(interfaces), "Parent", "tags")
		want := "z.array(LeafSchema)"
		if got != want {
			t.Errorf("Parent.tags = %q, want %q (non-cyclic refs must not be lazy)", got, want)
		}
	})
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

func TestZodGeneration_LeafSchemasEmittedBeforeReferences(t *testing.T) {
	// Regression for the v0.37.2 bug introduced by #169: a parent schema
	// that referenced a leaf schema (e.g. Links: z.array(EventTagSchema))
	// could be emitted *before* the leaf's `const`, causing a TDZ
	// ReferenceError at module load. Fix is topological sort in
	// buildZodSchemas. This test asserts the order constraint directly on
	// the generated file.
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

	leafIdx := strings.Index(schemaFile, "EventTagSchema = z.object")
	parentIdx := strings.Index(schemaFile, "SliceElemRequestSchema = z.object")
	if leafIdx < 0 {
		t.Fatalf("EventTagSchema declaration not found in generated file:\n%s", schemaFile)
	}
	if parentIdx < 0 {
		t.Fatalf("SliceElemRequestSchema declaration not found in generated file:\n%s", schemaFile)
	}
	if leafIdx > parentIdx {
		t.Errorf("EventTagSchema must be declared before SliceElemRequestSchema (leafIdx=%d, parentIdx=%d)\n---\n%s", leafIdx, parentIdx, schemaFile)
	}
}

func TestTopoSortSchemas(t *testing.T) {
	// Direct unit test for the sort: leaf must come before parent.
	schemas := []zodSchemaData{
		{Name: "Parent"},
		{Name: "Leaf"},
		{Name: "Unrelated"},
	}
	deps := map[string][]string{
		"Parent":    {"Leaf"},
		"Leaf":      nil,
		"Unrelated": nil,
	}
	got := topoSortSchemas(schemas, deps)
	pos := make(map[string]int)
	for i, s := range got {
		pos[s.Name] = i
	}
	if pos["Leaf"] >= pos["Parent"] {
		t.Errorf("Leaf must come before Parent: got positions Leaf=%d Parent=%d", pos["Leaf"], pos["Parent"])
	}
	if len(got) != 3 {
		t.Errorf("expected 3 schemas, got %d", len(got))
	}

	// Cycle: A -> B -> A. Should not infinite-loop, both should appear.
	cyclicSchemas := []zodSchemaData{{Name: "A"}, {Name: "B"}}
	cyclicDeps := map[string][]string{"A": {"B"}, "B": {"A"}}
	cyclicGot := topoSortSchemas(cyclicSchemas, cyclicDeps)
	if len(cyclicGot) != 2 {
		t.Errorf("cycle: expected 2 schemas, got %d", len(cyclicGot))
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
		// Issue 1 + #178: omitempty,url,max=500 on a non-pointer string wraps in the
		// empty-literal union but does NOT append .optional() — optionality is driven
		// by pointer / json:,omitempty alone, matching the TS interface generator.
		{`imageUrl: z.union([z.literal(""), z.string().url().max(500)])`, "ImageURL: empty-literal union, no .optional()"},
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
	// Regression (#178): non-pointer validate-omitempty string must not grow a
	// trailing .optional() — isOptional treats it as required and the Zod
	// inferred type has to match.
	if strings.Contains(schemaFile, `imageUrl: z.union([z.literal(""), z.string().url().max(500)]).optional()`) {
		t.Error("#178: validate-omitempty alone should not append .optional() on a non-pointer string field")
	}
}

// --- Issue #176: enum-aware Zod codegen ---

// EnumTarget is a string-based enum used by the integration test below.
type EnumTarget string

const (
	EnumTargetEvent   EnumTarget = "event"
	EnumTargetPost    EnumTarget = "post"
	EnumTargetComment EnumTarget = "comment"
)

func EnumTargetValues() []EnumTarget {
	return []EnumTarget{EnumTargetEvent, EnumTargetPost, EnumTargetComment}
}

// EnumPriority is an int-based enum to exercise the z.union([z.literal(N),
// ...]) output path.
type EnumPriority int

const (
	EnumPriorityLow  EnumPriority = 0
	EnumPriorityMed  EnumPriority = 1
	EnumPriorityHigh EnumPriority = 2
)

func EnumPriorityValues() []EnumPriority {
	return []EnumPriority{EnumPriorityLow, EnumPriorityMed, EnumPriorityHigh}
}

// EnumParamsRequest combines every enum field shape covered by the Zod fix
// so the integration test can assert each emission in one Generate() run.
type EnumParamsRequest struct {
	Title   string       `json:"title"   validate:"required,min=1"`
	Target  EnumTarget   `json:"target"  validate:"required"`
	Opt     EnumTarget   `json:"opt"     validate:"omitempty"`
	Related []EnumTarget `json:"related"`
	Level   EnumPriority `json:"level"   validate:"required"`
}

type EnumParamsHandlers struct{}

func (h *EnumParamsHandlers) Submit(ctx context.Context, req *EnumParamsRequest) error {
	return nil
}

func TestZodGeneration_EnumFields(t *testing.T) {
	// Integration regression for issue #176: generated Zod schemas must use
	// z.enum([...]) for string enums and z.union([z.literal(N), ...]) for
	// int enums so z.infer<> is assignable to the branded TS enum types the
	// interface generator emits. Before this fix both fell through to
	// z.string() / z.number().int().
	registry := NewRegistry()
	registry.Register(&EnumParamsHandlers{})
	registry.RegisterEnum(EnumTargetValues())
	registry.RegisterEnum(EnumPriorityValues())

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
		// Required string enum: plain z.enum([...]) literal, no .min(1) noise.
		{`target: z.enum(["event", "post", "comment"])`, "Target: string enum"},
		// validate-omitempty string enum: empty-literal union wrap, no .optional()
		// since the Go field is a non-pointer string (#178).
		{`opt: z.union([z.literal(""), z.enum(["event", "post", "comment"])])`, "Opt: string enum with omitempty wrap"},
		// Slice of string enum → z.array(z.enum([...])).
		{`related: z.array(z.enum(["event", "post", "comment"]))`, "Related: slice of string enum"},
		// Int enum → z.union of literals.
		{`level: z.union([z.literal(0), z.literal(1), z.literal(2)])`, "Level: int enum"},
	}
	for _, exp := range expectations {
		if !strings.Contains(schemaFile, exp.substr) {
			t.Errorf("%s — expected schema to contain %q\n---\n%s", exp.desc, exp.substr, schemaFile)
		}
	}

	// Regressions: enum fields must not fall through to the primitive base
	// types. Before #176 these were the output.
	forbidden := []string{
		"target: z.string()",
		"related: z.array(z.string())",
		"level: z.number().int()",
	}
	for _, bad := range forbidden {
		if strings.Contains(schemaFile, bad) {
			t.Errorf("schema should not contain %q (pre-#176 regression)\n---\n%s", bad, schemaFile)
		}
	}
}
