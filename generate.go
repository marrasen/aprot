package aprot

import (
	"embed"
	"encoding"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// MarshalTSType is the result of inferring a TypeScript type from a Go type's
// JSON marshaling behavior.
type MarshalTSType struct {
	TSType string // e.g. "string", "number", "boolean", "Record<string, number>", "string[]"
}

// InferTypeFromMarshal checks whether t implements json.Marshaler or
// encoding.TextMarshaler and, if so, marshals a zero value to determine the
// TypeScript type.  Returns nil when the type does not implement either
// interface, when marshaling produces null, or when the type is an interface.
func InferTypeFromMarshal(t reflect.Type) *MarshalTSType {
	if t.Kind() == reflect.Interface {
		return nil
	}

	// Check for TextMarshaler-only (no json.Marshaler).
	// encoding/json uses TextMarshaler to produce a JSON string.
	hasJSONMarshaler := t.Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType)
	hasTextMarshaler := t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)

	if !hasJSONMarshaler && hasTextMarshaler {
		return &MarshalTSType{TSType: "string"}
	}
	if !hasJSONMarshaler {
		return nil
	}

	// json.Marshaler: create zero value and marshal it.
	var data []byte
	func() {
		defer func() { _ = recover() }() // guard against panics on zero-value marshal
		v := reflect.New(t)
		var err error
		data, err = json.Marshal(v.Interface())
		if err != nil {
			data = nil
		}
	}()

	if len(data) == 0 {
		return nil
	}

	switch data[0] {
	case '"':
		return &MarshalTSType{TSType: "string"}
	case 't', 'f':
		return &MarshalTSType{TSType: "boolean"}
	case '{':
		return inferObjectType(data)
	case '[':
		return inferArrayType(data)
	case 'n':
		return nil
	default:
		// digit or '-' → number
		if (data[0] >= '0' && data[0] <= '9') || data[0] == '-' {
			return &MarshalTSType{TSType: "number"}
		}
		return nil
	}
}

// SQLNullTSType checks whether t is a database/sql nullable type (NullString,
// NullInt64, NullBool, NullFloat64, NullInt32, NullInt16, NullByte, NullTime,
// or the generic Null[T]) and returns the corresponding TypeScript type
// (e.g. "string | null"). Returns "" if t is not a sql.Null type.
//
// typeResolver is called for the generic Null[T] case to convert the inner
// Go type to its TypeScript representation.
func SQLNullTSType(t reflect.Type, typeResolver func(reflect.Type) string) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.PkgPath() != "database/sql" {
		return ""
	}
	switch t.Name() {
	case "NullString":
		return "string | null"
	case "NullInt64", "NullInt32", "NullInt16", "NullFloat64", "NullByte":
		return "number | null"
	case "NullBool":
		return "boolean | null"
	case "NullTime":
		return "string | null"
	default:
		// Generic sql.Null[T] — has fields V (of type T) and Valid (bool).
		if strings.HasPrefix(t.Name(), "Null[") {
			if vField, ok := t.FieldByName("V"); ok {
				return typeResolver(vField.Type) + " | null"
			}
		}
		return ""
	}
}

// inferObjectType unmarshals JSON object data and infers a Record<string, T> type.
// Returns Record<string, any> for empty or heterogeneous objects.
func inferObjectType(data []byte) *MarshalTSType {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	if len(obj) == 0 {
		return &MarshalTSType{TSType: "Record<string, any>"}
	}
	var common string
	for _, v := range obj {
		ts := jsonValueToTS(v)
		if common == "" {
			common = ts
		} else if ts != common {
			return &MarshalTSType{TSType: "Record<string, any>"}
		}
	}
	return &MarshalTSType{TSType: "Record<string, " + common + ">"}
}

// inferArrayType unmarshals JSON array data and infers a T[] type.
// Returns any[] for empty or heterogeneous arrays.
func inferArrayType(data []byte) *MarshalTSType {
	var arr []interface{}
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil
	}
	if len(arr) == 0 {
		return &MarshalTSType{TSType: "any[]"}
	}
	var common string
	for _, v := range arr {
		ts := jsonValueToTS(v)
		if common == "" {
			common = ts
		} else if ts != common {
			return &MarshalTSType{TSType: "any[]"}
		}
	}
	return &MarshalTSType{TSType: common + "[]"}
}

