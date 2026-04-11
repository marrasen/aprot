package aprot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAPIGenerator_BasicSpec(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&RESTHandlers{})

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
	registry.Register(&RESTHandlers{})

	gen := NewOpenAPIGenerator(registry, "Test API", "1.0.0")
	spec, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Check that CreateUserReq schema has validation constraints
	schema, ok := spec.Components.Schemas["CreateUserReq"]
	if !ok {
		t.Fatal("expected CreateUserReq in components/schemas")
	}

	// name field should have minLength from validate:"required,min=2"
	nameSchema, ok := schema.Properties["name"]
	if !ok {
		t.Fatal("expected 'name' property")
	}
	if nameSchema.MinLength == nil || *nameSchema.MinLength != 2 {
		t.Errorf("expected name minLength=2, got %v", nameSchema.MinLength)
	}

	// email field should have format:"email"
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
	registry.Register(&RESTHandlers{})

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
	registry.Register(&RESTHandlers{})

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

func TestOpenAPIGenerator_ViaGenerator(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&RESTHandlers{})

	gen := NewGenerator(registry).WithOptions(GeneratorOptions{
		Mode:           OutputVanilla,
		OpenAPI:        true,
		OpenAPITitle:   "My API",
		OpenAPIVersion: "2.0.0",
	})

	results, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	content, ok := results["openapi.json"]
	if !ok {
		t.Fatal("expected openapi.json in results")
	}

	var spec OpenAPISpec
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		t.Fatalf("invalid openapi.json: %v", err)
	}
	if spec.Info.Title != "My API" {
		t.Errorf("expected title 'My API', got %q", spec.Info.Title)
	}
	if spec.Info.Version != "2.0.0" {
		t.Errorf("expected version '2.0.0', got %q", spec.Info.Version)
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
