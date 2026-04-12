package aprot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAPIGenerator_BasicSpec(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	if spec.OpenAPI != "3.0.3" {
		t.Errorf("expected openapi 3.0.3, got %s", spec.OpenAPI)
	}
	if spec.Info.Title != "Test API" {
		t.Errorf("expected title 'Test API', got %s", spec.Info.Title)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("expected paths to be generated")
	}

	// Check GetUser path
	for path, item := range spec.Paths {
		if strings.Contains(path, "get-user") {
			if item.Get == nil {
				t.Error("GetUser should be a GET operation")
			}
			if item.Get != nil {
				if len(item.Get.Parameters) != 1 {
					t.Errorf("GetUser: expected 1 parameter, got %d", len(item.Get.Parameters))
				} else {
					if item.Get.Parameters[0].Name != "id" {
						t.Errorf("expected param name 'id', got %q", item.Get.Parameters[0].Name)
					}
					if item.Get.Parameters[0].In != "path" {
						t.Errorf("expected param in 'path', got %q", item.Get.Parameters[0].In)
					}
				}
			}
			break
		}
	}
}

func TestOpenAPIGenerator_ValidationConstraints(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	schema, ok := spec.Components.Schemas["CreateUserReq"]
	if !ok {
		t.Fatal("expected CreateUserReq in components/schemas")
	}

	nameSchema, ok := schema.Properties["name"]
	if !ok {
		t.Fatal("expected 'name' property")
	}
	if nameSchema.MinLength == nil || *nameSchema.MinLength != 2 {
		t.Errorf("expected name minLength=2, got %v", nameSchema.MinLength)
	}

	emailSchema, ok := schema.Properties["email"]
	if !ok {
		t.Fatal("expected 'email' property")
	}
	if emailSchema.Format != "email" {
		t.Errorf("expected email format='email', got %q", emailSchema.Format)
	}
}

func TestOpenAPIGenerator_HTTPMethods(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	for path, item := range spec.Paths {
		if strings.Contains(path, "create-user") {
			if item.Post == nil {
				t.Error("CreateUser should be POST")
			}
			if item.Post != nil && item.Post.RequestBody == nil {
				t.Error("CreateUser should have request body")
			}
		}
		if strings.Contains(path, "delete-user") {
			if item.Delete == nil {
				t.Error("DeleteUser should be DELETE")
			}
		}
		if strings.Contains(path, "update-user") {
			if item.Put == nil {
				t.Error("UpdateUser should be PUT")
			}
		}
		if strings.Contains(path, "list-users") {
			if item.Get == nil {
				t.Error("ListUsers should be GET")
			}
		}
	}
}

func TestOpenAPIGenerator_JSON(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	data, err := gen.GenerateJSON()
	if err != nil {
		t.Fatalf("GenerateJSON() failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated JSON is invalid: %v", err)
	}

	if parsed["openapi"] != "3.0.3" {
		t.Errorf("expected openapi 3.0.3 in JSON")
	}
}

func TestOpenAPIGenerator_OnlyRESTGroups(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})
	registry.Register(&AdminHandlers{}) // not REST

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	for path := range spec.Paths {
		if strings.Contains(path, "admin") || strings.Contains(path, "delete-everything") {
			t.Errorf("non-REST handler should not appear in spec: %s", path)
		}
	}

	if len(spec.Paths) == 0 {
		t.Fatal("expected REST handler paths")
	}
}

func TestOpenAPIGenerator_WithBasePath(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0").
		WithBasePath("/rest/api/v1.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	for path := range spec.Paths {
		if !strings.HasPrefix(path, "/rest/api/v1.0/") {
			t.Errorf("expected path to start with /rest/api/v1.0/, got %s", path)
		}
	}

	if len(spec.Paths) == 0 {
		t.Fatal("expected paths")
	}
}

func TestOpenAPIGenerator_WithBasePath_TrailingSlash(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0").
		WithBasePath("/api/v1/")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	for path := range spec.Paths {
		if strings.Contains(path, "//") {
			t.Errorf("path should not have double slashes: %s", path)
		}
		if !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("expected path to start with /api/v1/, got %s", path)
		}
	}
}