// jsonValueToTS maps a single JSON value (from json.Unmarshal into interface{})
// to a TypeScript type string. Nested objects and arrays return "any".
func jsonValueToTS(v interface{}) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	default:
		return "any"
	}
}

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"toLowerCamel": toLowerCamel,
	"toKebab":      toKebab,
	"join":         strings.Join,
	"hasParams": func(params []paramData) bool {
		return len(params) > 0
	},
	"paramDecl": func(params []paramData) string {
		var parts []string
		for _, p := range params {
			parts = append(parts, p.Name+": "+p.Type)
		}
		return strings.Join(parts, ", ")
	},
	"paramNames": func(params []paramData) string {
		var parts []string
		for _, p := range params {
			parts = append(parts, p.Name)
		}
		return strings.Join(parts, ", ")
	},
	"paramArray": func(params []paramData) string {
		var parts []string
		for _, p := range params {
			if p.Variadic {
				parts = append(parts, "..."+p.Name)
			} else {
				parts = append(parts, p.Name)
			}
		}
		return strings.Join(parts, ", ")
	},
	"hasMethodsWithParams": func(methods []methodData) bool {
		for _, m := range methods {
			if len(m.Params) > 0 {
				return true
			}
		}
		return false
	},
}).ParseFS(templateFS, "templates/*.tmpl"))

// OutputMode specifies the type of client code to generate.
type OutputMode string

const (
	OutputVanilla OutputMode = "vanilla"
	OutputReact   OutputMode = "react"
)

// GeneratorOptions configures the code generator.
type GeneratorOptions struct {
	// OutputDir is the directory to write generated files to.
	// If empty, files are written to current directory.
	OutputDir string

	// Mode specifies vanilla or react output.
	Mode OutputMode

	// Naming controls how Go names are transformed into TypeScript names.
	// If nil, DefaultNaming{} is used (preserving current behavior).
	Naming NamingPlugin
}

// Generator generates TypeScript client code from a registry.
type Generator struct {
	registry       *Registry
	options        GeneratorOptions
	types          map[reflect.Type]string
	collectedEnums map[reflect.Type]*EnumInfo // enums used by current handler
	marshalCache   map[reflect.Type]*MarshalTSType
	marshalChecked map[reflect.Type]bool
}

// NewGenerator creates a new TypeScript generator.
func NewGenerator(registry *Registry) *Generator {
	return &Generator{
		registry: registry,
		options: GeneratorOptions{
			Mode: OutputVanilla,
		},
		types:          make(map[reflect.Type]string),
		collectedEnums: make(map[reflect.Type]*EnumInfo),
		marshalCache:   make(map[reflect.Type]*MarshalTSType),
		marshalChecked: make(map[reflect.Type]bool),
	}
}

func (g *Generator) naming() NamingPlugin {
	if g.options.Naming != nil {
		return g.options.Naming
	}
	return DefaultNaming{}
}

// WithOptions sets generator options.
func (g *Generator) WithOptions(opts GeneratorOptions) *Generator {
	g.options = opts
	if g.options.Mode == "" {
		g.options.Mode = OutputVanilla
	}
	return g
}

// typeImportGroup represents a set of type names to import from a single module.
type typeImportGroup struct {
	Module string   // e.g., "./client", "./api"
	Names  []string // sorted type names
}

// templateData holds all data needed for template rendering.
type templateData struct {
	StructName       string
	FileName         string
	Interfaces       []interfaceData
	Methods          []methodData
	PushEvents       []pushEventData
	CustomErrorCodes []errorCodeData
	Enums            []enumTemplateData
	BaseTypeImports  []string          // base type names to import from './client' (handler files only)
	SharedImports    []typeImportGroup // shared type imports from package files (e.g., "./api")
}

type enumTemplateData struct {
	Name     string
	IsString bool
	Values   []enumValueTemplateData
}

type enumValueTemplateData struct {
	Name  string // e.g., "Pending"
	Value string // e.g., `"pending"` (quoted) or `0`
}

type interfaceData struct {
	Name   string
	Fields []fieldData
}

type fieldData struct {
	Name     string
	Type     string
	Optional bool
}

type paramData struct {
	Name     string // parameter name (from AST or "arg0")
	Type     string // TypeScript type string
	Variadic bool
}

type methodData struct {
	Name          string // short method name (e.g., "CreateUser")
	WireMethod    string // qualified wire name (e.g., "PublicHandlers.CreateUser")
	MethodName    string // camelCase function name (e.g., "createUser")
	HookName      string // React hook name (e.g., "useCreateUser")
	SubscribeName string // subscribe function name (e.g., "subscribeCreateUser")
	ResponseType  string
	IsVoid        bool
	Params        []paramData
}

type pushEventData struct {
	Name        string
	HandlerName string
	HookName    string
	DataType    string
}

