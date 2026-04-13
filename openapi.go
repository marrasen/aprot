package aprot

import (
	"encoding/json"
	"go/doc"
	"reflect"
	"sort"
	"strings"
)

// OpenAPI 3.0 types

// OpenAPISpec represents an OpenAPI 3.0 document.
type OpenAPISpec struct {
	OpenAPI    string               `json:"openapi"`
	Info       OpenAPIInfo          `json:"info"`
	Paths      map[string]*PathItem `json:"paths"`
	Components *Components          `json:"components,omitempty"`
}

// OpenAPIInfo describes the API metadata.
type OpenAPIInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

// PathItem represents the operations on a single path.
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
}

// Operation represents a single API operation on a path.
type Operation struct {
	OperationID string              `json:"operationId"`
	Tags        []string            `json:"tags,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	Description string              `json:"description,omitempty"`
	Parameters  []Parameter         `json:"parameters,omitempty"`
	RequestBody *RequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]Response `json:"responses"`
}

// Parameter represents a single operation parameter.
type Parameter struct {
	Name     string      `json:"name"`
	In       string      `json:"in"` // "path", "query", "header"
	Required bool        `json:"required"`
	Schema   *JSONSchema `json:"schema"`
}

// RequestBody represents the request body.
type RequestBody struct {
	Required bool                 `json:"required"`
	Content  map[string]MediaType `json:"content"`
}

// MediaType describes a media type with schema.
type MediaType struct {
	Schema *JSONSchema `json:"schema"`
}

// Response represents a single response.
type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// Components holds reusable schema definitions.
type Components struct {
	Schemas map[string]*JSONSchema `json:"schemas,omitempty"`
}

// JSONSchema represents a JSON Schema object (subset used by OpenAPI 3.0).
type JSONSchema struct {
	Type                 string                 `json:"type,omitempty"`
	Format               string                 `json:"format,omitempty"`
	Properties           map[string]*JSONSchema `json:"properties,omitempty"`
	Required             []string               `json:"required,omitempty"`
	Items                *JSONSchema            `json:"items,omitempty"`
	Enum                 []any                  `json:"enum,omitempty"`
	Ref                  string                 `json:"$ref,omitempty"`
	MinLength            *int                   `json:"minLength,omitempty"`
	MaxLength            *int                   `json:"maxLength,omitempty"`
	Minimum              *float64               `json:"minimum,omitempty"`
	Maximum              *float64               `json:"maximum,omitempty"`
	ExclusiveMinimum     *float64               `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *float64               `json:"exclusiveMaximum,omitempty"`
	Pattern              string                 `json:"pattern,omitempty"`
	Description          string                 `json:"description,omitempty"`
	Nullable             bool                   `json:"nullable,omitempty"`
	AdditionalProperties *JSONSchema            `json:"additionalProperties,omitempty"`
}

// OpenAPIGenerator generates an OpenAPI 3.0 spec from a Registry.
// Only handlers registered via RegisterREST are included in the spec.
type OpenAPIGenerator struct {
	registry *Registry
	naming   NamingPlugin
	schemas  map[reflect.Type]*JSONSchema
	title    string
	version  string
	basePath string      // prepended to all paths, e.g. "/rest/api/v1.0"
	meta     *sourceMeta // AST-extracted godoc and parameter names, populated by Generate
}

// NewOpenAPIGenerator creates an OpenAPI spec generator.
func NewOpenAPIGenerator(registry *Registry, title, version string) *OpenAPIGenerator {
	return &OpenAPIGenerator{
		registry: registry,
		naming:   DefaultNaming{FixAcronyms: true},
		schemas:  make(map[reflect.Type]*JSONSchema),
		title:    title,
		version:  version,
	}
}

// WithNaming sets the naming plugin for path generation.
func (g *OpenAPIGenerator) WithNaming(n NamingPlugin) *OpenAPIGenerator {
	g.naming = n
	return g
}

// WithBasePath sets a prefix prepended to all paths in the generated spec.
// Use this when the API is mounted behind a proxy or at a non-root path.
//
// Example:
//
//	oag.WithBasePath("/rest/api/v1.0")
//	// paths: "/rest/api/v1.0/todos/create-todo", etc.
func (g *OpenAPIGenerator) WithBasePath(path string) *OpenAPIGenerator {
	// Strip trailing slash to avoid double slashes
	g.basePath = strings.TrimRight(path, "/")
	return g
}

