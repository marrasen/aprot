package aprot

import (
	"encoding/json"
	"fmt"
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
	Type       string                 `json:"type,omitempty"`
	Format     string                 `json:"format,omitempty"`
	Properties map[string]*JSONSchema `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
	Items      *JSONSchema            `json:"items,omitempty"`
	Enum       []any                  `json:"enum,omitempty"`
	Ref        string                 `json:"$ref,omitempty"`
	MinLength  *int                   `json:"minLength,omitempty"`
	MaxLength  *int                   `json:"maxLength,omitempty"`
	MinItems   *int                   `json:"minItems,omitempty"`
	MaxItems   *int                   `json:"maxItems,omitempty"`
	Minimum    *float64               `json:"minimum,omitempty"`
	Maximum    *float64               `json:"maximum,omitempty"`
	// ExclusiveMinimum/Maximum are booleans in OpenAPI 3.0.3 (modifiers on
	// minimum/maximum), unlike the numeric form added in 3.1. This spec
	// declares 3.0.3, so they must be booleans paired with Minimum/Maximum.
	ExclusiveMinimum     *bool       `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *bool       `json:"exclusiveMaximum,omitempty"`
	Pattern              string      `json:"pattern,omitempty"`
	Description          string      `json:"description,omitempty"`
	Nullable             bool        `json:"nullable,omitempty"`
	AdditionalProperties *JSONSchema `json:"additionalProperties,omitempty"`
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
	meta     *sourceMeta // godoc and parameter names, populated by Generate
	sg       *schemaGen  // shared schema builder in ref mode, populated by Generate
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

	// Resolve godoc metadata (parameter names and doc comments): baked docs
	// when the registry has them, AST extraction from source otherwise.
	g.meta = g.registry.resolveSourceMeta()
	g.sg = &schemaGen{registry: g.registry, meta: g.meta, refs: g.schemas}

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
					// Match the REST adapter's fallback naming (arg0, arg1, …)
					// so OpenAPI path params line up with the actual routes and
					// multiple unnamed params don't collide on a single "arg".
					name := fmt.Sprintf("arg%d", i)
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
					Schema:   g.sg.goTypeToJSONSchema(pp.Info.Type),
				})
			}

			// Request body
			if bodyParam != nil {
				schema := g.sg.goTypeToJSONSchema(bodyParam.Type)
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
				respSchema := g.sg.goTypeToJSONSchema(info.ResponseType)
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
		spec.Components.Schemas[sanitizeTSIdent(t.Name())] = schema
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

// byteSliceFieldSchema returns the JSON Schema for a byte-slice or byte-array
// field, honoring the encoding/json/v2 `format:` tag (issue #174).
// Returns nil if the field type is not a byte slice or array — caller should
// fall through to the default goTypeToJSONSchema path.
func byteSliceFieldSchema(field reflect.StructField) *JSONSchema {
	if !isByteSlice(field.Type) && !isByteArray(field.Type) {
		return nil
	}
	format := jsonFormatOption(field)
	if format != "" {
		if _, _, _, ok := byteSliceFormatShape(format); ok {
			if format == "array" {
				schema := &JSONSchema{Type: "array", Items: &JSONSchema{Type: "integer"}}
				if isByteArray(field.Type) {
					// A [N]byte forced onto the number-array wire shape keeps
					// its fixed length (#240).
					n := field.Type.Len()
					schema.MinItems = &n
					schema.MaxItems = &n
				}
				return schema
			}
			return &JSONSchema{Type: "string", Format: "byte"}
		}
		// Unrecognized format tag: fall through to the default shape.
	}
	if isUnnamedByteSlice(field.Type) || isByteArray(field.Type) {
		return &JSONSchema{Type: "string", Format: "byte"}
	}
	// Named byte slice with no tag keeps the v2 number-array default.
	return &JSONSchema{Type: "array", Items: &JSONSchema{Type: "integer"}}
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

// applyValidateConstraints maps validate struct tags to JSON Schema constraints.
func applyValidateConstraints(schema *JSONSchema, tag string, t reflect.Type) {
	rules := ParseValidateTag(tag)
	kind := t.Kind()
	if kind == reflect.Ptr {
		kind = t.Elem().Kind()
	}
	isString := kind == reflect.String
	isArray := kind == reflect.Slice || kind == reflect.Array

	for _, r := range rules {
		switch r.Tag {
		case "min":
			n := parseFloat(r.Param)
			switch {
			case isString:
				intN := int(n)
				schema.MinLength = &intN
			case isArray:
				intN := int(n)
				schema.MinItems = &intN
			default:
				schema.Minimum = &n
			}
		case "max":
			n := parseFloat(r.Param)
			switch {
			case isString:
				intN := int(n)
				schema.MaxLength = &intN
			case isArray:
				intN := int(n)
				schema.MaxItems = &intN
			default:
				schema.Maximum = &n
			}
		case "len":
			n := int(parseFloat(r.Param))
			if isArray {
				schema.MinItems = &n
				schema.MaxItems = &n
			} else {
				schema.MinLength = &n
				schema.MaxLength = &n
			}
		case "gte":
			n := parseFloat(r.Param)
			schema.Minimum = &n
		case "gt":
			n := parseFloat(r.Param)
			tru := true
			schema.Minimum = &n
			schema.ExclusiveMinimum = &tru
		case "lte":
			n := parseFloat(r.Param)
			schema.Maximum = &n
		case "lt":
			n := parseFloat(r.Param)
			tru := true
			schema.Maximum = &n
			schema.ExclusiveMaximum = &tru
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