type errorCodeData struct {
	Name       string // e.g., "EndOfFile"
	Code       int    // e.g., 1000
	MethodName string // e.g., "isEndOfFile"
}

// Generate writes TypeScript client code for all handler groups.
// Returns a map of filename to content, or writes to OutputDir if set.
// Generates:
//   - client.ts: Base client with ApiClient, ApiError, ErrorCode, etc.
//   - {handler-name}.ts: Handler-specific interfaces and methods for each handler group
func (g *Generator) Generate() (map[string]string, error) {
	results := make(map[string]string)

	// Phase 1: Pre-scan all groups to find types shared across 2+ groups.
	// Shared types go into per-package .ts files; group-specific types stay in handler files.
	typeGroups := make(map[reflect.Type]map[string]bool)
	enumGroups := make(map[reflect.Type]map[string]bool)

	for _, group := range g.registry.Groups() {
		g.types = make(map[reflect.Type]string)
		g.collectedEnums = make(map[reflect.Type]*EnumInfo)
		g.collectGroupTypes(group)

		for t := range g.types {
			if typeGroups[t] == nil {
				typeGroups[t] = make(map[string]bool)
			}
			typeGroups[t][group.Name] = true
		}
		for t := range g.collectedEnums {
			if enumGroups[t] == nil {
				enumGroups[t] = make(map[string]bool)
			}
			enumGroups[t][group.Name] = true
		}
	}

	// Group shared types by Go package name.
	// sharedTypesByPkg maps package name -> set of reflect.Types
	sharedTypesByPkg := make(map[string]map[reflect.Type]bool)
	sharedEnumsByPkg := make(map[string]map[reflect.Type]bool)
	sharedTypeSet := make(map[reflect.Type]bool)
	sharedEnumSet := make(map[reflect.Type]bool)

	for t, groups := range typeGroups {
		if len(groups) > 1 {
			pkg := pkgShortName(t.PkgPath())
			if sharedTypesByPkg[pkg] == nil {
				sharedTypesByPkg[pkg] = make(map[reflect.Type]bool)
			}
			sharedTypesByPkg[pkg][t] = true
			sharedTypeSet[t] = true
		}
	}
	for t, groups := range enumGroups {
		if len(groups) > 1 {
			pkg := pkgShortName(t.PkgPath())
			if sharedEnumsByPkg[pkg] == nil {
				sharedEnumsByPkg[pkg] = make(map[reflect.Type]bool)
			}
			sharedEnumsByPkg[pkg][t] = true
			sharedEnumSet[t] = true
		}
	}

	// Enums registered via RegisterEnum (not tied to any handler group) always
	// go into shared per-package files.
	for i := range g.registry.SharedEnums() {
		ei := &g.registry.SharedEnums()[i]
		t := ei.Type
		pkg := pkgShortName(t.PkgPath())
		if sharedEnumsByPkg[pkg] == nil {
			sharedEnumsByPkg[pkg] = make(map[reflect.Type]bool)
		}
		sharedEnumsByPkg[pkg][t] = true
		sharedEnumSet[t] = true
	}

	// Generate a {pkg}.ts file for each package that has shared types.
	// Build a name -> module map for handler import resolution.
	sharedTypeNames := make(map[string]string) // type name -> module (e.g., "User" -> "./api")
	// Collect all package names (union of types and enums) for deterministic iteration
	allSharedPkgs := make(map[string]bool)
	for pkg := range sharedTypesByPkg {
		allSharedPkgs[pkg] = true
	}
	for pkg := range sharedEnumsByPkg {
		allSharedPkgs[pkg] = true
	}
	sortedPkgs := make([]string, 0, len(allSharedPkgs))
	for pkg := range allSharedPkgs {
		sortedPkgs = append(sortedPkgs, pkg)
	}
	sort.Strings(sortedPkgs)

	for _, pkg := range sortedPkgs {
		g.types = make(map[reflect.Type]string)
		g.collectedEnums = make(map[reflect.Type]*EnumInfo)

		if types, ok := sharedTypesByPkg[pkg]; ok {
			for t := range types {
				g.types[t] = t.Name()
			}
		}
		if enums, ok := sharedEnumsByPkg[pkg]; ok {
			for t := range enums {
				if enumInfo := g.registry.GetEnum(t); enumInfo != nil {
					g.collectedEnums[t] = enumInfo
				}
			}
		}

		pkgData := templateData{
			StructName: pkg,
			FileName:   pkg + ".ts",
		}
		pkgData.Interfaces = g.buildInterfaces()
		pkgData.Enums = g.buildEnums()

		module := "./" + pkg
		for _, iface := range pkgData.Interfaces {
			sharedTypeNames[iface.Name] = module
		}
		for _, enum := range pkgData.Enums {
			sharedTypeNames[enum.Name+"Type"] = module
		}

		var buf strings.Builder
		if err := templates.ExecuteTemplate(&buf, "client-shared.ts.tmpl", pkgData); err != nil {
			return nil, err
		}
		results[pkgData.FileName] = buf.String()
	}

	// Build base client data (no user types — just ApiClient, ApiError, ErrorCode, etc.)
	baseData := templateData{
		StructName: "Base",
		FileName:   "client.ts",
	}
	for _, ec := range g.registry.ErrorCodes() {
		baseData.CustomErrorCodes = append(baseData.CustomErrorCodes, errorCodeData{
			Name:       ec.Name,
			Code:       ec.Code,
			MethodName: g.naming().ErrorMethodName(ec.Name),
		})
	}

	// Build base type name set for handler import resolution (RequestOptions, PushHandler, etc.)
	baseTypeNames := make(map[string]bool)

	baseTemplateName := "client-base.ts.tmpl"
	if g.options.Mode == OutputReact {
		baseTemplateName = "client-base-react.ts.tmpl"
	}

	// Generate handler files
	handlerTemplateName := "client-handler.ts.tmpl"
	if g.options.Mode == OutputReact {
		handlerTemplateName = "client-handler-react.ts.tmpl"
	}

	// Extract param names from AST
	paramNames := g.extractAllParamNames()

	// Phase 2: handler groups
	for _, group := range g.registry.Groups() {
		g.types = make(map[reflect.Type]string)
		g.collectedEnums = make(map[reflect.Type]*EnumInfo)
		g.collectGroupTypes(group)

		// Exclude shared types (they live in per-package .ts files)
		for t := range sharedTypeSet {
			delete(g.types, t)
		}
		for t := range sharedEnumSet {
			delete(g.collectedEnums, t)
		}

		data := g.buildTemplateData(group, paramNames)
		data.BaseTypeImports = findBaseTypeImports(&data, baseTypeNames)
		data.SharedImports = findSharedTypeImports(&data, sharedTypeNames)

		var buf strings.Builder
		if err := templates.ExecuteTemplate(&buf, handlerTemplateName, data); err != nil {
			return nil, err
		}

		results[data.FileName] = buf.String()
	}

	// Render base client
	var baseBuf strings.Builder
	if err := templates.ExecuteTemplate(&baseBuf, baseTemplateName, baseData); err != nil {
		return nil, err
	}
	results["client.ts"] = baseBuf.String()

	// Run generate hooks
	for _, hook := range g.registry.generateHooks {
		hook(results, g.options.Mode)
	}

	// Write to files if OutputDir is set
	if g.options.OutputDir != "" {
		if err := os.MkdirAll(g.options.OutputDir, 0755); err != nil {
			return nil, err
		}
		for filename, content := range results {
			path := filepath.Join(g.options.OutputDir, filename)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return nil, err
			}
		}
	}

	return results, nil
}

