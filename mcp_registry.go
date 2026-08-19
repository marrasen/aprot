package aprot

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// MCPTool configures one handler method's exposure as an MCP tool. The zero
// value is valid: the tool name is derived from the method's wire name, and
// the description falls back to the handler's godoc.
//
// The hint fields carry MCP tool annotations — behavioral metadata a client
// uses to decide, for example, whether calling the tool needs user
// confirmation. All four are emitted explicitly by the adapter, so set them
// deliberately: ReadOnly for pure queries, Destructive for irreversible
// updates, Idempotent when repeating a call with the same arguments has no
// additional effect, OpenWorld when the tool interacts with entities beyond
// the server (per the MCP spec, hints are advisory and must not gate
// security decisions).
type MCPTool struct {
	Name        string // tool name override; default snake_case of "Group.Method" (e.g. user_handlers_create_user)
	Title       string // human-readable display name
	Description string // description override; default is the handler's godoc
	ReadOnly    bool   // tool does not modify its environment
	Destructive bool   // tool may perform destructive updates
	Idempotent  bool   // repeated calls with the same arguments have no additional effect
	OpenWorld   bool   // tool interacts with external entities
}

// MCPOptions configures EnableMCP. Tools is keyed by Go method name;
// only listed methods are exposed.
type MCPOptions struct {
	Tools map[string]MCPTool
}

// EnableMCP marks selected methods of an already-registered handler group
// for exposure as MCP tools, mirroring how EnableREST marks a group for
// REST. Exposure is deliberately per-method opt-in rather than
// registry-wide: signatures designed for a typed TypeScript client are often
// hostile to a model (integer foreign keys, dozens of listing methods
// burning context), so consumers curate a subset and set model-facing names,
// descriptions and behavior hints here.
//
// The handler must have been registered first (Register or RegisterREST);
// unknown groups, unknown methods and streaming methods panic at
// registration time. The registered tools are served by the aprot/mcp
// adapter, which dispatches through [Server.Invoke] — the same pipeline,
// middleware and auth as every other transport.
func (r *Registry) EnableMCP(handler any, opts MCPOptions) {
	t := reflect.TypeOf(handler)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	group, ok := r.groups[name]
	if !ok {
		panic("aprot: EnableMCP called with unregistered handler: " + name)
	}
	if r.mcpTools == nil {
		r.mcpTools = make(map[string]MCPTool)
	}
	for methodName, tool := range opts.Tools {
		info, ok := group.Handlers[methodName]
		if !ok {
			panic("aprot: EnableMCP: no method " + methodName + " on handler " + name)
		}
		if info.Kind != HandlerKindUnary {
			panic("aprot: EnableMCP: streaming handler " + name + "." + methodName + " cannot be exposed as an MCP tool")
		}
		r.mcpTools[name+"."+methodName] = tool
	}
}

// MCPToolInfo is a fully resolved MCP tool descriptor: what the aprot/mcp
// adapter lists to clients and how it binds tool arguments back to the
// handler's positional parameters.
type MCPToolInfo struct {
	Name        string      // tool name (unique)
	Title       string      // optional display name
	Description string      // resolved description (override or handler godoc)
	Method      string      // wire method for Server.Invoke ("Group.Method")
	InputSchema *JSONSchema // object schema for the tool's arguments
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
	// Params is the argument-binding plan, in the handler's positional
	// order. When SingleStruct is true the arguments object itself is the
	// (single) struct parameter; otherwise each argument is looked up by
	// Params[i].Name.
	Params       []MCPToolParam
	SingleStruct bool
}

// MCPToolParam describes one handler parameter's binding from the MCP
// arguments object.
type MCPToolParam struct {
	Name     string // property name in InputSchema and in tool-call arguments
	Struct   bool   // parameter is a struct (bound as a nested object)
	Required bool   // non-pointer parameter: the argument must be present
}

// MCPTools resolves the tools registered via EnableMCP into full
// descriptors: names, descriptions (overrides or handler godoc), input
// schemas built by the same generator as OpenAPI, and argument-binding
// plans. Sorted by tool name. Duplicate tool names panic — names must be
// unique across the registry for MCP dispatch to be unambiguous.
func (r *Registry) MCPTools() []MCPToolInfo {
	if len(r.mcpTools) == 0 {
		return nil
	}
	meta := r.resolveSourceMeta()
	sg := &schemaGen{registry: r, meta: meta}

	tools := make([]MCPToolInfo, 0, len(r.mcpTools))
	seen := make(map[string]string, len(r.mcpTools))
	for wireMethod, tool := range r.mcpTools {
		info, ok := r.lookupMethod(wireMethod)
		if !ok {
			continue
		}

		ti := MCPToolInfo{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			Method:      wireMethod,
			ReadOnly:    tool.ReadOnly,
			Destructive: tool.Destructive,
			Idempotent:  tool.Idempotent,
			OpenWorld:   tool.OpenWorld,
		}
		if ti.Name == "" {
			ti.Name = toSnake(info.StructName) + "_" + toSnake(info.Name)
		}
		if ti.Description == "" {
			ti.Description = strings.TrimSpace(meta.handlerDoc(info.StructName, info.Name))
		}
		if prev, dup := seen[ti.Name]; dup {
			panic("aprot: MCP tool name " + ti.Name + " used by both " + prev + " and " + wireMethod)
		}
		seen[ti.Name] = wireMethod

		ti.Params, ti.SingleStruct, ti.InputSchema = mcpBinding(sg, meta, info)
		tools = append(tools, ti)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// mcpBinding builds the argument-binding plan and input schema for a
// handler. A single struct parameter is the model-friendly common case: the
// arguments object is the struct itself. Any other shape maps each
// positional parameter to a named property (names from godoc extraction,
// argN fallback), with struct parameters nested under their name.
func mcpBinding(sg *schemaGen, meta *sourceMeta, info *HandlerInfo) ([]MCPToolParam, bool, *JSONSchema) {
	isStruct := func(t reflect.Type) bool {
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		return t.Kind() == reflect.Struct
	}

	if len(info.Params) == 1 && isStruct(info.Params[0].Type) {
		schema := sg.goTypeToJSONSchema(info.Params[0].Type)
		if schema.Type != "object" {
			// Registered enum structs etc. — fall through to named binding.
		} else {
			p := MCPToolParam{Struct: true, Required: info.Params[0].Type.Kind() != reflect.Ptr}
			return []MCPToolParam{p}, true, schema
		}
	}

	astNames := meta.paramNames(info.StructName, info.Name)
	schema := &JSONSchema{Type: "object", Properties: make(map[string]*JSONSchema)}
	params := make([]MCPToolParam, 0, len(info.Params))
	for i := range info.Params {
		p := &info.Params[i]
		name := fmt.Sprintf("arg%d", i)
		if i < len(astNames) {
			name = astNames[i]
		}
		mp := MCPToolParam{
			Name:     name,
			Struct:   isStruct(p.Type),
			Required: p.Type.Kind() != reflect.Ptr,
		}
		params = append(params, mp)
		schema.Properties[name] = sg.goTypeToJSONSchema(p.Type)
		if mp.Required {
			schema.Required = append(schema.Required, name)
		}
	}
	sort.Strings(schema.Required)
	return params, false, schema
}

// toSnake converts CamelCase to snake_case, treating consecutive uppercase
// letters as one acronym word (RESTHandlers → rest_handlers).
func toSnake(s string) string {
	return strings.ReplaceAll(toKebabAcronyms(s), "-", "_")
}
