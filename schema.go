package aprot

import (
	"reflect"
	"sort"
	"strings"
)

// schemaGen builds JSON Schemas from Go types. It is the single
// implementation behind both the OpenAPI generator and Registry.SchemaFor
// (issue #316), so struct flattening, registered enums, byte-slice wire
// shapes and validate-tag constraints stay identical across consumers.
//
// Two modes:
//   - ref mode (refs != nil): struct types are registered in refs and
//     referenced via "#/components/schemas/<name>" — the OpenAPI shape.
//   - inline mode (refs == nil): struct schemas are expanded in place,
//     producing a self-contained schema. Recursive types are broken with a
//     plain object schema carrying the type name as description.
type schemaGen struct {
	registry *Registry
	meta     *sourceMeta
	refs     map[reflect.Type]*JSONSchema // ref mode when non-nil
	building map[reflect.Type]bool        // inline-mode cycle guard (lazy)
}

// SchemaFor returns a self-contained JSON Schema for a Go type: registered
// enums become enum schemas, structs are expanded inline with embedded
// fields flattened, `validate` tags map to constraints, and godoc on types
// and fields becomes descriptions (from baked docs when [Registry.SetSourceDocs]
// was called, otherwise extracted from source when available). This is the
// same schema generation the OpenAPI generator uses, exposed for consumers
// that need per-type schemas — MCP tool input schemas in particular.
//
// Pointers are dereferenced. Recursive struct types are valid input; the
// recursion is broken with an unconstrained object schema. Each call
// resolves godoc metadata, so cache the result if calling in a hot path.
func (r *Registry) SchemaFor(t reflect.Type) *JSONSchema {
	sg := &schemaGen{registry: r, meta: r.resolveSourceMeta()}
	return sg.goTypeToJSONSchema(t)
}