// GenerateTo writes TypeScript client code to a single writer.
// This combines all handler groups into one file (legacy behavior).
func (g *Generator) GenerateTo(w io.Writer) error {
	// Collect all types from all groups
	for _, group := range g.registry.Groups() {
		for _, info := range group.Handlers {
			for _, param := range info.Params {
				g.collectNestedType(param.Type)
			}
			if info.ResponseType.Kind() == reflect.Struct && info.ResponseType != voidResponseType {
				g.collectType(info.ResponseType)
			} else {
				g.collectNestedType(info.ResponseType)
			}
		}
		for _, event := range group.PushEvents {
			g.collectType(event.DataType)
		}
		// Include enums explicitly registered for this group
		for i := range group.Enums {
			g.collectedEnums[group.Enums[i].Type] = &group.Enums[i]
		}
	}
	// Include shared enums (not tied to any handler group)
	for i := range g.registry.SharedEnums() {
		ei := &g.registry.SharedEnums()[i]
		g.collectedEnums[ei.Type] = ei
	}

	// Extract param names from AST
	paramNames := g.extractAllParamNames()

	// Build combined template data
	data := templateData{
		StructName: "Combined",
		FileName:   "client.ts",
	}

	// Build interfaces
	data.Interfaces = g.buildInterfaces()

	// Build methods from all groups
	handlers := g.registry.Handlers()
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, qualifiedName := range names {
		info := handlers[qualifiedName]
		shortName := info.Name
		isVoid := info.ResponseType == voidResponseType
		respType := "void"
		if !isVoid {
			respType = g.goTypeToTS(info.ResponseType)
		}
		data.Methods = append(data.Methods, methodData{
			Name:          shortName,
			WireMethod:    qualifiedName,
			MethodName:    g.naming().MethodName(shortName),
			HookName:      g.naming().HookName(shortName),
			SubscribeName: "subscribe" + shortName,
			ResponseType:  respType,
			IsVoid:        isVoid,
			Params:        g.buildParamData(info, paramNames),
		})
	}

	// Build push events from all groups
	for _, event := range g.registry.PushEvents() {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: g.naming().HandlerName(event.Name),
			HookName:    g.naming().HookName(event.Name),
			DataType:    event.DataType.Name(),
		})
	}

	// Build custom error codes
	for _, ec := range g.registry.ErrorCodes() {
		data.CustomErrorCodes = append(data.CustomErrorCodes, errorCodeData{
			Name:       ec.Name,
			Code:       ec.Code,
			MethodName: g.naming().ErrorMethodName(ec.Name),
		})
	}

	// Build enums
	data.Enums = g.buildEnums()

	templateName := "client.ts.tmpl"
	if g.options.Mode == OutputReact {
		templateName = "client-react.ts.tmpl"
	}

	// Execute template to a buffer so we can run hooks
	var buf strings.Builder
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return err
	}

	content := buf.String()

	// Run generate hooks (single-file mode: hooks append to the file content)
	if len(g.registry.generateHooks) > 0 {
		results := map[string]string{"client.ts": content}
		for _, hook := range g.registry.generateHooks {
			hook(results, g.options.Mode)
		}
		content = results["client.ts"]
	}

	_, err := io.WriteString(w, content)
	return err
}