// Generate produces an OpenAPI 3.0 spec.
func (g *OpenAPIGenerator) Generate() (*OpenAPISpec, error) {
	spec := &OpenAPISpec{
		OpenAPI: "3.0.3",
		Info:    OpenAPIInfo{Title: g.title, Version: g.version},
		Paths:   make(map[string]*PathItem),
		Components: &Components{
			Schemas: make(map[string]*JSONSchema),
		},
	}

	// Extract AST metadata (parameter names and godoc) from handler source dirs
	dirs := make(map[string]bool)
	for _, group := range g.registry.Groups() {
		if dir := group.SourceDir(); dir != "" {
			dirs[dir] = true
		}
	}
	g.meta = extractSourceMeta(dirs)

	// Sort groups for deterministic output
	groupNames := make([]string, 0, len(g.registry.Groups()))
	for name := range g.registry.Groups() {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	for _, groupName := range groupNames {
		if !g.registry.IsREST(groupName) {
			continue
		}
		group := g.registry.Groups()[groupName]
		prefix := g.naming.PathPrefix(groupName)

		methodNames := make([]string, 0, len(group.Handlers))
		for name := range group.Handlers {
			methodNames = append(methodNames, name)
		}
		sort.Strings(methodNames)

		for _, methodName := range methodNames {
			info := group.Handlers[methodName]
			// Streaming handlers are websocket/SSE only; OpenAPI describes
			// request/response HTTP operations with single response bodies.
			if info.Kind != HandlerKindUnary {
				continue
			}
			httpMethod := inferHTTPMethod(methodName)
			segment := g.naming.PathSegment(methodName)

			// Classify params
			var pathParams []routeParam
			var bodyParam *ParamInfo

			astNames := g.meta.paramNames(info.StructName, info.Name)

			for i := range info.Params {
				p := &info.Params[i]
				pt := p.Type
				if pt.Kind() == reflect.Ptr {
					pt = pt.Elem()
				}
				if pt.Kind() == reflect.Struct {
					bodyParam = p
				} else {
					name := "arg"
					if i < len(astNames) {
						name = astNames[i]
					}
					pathParams = append(pathParams, routeParam{Name: name, Info: *p})
				}
			}

			// Build path
			path := prefix + "/" + segment
			for _, pp := range pathParams {
				path += "/{" + pp.Name + "}"
			}

			// Build operation
			summary := groupName + "." + methodName
			description := ""
			if handlerDoc := g.meta.handlerDoc(info.StructName, info.Name); handlerDoc != "" {
				summary = doc.Synopsis(handlerDoc)
				description = handlerDoc
			}

			op := &Operation{
				OperationID: groupName + "_" + methodName,
				Tags:        []string{groupName},
				Summary:     summary,
				Description: description,
				Responses:   make(map[string]Response),
			}

			// Path parameters
			for _, pp := range pathParams {
				op.Parameters = append(op.Parameters, Parameter{
					Name:     pp.Name,
					In:       "path",
					Required: true,
					Schema:   g.goTypeToJSONSchema(pp.Info.Type),
				})
			}

			// Request body
			if bodyParam != nil {
				schema := g.goTypeToJSONSchema(bodyParam.Type)
				op.RequestBody = &RequestBody{
					Required: true,
					Content: map[string]MediaType{
						"application/json": {Schema: schema},
					},
				}
			}

			// Response
			if info.IsVoid {
				op.Responses["204"] = Response{Description: "No content"}
			} else {
				respSchema := g.goTypeToJSONSchema(info.ResponseType)
				op.Responses["200"] = Response{
					Description: "Successful response",
					Content: map[string]MediaType{
						"application/json": {Schema: respSchema},
					},
				}
			}

			// Error responses
			op.Responses["422"] = Response{Description: "Validation error"}
			op.Responses["500"] = Response{Description: "Internal server error"}

			// Add to path item
			fullPath := g.basePath + path
			pathItem, ok := spec.Paths[fullPath]
			if !ok {
				pathItem = &PathItem{}
				spec.Paths[fullPath] = pathItem
			}
			switch httpMethod {
			case HTTPGet:
				pathItem.Get = op
			case HTTPPost:
				pathItem.Post = op
			case HTTPPut:
				pathItem.Put = op
			case HTTPPatch:
				pathItem.Patch = op
			case HTTPDelete:
				pathItem.Delete = op
			}
		}
	}

	// Collect schemas into components
	for t, schema := range g.schemas {
		spec.Components.Schemas[t.Name()] = schema
	}

	return spec, nil
}

// GenerateJSON produces the OpenAPI spec as formatted JSON bytes.
func (g *OpenAPIGenerator) GenerateJSON() ([]byte, error) {
	spec, err := g.Generate()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(spec, "", "  ")
}

// goTypeToJSONSchema converts a Go reflect.Type to a JSON Schema.
func (g *OpenAPIGenerator) goTypeToJSONSchema(t reflect.Type) *JSONSchema {
	if t.Kind() == reflect.Ptr {
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
			// both encoding/json v1 and go-json-experiment/json v2 (issue
			// #174). OpenAPI 3.0 represents this as {type: string, format:
			// byte}.
			return &JSONSchema{Type: "string", Format: "byte"}
		}
		return &JSONSchema{
			Type:  "array",
			Items: g.goTypeToJSONSchema(t.Elem()),
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
		// Register as component schema and return $ref
		if _, exists := g.schemas[t]; !exists {
			g.buildStructSchema(t)
		}
		return &JSONSchema{Ref: "#/components/schemas/" + t.Name()}
	case reflect.Interface:
		return &JSONSchema{}
	default:
		return &JSONSchema{}
	}
}

// byteSliceFieldSchema returns the JSON Schema for a byte-slice field,
// honoring the go-json-experiment/json v2 `format:` tag (issue #174).
// Returns nil if the field type is not a byte slice — caller should fall
// through to the default goTypeToJSONSchema path.
func byteSliceFieldSchema(field reflect.StructField) *JSONSchema {
	if !isByteSlice(field.Type) {
		return nil
	}
	format := jsonFormatOption(field)
	if format != "" {
		if _, _, _, ok := byteSliceFormatShape(format); ok {
			if format == "array" {
				return &JSONSchema{Type: "array", Items: &JSONSchema{Type: "integer"}}
			}
			return &JSONSchema{Type: "string", Format: "byte"}
		}
		// Unrecognized format tag: fall through to the default shape.
	}
	if isUnnamedByteSlice(field.Type) {
		return &JSONSchema{Type: "string", Format: "byte"}
	}
	// Named byte slice with no tag keeps the v2 number-array default.
	return &JSONSchema{Type: "array", Items: &JSONSchema{Type: "integer"}}
}

// buildStructSchema builds a JSON Schema for a struct type and registers it.
func (g *OpenAPIGenerator) buildStructSchema(t reflect.Type) {
	schema := &JSONSchema{
		Type:        "object",
		Properties:  make(map[string]*JSONSchema),
		Description: g.meta.typeDoc(t.Name()),
	}
	// Register early to handle circular references
	g.schemas[t] = schema

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
			if ft.Kind() == reflect.Ptr {
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
		isOptional := strings.Contains(jsonTag, "omitempty") || field.Type.Kind() == reflect.Ptr
		if !isOptional {
			schema.Required = append(schema.Required, jsonName)
		}
	}

	sort.Strings(schema.Required)
}

// buildFieldsInto adds struct fields to an existing schema (for embedded struct flattening).
func (g *OpenAPIGenerator) buildFieldsInto(t reflect.Type, schema *JSONSchema) {
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
		isOptional := strings.Contains(jsonTag, "omitempty") || field.Type.Kind() == reflect.Ptr
		if !isOptional {
			schema.Required = append(schema.Required, jsonName)
		}
	}
}