func TestOpenAPIGenerator_DocComments(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterREST(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Locate the CreateUser operation by path.
	var createOp *Operation
	for path, item := range spec.Paths {
		if strings.Contains(path, "create-user") {
			createOp = item.Post
			break
		}
	}
	if createOp == nil {
		t.Fatal("expected to find CreateUser operation")
	}

	wantSummary := "CreateUser provisions a new user account."
	if createOp.Summary != wantSummary {
		t.Errorf("Summary: got %q, want %q", createOp.Summary, wantSummary)
	}
	if !strings.Contains(createOp.Description, "immediately active") {
		t.Errorf("Description should contain 'immediately active', got %q", createOp.Description)
	}

	// Undocumented handlers still fall back to the generated synthetic summary.
	var getOp *Operation
	for path, item := range spec.Paths {
		if strings.Contains(path, "get-user") {
			getOp = item.Get
			break
		}
	}
	if getOp == nil {
		t.Fatal("expected to find GetUser operation")
	}
	if getOp.Summary != "RESTHandlers.GetUser" {
		t.Errorf("undocumented Summary: got %q, want fallback %q", getOp.Summary, "RESTHandlers.GetUser")
	}
	if getOp.Description != "" {
		t.Errorf("undocumented Description: got %q, want empty", getOp.Description)
	}

	// Struct type doc → schema Description.
	schema, ok := spec.Components.Schemas["CreateUserReq"]
	if !ok {
		t.Fatal("expected CreateUserReq in components/schemas")
	}
	if !strings.Contains(schema.Description, "payload accepted by the CreateUser") {
		t.Errorf("schema Description: got %q", schema.Description)
	}

	// Struct field doc → property Description.
	nameProp, ok := schema.Properties["name"]
	if !ok {
		t.Fatal("expected 'name' property")
	}
	if !strings.Contains(nameProp.Description, "full display name") {
		t.Errorf("name field Description: got %q", nameProp.Description)
	}
	emailProp, ok := schema.Properties["email"]
	if !ok {
		t.Fatal("expected 'email' property")
	}
	if !strings.Contains(emailProp.Description, "primary contact address") {
		t.Errorf("email field Description: got %q", emailProp.Description)
	}
}

func TestApplyValidateConstraints(t *testing.T) {
	stringType := reflect.TypeOf("")
	intType := reflect.TypeOf(0)

	tests := []struct {
		name  string
		tag   string
		goTyp reflect.Type
		check func(s *JSONSchema) bool
		desc  string
	}{
		{"min string", "min=3", stringType, func(s *JSONSchema) bool { return s.MinLength != nil && *s.MinLength == 3 }, "minLength=3"},
		{"max string", "max=50", stringType, func(s *JSONSchema) bool { return s.MaxLength != nil && *s.MaxLength == 50 }, "maxLength=50"},
		{"gte number", "gte=12", intType, func(s *JSONSchema) bool { return s.Minimum != nil && *s.Minimum == 12 }, "minimum=12"},
		{"lte number", "lte=110", intType, func(s *JSONSchema) bool { return s.Maximum != nil && *s.Maximum == 110 }, "maximum=110"},
		{"email", "email", stringType, func(s *JSONSchema) bool { return s.Format == "email" }, "format=email"},
		{"url", "url", stringType, func(s *JSONSchema) bool { return s.Format == "uri" }, "format=uri"},
		{"uuid", "uuid", stringType, func(s *JSONSchema) bool { return s.Format == "uuid" }, "format=uuid"},
		{"oneof", "oneof=red green blue", stringType, func(s *JSONSchema) bool { return len(s.Enum) == 3 }, "3 enum values"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := &JSONSchema{Type: "string"}
			if tt.goTyp.Kind() != reflect.String {
				schema.Type = "integer"
			}
			applyValidateConstraints(schema, tt.tag, tt.goTyp)
			if !tt.check(schema) {
				t.Errorf("expected %s for tag %q", tt.desc, tt.tag)
			}
		})
	}
}

// TestOpenAPIGoTypeToJSONSchema_ByteSlice is a regression test for issue #174:
// unnamed []byte must map to {type: string, format: byte} because it is
// serialized as a base64 string by both encoding/json v1 and
// go-json-experiment/json v2, not as an array of integers. Named byte slices
// are left on the existing array path to match v2's per-element encoding.
func TestOpenAPIGoTypeToJSONSchema_ByteSlice(t *testing.T) {
	gen := NewOpenAPIGenerator(NewRegistry(), "Test", "1.0.0")

	t.Run("unnamed []byte is string/byte", func(t *testing.T) {
		schema := gen.goTypeToJSONSchema(reflect.TypeOf([]byte(nil)))
		if schema == nil {
			t.Fatal("schema is nil")
		}
		if schema.Type != "string" {
			t.Errorf("Type = %q, want %q", schema.Type, "string")
		}
		if schema.Format != "byte" {
			t.Errorf("Format = %q, want %q", schema.Format, "byte")
		}
		if schema.Items != nil {
			t.Errorf("Items should be nil for string/byte, got %+v", schema.Items)
		}
	})

	t.Run("named byte slice still array", func(t *testing.T) {
		schema := gen.goTypeToJSONSchema(reflect.TypeOf(namedByteSlice(nil)))
		if schema == nil {
			t.Fatal("schema is nil")
		}
		if schema.Type != "array" {
			t.Errorf("Type = %q, want %q", schema.Type, "array")
		}
		if schema.Items == nil || schema.Items.Type != "integer" {
			t.Errorf("Items = %+v, want integer element", schema.Items)
		}
	})
}

// TestOpenAPIByteSliceFormatTag covers the v2 json `format:` tag end-to-end
// through the struct schema builder: the emitted JSON Schema for each field
// must match the on-the-wire shape driven by the tag, inverting the #174
// defaults where the tag says so.
func TestOpenAPIByteSliceFormatTag(t *testing.T) {
	gen := NewOpenAPIGenerator(NewRegistry(), "Test", "1.0.0")

	gen.buildStructSchema(reflect.TypeOf(ByteSliceFormatTagStruct{}))

	schema := gen.schemas[reflect.TypeOf(ByteSliceFormatTagStruct{})]
	if schema == nil {
		t.Fatal("ByteSliceFormatTagStruct schema not registered")
	}

	arrayFields := []string{"arrayFromUnnamed", "arrayFromNamed"}
	for _, name := range arrayFields {
		prop := schema.Properties[name]
		if prop == nil {
			t.Fatalf("property %q missing", name)
		}
		if prop.Type != "array" {
			t.Errorf("%s Type = %q, want %q", name, prop.Type, "array")
		}
		if prop.Items == nil || prop.Items.Type != "integer" {
			t.Errorf("%s Items = %+v, want integer", name, prop.Items)
		}
	}

	stringFields := []string{
		"base64FromNamed", "base64urlFromNamed", "base32FromNamed",
		"base32hexFromNamed", "base16FromNamed", "hexFromNamed",
	}
	for _, name := range stringFields {
		prop := schema.Properties[name]
		if prop == nil {
			t.Fatalf("property %q missing", name)
		}
		if prop.Type != "string" || prop.Format != "byte" {
			t.Errorf("%s = {Type: %q, Format: %q}, want {string, byte}", name, prop.Type, prop.Format)
		}
	}
}