func (g *Generator) buildTemplateData(group *HandlerGroup, paramNames map[string]map[string][]string) templateData {
	data := templateData{
		StructName: group.Name,
		FileName:   g.naming().FileName(group.Name) + ".ts",
	}

	// Build interfaces
	types := make([]reflect.Type, 0, len(g.types))
	for t := range g.types {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i].Name() < types[j].Name()
	})

	for _, t := range types {
		iface := interfaceData{Name: t.Name()}
		iface.Fields = g.collectInterfaceFields(t)
		data.Interfaces = append(data.Interfaces, iface)
	}

	// Build methods
	names := make([]string, 0, len(group.Handlers))
	for name := range group.Handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		info := group.Handlers[name]
		isVoid := info.ResponseType == voidResponseType
		respType := "void"
		if !isVoid {
			respType = g.goTypeToTS(info.ResponseType)
		}
		data.Methods = append(data.Methods, methodData{
			Name:          name,
			WireMethod:    group.Name + "." + name,
			MethodName:    g.naming().MethodName(name),
			HookName:      g.naming().HookName(name),
			SubscribeName: "subscribe" + name,
			ResponseType:  respType,
			IsVoid:        isVoid,
			Params:        g.buildParamData(info, paramNames),
		})
	}

	// Build push events
	for _, event := range group.PushEvents {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: g.naming().HandlerName(event.Name),
			HookName:    g.naming().HookName(event.Name),
			DataType:    event.DataType.Name(),
		})
	}

	// Build custom error codes
	for _, ec := range g.registry.ErrorCodes() {
		data.CustomErrorCodes = append(data.CustomErrorCodes, errorCodeData{
			Name:       ec.Name,
			Code:       ec.Code,
			MethodName: g.naming().ErrorMethodName(ec.Name),
		})
	}

	// Build enums (sorted by name for deterministic output)
	enumTypes := make([]reflect.Type, 0, len(g.collectedEnums))
	for t := range g.collectedEnums {
		enumTypes = append(enumTypes, t)
	}
	sort.Slice(enumTypes, func(i, j int) bool {
		return g.collectedEnums[enumTypes[i]].Name < g.collectedEnums[enumTypes[j]].Name
	})

	for _, t := range enumTypes {
		enumInfo := g.collectedEnums[t]
		ed := enumTemplateData{
			Name:     enumInfo.Name,
			IsString: enumInfo.IsString,
		}
		for _, v := range enumInfo.Values {
			var val string
			if enumInfo.IsString {
				val = fmt.Sprintf(`"%s"`, v.Value)
			} else {
				val = fmt.Sprintf("%d", v.Value)
			}
			ed.Values = append(ed.Values, enumValueTemplateData{
				Name:  v.Name,
				Value: val,
			})
		}
		data.Enums = append(data.Enums, ed)
	}

	return data
}