// goTypeToJSONSchema converts a Go reflect.Type to a JSON Schema.
func (g *schemaGen) goTypeToJSONSchema(t reflect.Type) *JSONSchema {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// Check registered enums
	if enumInfo := g.registry.GetEnum(t); enumInfo != nil {
		return g.enumToJSONSchema(enumInfo)
	}

	switch t.Kind() {
	case reflect.String:
		return &JSONSchema{Type: "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &JSONSchema{Type: "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &JSONSchema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &JSONSchema{Type: "number"}
	case reflect.Bool:
		return &JSONSchema{Type: "boolean"}
	case reflect.Slice:
		if isUnnamedByteSlice(t) {
			// Unnamed []byte is base64-encoded as a string on the wire under
			// both encoding/json v1 and encoding/json/v2 (issue
			// #174). OpenAPI 3.0 represents this as {type: string, format:
			// byte}.
			return &JSONSchema{Type: "string", Format: "byte"}
		}
		return &JSONSchema{
			Type:  "array",
			Items: g.goTypeToJSONSchema(t.Elem()),
		}
	case reflect.Array:
		if isByteArray(t) {
			// [N]byte (named or not) is base64-encoded as a string by
			// encoding/json/v2, same as unnamed []byte (#240).
			return &JSONSchema{Type: "string", Format: "byte"}
		}
		n := t.Len()
		return &JSONSchema{
			Type:     "array",
			Items:    g.goTypeToJSONSchema(t.Elem()),
			MinItems: &n,
			MaxItems: &n,
		}
	case reflect.Map:
		return &JSONSchema{
			Type:                 "object",
			AdditionalProperties: g.goTypeToJSONSchema(t.Elem()),
		}
	case reflect.Struct:
		if t.PkgPath() == "" {
			return &JSONSchema{Type: "object"}
		}
		if g.refs != nil {
			// Ref mode: register as component schema and return $ref.
			if _, exists := g.refs[t]; !exists {
				g.buildStructSchema(t)
			}
			return &JSONSchema{Ref: "#/components/schemas/" + sanitizeTSIdent(t.Name())}
		}
		// Inline mode: expand in place; break recursion with a bare object.
		if g.building[t] {
			return &JSONSchema{Type: "object", Description: t.Name()}
		}
		if g.building == nil {
			g.building = make(map[reflect.Type]bool)
		}
		g.building[t] = true
		schema := g.buildStructSchema(t)
		delete(g.building, t)
		return schema
	case reflect.Interface:
		return &JSONSchema{}
	default:
		return &JSONSchema{}
	}
}

// buildStructSchema builds a JSON Schema for a struct type. In ref mode the
// schema is registered in g.refs before fields are walked, so circular
// references resolve to the component $ref.
func (g *schemaGen) buildStructSchema(t reflect.Type) *JSONSchema {
	schema := &JSONSchema{
		Type:        "object",
		Properties:  make(map[string]*JSONSchema),
		Description: g.meta.typeDoc(t.Name()),
	}
	if g.refs != nil {
		// Register early to handle circular references
		g.refs[t] = schema
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Tag.Get("json") == "-" {
			continue
		}

		// Handle embedded structs
		if field.Anonymous {
			ft := field.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				// Flatten embedded fields
				subSchema := &JSONSchema{
					Type:       "object",
					Properties: make(map[string]*JSONSchema),
				}
				g.buildFieldsInto(ft, subSchema)
				for name, prop := range subSchema.Properties {
					schema.Properties[name] = prop
				}
				schema.Required = append(schema.Required, subSchema.Required...)
				continue
			}
		}

		jsonName := jsonFieldName(field)
		var fieldSchema *JSONSchema
		if s := byteSliceFieldSchema(field); s != nil {
			fieldSchema = s
		} else {
			fieldSchema = g.goTypeToJSONSchema(field.Type)
		}

		// Apply validate constraints
		validateTag := field.Tag.Get("validate")
		if validateTag != "" {
			applyValidateConstraints(fieldSchema, validateTag, field.Type)
		}

		// Attach field godoc. The Go field name (not JSON name) is what the AST knows.
		if fdoc := g.meta.fieldDoc(t.Name(), field.Name); fdoc != "" {
			fieldSchema.Description = fdoc
		}

		schema.Properties[jsonName] = fieldSchema

		// Determine if required
		jsonTag := field.Tag.Get("json")
		isOptional := strings.Contains(jsonTag, "omitempty") || field.Type.Kind() == reflect.Pointer
		if !isOptional {
			schema.Required = append(schema.Required, jsonName)
		}
	}

	sort.Strings(schema.Required)
	return schema
}

// buildFieldsInto adds struct fields to an existing schema (for embedded struct flattening).
func (g *schemaGen) buildFieldsInto(t reflect.Type, schema *JSONSchema) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() || field.Tag.Get("json") == "-" {
			continue
		}
		jsonName := jsonFieldName(field)
		var fieldSchema *JSONSchema
		if s := byteSliceFieldSchema(field); s != nil {
			fieldSchema = s
		} else {
			fieldSchema = g.goTypeToJSONSchema(field.Type)
		}

		validateTag := field.Tag.Get("validate")
		if validateTag != "" {
			applyValidateConstraints(fieldSchema, validateTag, field.Type)
		}

		if fdoc := g.meta.fieldDoc(t.Name(), field.Name); fdoc != "" {
			fieldSchema.Description = fdoc
		}

		schema.Properties[jsonName] = fieldSchema

		jsonTag := field.Tag.Get("json")
		isOptional := strings.Contains(jsonTag, "omitempty") || field.Type.Kind() == reflect.Pointer
		if !isOptional {
			schema.Required = append(schema.Required, jsonName)
		}
	}
}

// enumToJSONSchema converts an enum to a JSON Schema with enum values.
func (g *schemaGen) enumToJSONSchema(info *EnumInfo) *JSONSchema {
	schema := &JSONSchema{}
	if info.IsString {
		schema.Type = "string"
	} else {
		schema.Type = "integer"
	}
	for _, v := range info.Values {
		schema.Enum = append(schema.Enum, v.Value)
	}
	return schema
}