// jsonFieldName gets the JSON field name from a struct field.
func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" {
		return field.Name
	}
	return parts[0]
}

// enumToJSONSchema converts an enum to a JSON Schema with enum values.
func (g *OpenAPIGenerator) enumToJSONSchema(info *EnumInfo) *JSONSchema {
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

// applyValidateConstraints maps validate struct tags to JSON Schema constraints.
func applyValidateConstraints(schema *JSONSchema, tag string, t reflect.Type) {
	rules := ParseValidateTag(tag)
	kind := t.Kind()
	if kind == reflect.Ptr {
		kind = t.Elem().Kind()
	}
	isString := kind == reflect.String

	for _, r := range rules {
		switch r.Tag {
		case "min":
			n := parseFloat(r.Param)
			if isString {
				intN := int(n)
				schema.MinLength = &intN
			} else {
				schema.Minimum = &n
			}
		case "max":
			n := parseFloat(r.Param)
			if isString {
				intN := int(n)
				schema.MaxLength = &intN
			} else {
				schema.Maximum = &n
			}
		case "len":
			n := int(parseFloat(r.Param))
			schema.MinLength = &n
			schema.MaxLength = &n
		case "gte":
			n := parseFloat(r.Param)
			schema.Minimum = &n
		case "gt":
			n := parseFloat(r.Param)
			schema.ExclusiveMinimum = &n
		case "lte":
			n := parseFloat(r.Param)
			schema.Maximum = &n
		case "lt":
			n := parseFloat(r.Param)
			schema.ExclusiveMaximum = &n
		case "email":
			schema.Format = "email"
		case "url":
			schema.Format = "uri"
		case "uuid":
			schema.Format = "uuid"
		case "alpha":
			schema.Pattern = "^[a-zA-Z]+$"
		case "alphanum":
			schema.Pattern = "^[a-zA-Z0-9]+$"
		case "oneof":
			values := strings.Split(r.Param, " ")
			for _, v := range values {
				schema.Enum = append(schema.Enum, v)
			}
		}
	}
}

// parseFloat parses a string as float64, returning 0 on failure.
func parseFloat(s string) float64 {
	var f float64
	_ = json.Unmarshal([]byte(s), &f)
	return f
}