// buildInterfaces extracts sorted interface data from g.types.
func (g *Generator) buildInterfaces() []interfaceData {
	types := make([]reflect.Type, 0, len(g.types))
	for t := range g.types {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i].Name() < types[j].Name()
	})

	var ifaces []interfaceData
	for _, t := range types {
		iface := interfaceData{Name: t.Name()}
		iface.Fields = g.collectInterfaceFields(t)
		ifaces = append(ifaces, iface)
	}
	return ifaces
}

// buildEnums extracts sorted enum data from g.collectedEnums.
func (g *Generator) buildEnums() []enumTemplateData {
	enumTypes := make([]reflect.Type, 0, len(g.collectedEnums))
	for t := range g.collectedEnums {
		enumTypes = append(enumTypes, t)
	}
	sort.Slice(enumTypes, func(i, j int) bool {
		return g.collectedEnums[enumTypes[i]].Name < g.collectedEnums[enumTypes[j]].Name
	})

	var enums []enumTemplateData
	for _, t := range enumTypes {
		enumInfo := g.collectedEnums[t]
		ed := enumTemplateData{
			Name:     enumInfo.Name,
			IsString: enumInfo.IsString,
		}
		for _, v := range enumInfo.Values {
			var val string
			if enumInfo.IsString {
				val = fmt.Sprintf(`"%s"`, v.Value)
			} else {
				val = fmt.Sprintf("%d", v.Value)
			}
			ed.Values = append(ed.Values, enumValueTemplateData{
				Name:  v.Name,
				Value: val,
			})
		}
		enums = append(enums, ed)
	}
	return enums
}

// isIdentChar reports whether c is a valid Go/TypeScript identifier character.
func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// containsTypeName checks whether name appears as a complete identifier in typeStr,
// not as a substring of a longer identifier. For example, containsTypeName("UserProfile", "User")
// returns false, but containsTypeName("User[]", "User") returns true.
func containsTypeName(typeStr, name string) bool {
	idx := 0
	for {
		i := strings.Index(typeStr[idx:], name)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(name)
		startOk := start == 0 || !isIdentChar(typeStr[start-1])
		endOk := end == len(typeStr) || !isIdentChar(typeStr[end])
		if startOk && endOk {
			return true
		}
		idx = start + 1
	}
}

// findBaseTypeImports returns the set of base type names referenced by the handler's
// interface fields, method responses, and push event data types. These need to be
// imported from './client' in multi-file mode.
func findBaseTypeImports(data *templateData, baseTypeNames map[string]bool) []string {
	referenced := make(map[string]bool)
	for _, iface := range data.Interfaces {
		for _, field := range iface.Fields {
			for name := range baseTypeNames {
				if containsTypeName(field.Type, name) {
					referenced[name] = true
				}
			}
		}
	}
	for _, m := range data.Methods {
		for name := range baseTypeNames {
			if containsTypeName(m.ResponseType, name) {
				referenced[name] = true
			}
		}
		for _, p := range m.Params {
			for name := range baseTypeNames {
				if containsTypeName(p.Type, name) {
					referenced[name] = true
				}
			}
		}
	}
	for _, ev := range data.PushEvents {
		for name := range baseTypeNames {
			if containsTypeName(ev.DataType, name) {
				referenced[name] = true
			}
		}
	}
	result := make([]string, 0, len(referenced))
	for name := range referenced {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// pkgShortName extracts the last path element from a Go package path.
// e.g., "github.com/user/project/api" -> "api"
func pkgShortName(pkgPath string) string {
	if i := strings.LastIndex(pkgPath, "/"); i >= 0 {
		return pkgPath[i+1:]
	}
	return pkgPath
}

// findSharedTypeImports scans handler template data for references to shared type names
// and returns import groups organized by module. sharedTypeNames maps type name -> module.
func findSharedTypeImports(data *templateData, sharedTypeNames map[string]string) []typeImportGroup {
	// Collect referenced type names grouped by module
	byModule := make(map[string]map[string]bool)
	scanRef := func(text string) {
		for name, module := range sharedTypeNames {
			if containsTypeName(text, name) {
				if byModule[module] == nil {
					byModule[module] = make(map[string]bool)
				}
				byModule[module][name] = true
			}
		}
	}

	for _, iface := range data.Interfaces {
		for _, field := range iface.Fields {
			scanRef(field.Type)
		}
	}
	for _, m := range data.Methods {
		scanRef(m.ResponseType)
		for _, p := range m.Params {
			scanRef(p.Type)
		}
	}
	for _, ev := range data.PushEvents {
		scanRef(ev.DataType)
	}

	// Build sorted result
	modules := make([]string, 0, len(byModule))
	for m := range byModule {
		modules = append(modules, m)
	}
	sort.Strings(modules)

	groups := make([]typeImportGroup, 0, len(modules))
	for _, m := range modules {
		names := make([]string, 0, len(byModule[m]))
		for n := range byModule[m] {
			names = append(names, n)
		}
		sort.Strings(names)
		groups = append(groups, typeImportGroup{Module: m, Names: names})
	}
	return groups
}

// collectInterfaceFields collects fields from a struct, flattening anonymous (embedded)
// struct fields to match encoding/json marshaling behavior.
func (g *Generator) collectInterfaceFields(t reflect.Type) []fieldData {
	var fields []fieldData
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() || shouldSkipField(field) {
			continue
		}
		if field.Anonymous {
			ft := field.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				fields = append(fields, g.collectInterfaceFields(ft)...)
				continue
			}
		}
		fields = append(fields, fieldData{
			Name:     g.getJSONName(field),
			Type:     g.goTypeToTS(field.Type),
			Optional: g.isOptional(field),
		})
	}
	return fields
}

func (g *Generator) collectType(t reflect.Type) {
	if t == nil || t == voidResponseType {
		return
	}
	if _, ok := g.types[t]; ok {
		return
	}
	g.types[t] = t.Name()

	// Recursively collect field types, flattening embedded structs
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			ft := field.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && !g.hasMarshalOverride(ft) {
				// Don't register as separate interface — fields are flattened.
				// But recurse into its fields to collect nested types.
				for j := 0; j < ft.NumField(); j++ {
					g.collectNestedType(ft.Field(j).Type)
				}
			}
			continue
		}
		g.collectNestedType(field.Type)
	}
}

// collectGroupTypes collects all types and enums used by a handler group's
// handlers, push events, and explicitly registered enums into g.types and
// g.collectedEnums. Caller must initialize g.types and g.collectedEnums first.
func (g *Generator) collectGroupTypes(group *HandlerGroup) {
	for _, info := range group.Handlers {
		for _, param := range info.Params {
			g.collectNestedType(param.Type)
		}
		if info.ResponseType.Kind() == reflect.Struct && info.ResponseType != voidResponseType {
			g.collectType(info.ResponseType)
		} else {
			g.collectNestedType(info.ResponseType)
		}
	}
	for _, event := range group.PushEvents {
		g.collectType(event.DataType)
	}
	for i := range group.Enums {
		g.collectedEnums[group.Enums[i].Type] = &group.Enums[i]
	}
}

// collectNestedType recursively collects named types (structs, enums) from
// arbitrarily nested type expressions like []*Struct, map[string][]Struct, etc.
func (g *Generator) collectNestedType(ft reflect.Type) {
	if ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}

	if enumInfo := g.registry.GetEnum(ft); enumInfo != nil {
		g.collectedEnums[ft] = enumInfo
	}

	switch ft.Kind() {
	case reflect.Struct:
		if ft.PkgPath() != "" && !g.hasMarshalOverride(ft) {
			g.collectType(ft)
		}
	case reflect.Slice:
		g.collectNestedType(ft.Elem())
	case reflect.Map:
		g.collectNestedType(ft.Elem())
	}
}

func (g *Generator) getJSONName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name // match encoding/json: use field name as-is
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" {
		return field.Name // tag like ",omitempty" — use field name as-is
	}
	return parts[0]
}

func (g *Generator) isOptional(field reflect.StructField) bool {
	tag := field.Tag.Get("json")
	if strings.Contains(tag, "omitempty") {
		return true
	}
	if field.Type.Kind() == reflect.Ptr {
		return true
	}
	return false
}

// inferTypeFromMarshalCached wraps InferTypeFromMarshal with per-Generator caching.
func (g *Generator) inferTypeFromMarshalCached(t reflect.Type) *MarshalTSType {
	if g.marshalChecked[t] {
		return g.marshalCache[t]
	}
	result := InferTypeFromMarshal(t)
	g.marshalChecked[t] = true
	g.marshalCache[t] = result
	return result
}

// hasMarshalOverride returns true if t has a custom JSON/text marshaler that
// overrides the default struct serialization. Used by type collection to skip
// struct-field recursion for types whose wire format differs from their Go fields.
func (g *Generator) hasMarshalOverride(t reflect.Type) bool {
	return g.inferTypeFromMarshalCached(t) != nil || SQLNullTSType(t, g.goTypeToTS) != ""
}

// shouldSkipField returns true for fields that should be excluded from reflected interfaces.
// Fields with json:"-" are skipped to match encoding/json behavior.
func shouldSkipField(field reflect.StructField) bool {
	return field.Tag.Get("json") == "-"
}

func (g *Generator) goTypeToTS(t reflect.Type) string {
	// Check if this is a registered enum type
	if enumInfo := g.registry.GetEnum(t); enumInfo != nil {
		return enumInfo.Name + "Type"
	}
	// Also check collected enums (for built-in protocol enums like TaskNodeStatus)
	if enumInfo, ok := g.collectedEnums[t]; ok {
		return enumInfo.Name + "Type"
	}

	// Check if the type has a custom JSON/text marshaler that produces a primitive.
	if mt := g.inferTypeFromMarshalCached(t); mt != nil {
		// For slice-kind types with marshalers (e.g. NonNilSlice[T]), refine
		// the element type using Go reflection instead of the zero-value marshal
		// output, which can only produce any[].
		if t.Kind() == reflect.Slice && strings.HasSuffix(mt.TSType, "[]") {
			elemType := g.goTypeToTS(t.Elem())
			if strings.Contains(elemType, " | ") {
				return "(" + elemType + ")[]"
			}
			return elemType + "[]"
		}
		return mt.TSType
	}

	// Check for database/sql nullable types.
	if tsType := SQLNullTSType(t, g.goTypeToTS); tsType != "" {
		return tsType
	}

	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice:
		elemType := g.goTypeToTS(t.Elem())
		if strings.Contains(elemType, " | ") {
			return "(" + elemType + ")[]"
		}
		return elemType + "[]"
	case reflect.Map:
		keyType := g.goTypeToTS(t.Key())
		valType := g.goTypeToTS(t.Elem())
		return "Record<" + keyType + ", " + valType + ">"
	case reflect.Ptr:
		return g.goTypeToTS(t.Elem())
	case reflect.Struct:
		if t.PkgPath() == "" {
			return "any"
		}
		return t.Name()
	case reflect.Interface:
		return "any"
	default:
		return "any"
	}
}

func toLowerCamel(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func toKebab(s string) string {
	var result strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				result.WriteRune('-')
			}
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// buildParamData converts HandlerInfo params to template paramData using AST names.
func (g *Generator) buildParamData(info *HandlerInfo, paramNames map[string]map[string][]string) []paramData {
	if len(info.Params) == 0 {
		return nil
	}

	// Look up AST names
	var astNames []string
	if methods, ok := paramNames[info.StructName]; ok {
		astNames = methods[info.Name]
	}

	params := make([]paramData, len(info.Params))
	for i, p := range info.Params {
		name := fmt.Sprintf("arg%d", i)
		if i < len(astNames) {
			name = astNames[i]
		}
		tsType := g.goTypeToTS(p.Type)
		if p.Variadic {
			tsType = tsType + "[]"
		}
		params[i] = paramData{
			Name:     name,
			Type:     tsType,
			Variadic: p.Variadic,
		}
	}
	return params
}

// extractAllParamNames extracts parameter names from source files for all handler groups.
// Returns structName → methodName → []paramName.
func (g *Generator) extractAllParamNames() map[string]map[string][]string {
	// Collect unique source directories from all groups
	dirs := make(map[string]bool)
	for _, group := range g.registry.Groups() {
		if dir := group.SourceDir(); dir != "" {
			dirs[dir] = true
		}
	}

	return extractParamNames(dirs)
}

// extractParamNames parses Go source files and extracts parameter names for handler methods.
// Returns structName → methodName → []paramName (excluding the first context.Context param).
func extractParamNames(dirs map[string]bool) map[string]map[string][]string {
	result := make(map[string]map[string][]string)

	for dir := range dirs {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, nil, 0)
		if err != nil {
			continue
		}
		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				for _, decl := range file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
						continue
					}

					// Get struct name from receiver
					recvType := fn.Recv.List[0].Type
					if star, ok := recvType.(*ast.StarExpr); ok {
						recvType = star.X
					}
					ident, ok := recvType.(*ast.Ident)
					if !ok {
						continue
					}
					structName := ident.Name
					methodName := fn.Name.Name

					// Extract parameter names, skipping the first (context.Context) param
					var names []string
					if fn.Type.Params != nil {
						first := true
						for _, field := range fn.Type.Params.List {
							if first {
								first = false
								continue
							}
							if len(field.Names) == 0 {
								names = append(names, "arg")
							} else {
								for _, n := range field.Names {
									names = append(names, n.Name)
								}
							}
						}
					}

					if result[structName] == nil {
						result[structName] = make(map[string][]string)
					}
					result[structName][methodName] = names
				}
			}
		}
	}

	return result
}
