package aprot

import (
	"embed"
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
	"time"
	"unicode"
)

var timeType = reflect.TypeOf(time.Time{})

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"toLowerCamel": toLowerCamel,
	"toKebab":      toKebab,
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
}

// Generator generates TypeScript client code from a registry.
type Generator struct {
	registry       *Registry
	options        GeneratorOptions
	types          map[reflect.Type]string
	collectedEnums map[reflect.Type]*EnumInfo // enums used by current handler
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
	}
}

// WithOptions sets generator options.
func (g *Generator) WithOptions(opts GeneratorOptions) *Generator {
	g.options = opts
	if g.options.Mode == "" {
		g.options.Mode = OutputVanilla
	}
	return g
}

// templateData holds all data needed for template rendering.
type templateData struct {
	StructName         string
	FileName           string
	Interfaces         []interfaceData
	Methods            []methodData
	PushEvents         []pushEventData
	CustomErrorCodes   []errorCodeData
	Enums              []enumTemplateData
	HasTasks           bool
	TaskMetaType       string          // e.g. "TaskMeta", empty if no meta
	TaskMetaInterfaces []interfaceData // the meta type's interface definitions
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
	Name         string // short method name (e.g., "CreateUser")
	WireMethod   string // qualified wire name (e.g., "PublicHandlers.CreateUser")
	MethodName   string // camelCase function name (e.g., "createUser")
	HookName     string // React hook name (e.g., "useCreateUser")
	ResponseType string
	IsVoid       bool
	Params       []paramData
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

	// Generate base client file
	baseData := templateData{
		StructName: "Base",
		FileName:   "client.ts",
		HasTasks:   g.registry.tasksEnabled,
	}
	baseData.TaskMetaType, baseData.TaskMetaInterfaces = g.buildTaskMetaData()
	for _, ec := range g.registry.ErrorCodes() {
		baseData.CustomErrorCodes = append(baseData.CustomErrorCodes, errorCodeData{
			Name:       ec.Name,
			Code:       ec.Code,
			MethodName: "is" + ec.Name,
		})
	}

	var baseBuf strings.Builder
	baseTemplateName := "client-base.ts.tmpl"
	if g.options.Mode == OutputReact {
		baseTemplateName = "client-base-react.ts.tmpl"
	}

	if err := templates.ExecuteTemplate(&baseBuf, baseTemplateName, baseData); err != nil {
		return nil, err
	}
	results["client.ts"] = baseBuf.String()

	// Generate handler files
	handlerTemplateName := "client-handler.ts.tmpl"
	if g.options.Mode == OutputReact {
		handlerTemplateName = "client-handler-react.ts.tmpl"
	}

	// Extract param names from AST
	paramNames := g.extractAllParamNames()

	for _, group := range g.registry.Groups() {
		g.types = make(map[reflect.Type]string)           // Reset types per group
		g.collectedEnums = make(map[reflect.Type]*EnumInfo) // Reset enums per group

		// Collect types for this group
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

		data := g.buildTemplateData(group, paramNames)

		var buf strings.Builder
		if err := templates.ExecuteTemplate(&buf, handlerTemplateName, data); err != nil {
			return nil, err
		}

		results[data.FileName] = buf.String()
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
	}

	// Extract param names from AST
	paramNames := g.extractAllParamNames()

	// Build combined template data
	data := templateData{
		StructName: "Combined",
		FileName:   "client.ts",
		HasTasks:   g.registry.tasksEnabled,
	}
	data.TaskMetaType, data.TaskMetaInterfaces = g.buildTaskMetaData()

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
			Name:         shortName,
			WireMethod:   qualifiedName,
			MethodName:   toLowerCamel(shortName),
			HookName:     "use" + shortName,
			ResponseType: respType,
			IsVoid:       isVoid,
			Params:       g.buildParamData(info, paramNames),
		})
	}

	// Build push events from all groups
	for _, event := range g.registry.PushEvents() {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: "on" + event.Name,
			HookName:    "use" + event.Name,
			DataType:    event.DataType.Name(),
		})
	}

	// Build custom error codes
	for _, ec := range g.registry.ErrorCodes() {
		data.CustomErrorCodes = append(data.CustomErrorCodes, errorCodeData{
			Name:       ec.Name,
			Code:       ec.Code,
			MethodName: "is" + ec.Name,
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

	templateName := "client.ts.tmpl"
	if g.options.Mode == OutputReact {
		templateName = "client-react.ts.tmpl"
	}

	return templates.ExecuteTemplate(w, templateName, data)
}

func (g *Generator) buildTemplateData(group *HandlerGroup, paramNames map[string]map[string][]string) templateData {
	data := templateData{
		StructName: group.Name,
		FileName:   toKebab(group.Name) + ".ts",
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
			Name:         name,
			WireMethod:   group.Name + "." + name,
			MethodName:   toLowerCamel(name),
			HookName:     "use" + name,
			ResponseType: respType,
			IsVoid:       isVoid,
			Params:       g.buildParamData(info, paramNames),
		})
	}

	// Build push events
	for _, event := range group.PushEvents {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: "on" + event.Name,
			HookName:    "use" + event.Name,
			DataType:    event.DataType.Name(),
		})
	}

	// Build custom error codes
	for _, ec := range g.registry.ErrorCodes() {
		data.CustomErrorCodes = append(data.CustomErrorCodes, errorCodeData{
			Name:       ec.Name,
			Code:       ec.Code,
			MethodName: "is" + ec.Name,
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

// buildTaskMetaData builds the template data for the task meta type.
// Returns the meta type name and its interface definitions.
func (g *Generator) buildTaskMetaData() (string, []interfaceData) {
	metaType := g.registry.TaskMetaType()
	if metaType == nil {
		return "", nil
	}

	var ifaces []interfaceData
	g.collectTaskMetaType(metaType, &ifaces)
	return metaType.Name(), ifaces
}

// collectTaskMetaType recursively collects interfaces for the meta type and its nested structs.
func (g *Generator) collectTaskMetaType(t reflect.Type, ifaces *[]interfaceData) {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.PkgPath() == "" {
		return
	}

	// Avoid duplicates
	for _, iface := range *ifaces {
		if iface.Name == t.Name() {
			return
		}
	}

	iface := interfaceData{Name: t.Name()}
	iface.Fields = g.collectInterfaceFields(t)
	// Recurse into nested struct types (including from embedded fields)
	g.collectTaskMetaNestedTypes(t, ifaces)
	*ifaces = append(*ifaces, iface)
}

// collectTaskMetaNestedTypes recursively collects nested struct types from fields,
// handling embedded structs by recursing into their fields without registering
// the embedded type itself (since its fields are flattened into the parent).
func (g *Generator) collectTaskMetaNestedTypes(t reflect.Type, ifaces *[]interfaceData) {
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
			if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && ft != timeType {
				g.collectTaskMetaNestedTypes(ft, ifaces)
			}
			continue
		}
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && ft != timeType {
			g.collectTaskMetaType(ft, ifaces)
		}
		if ft.Kind() == reflect.Slice {
			et := ft.Elem()
			if et.Kind() == reflect.Ptr {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct && et.PkgPath() != "" && et != timeType {
				g.collectTaskMetaType(et, ifaces)
			}
		}
	}
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
			if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && ft != timeType {
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
		if ft.PkgPath() != "" && ft != timeType {
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

// shouldSkipField returns true for fields that should be excluded from reflected interfaces.
// Fields of type interface{} (any) are skipped because they produce unhelpful "any" types
// and are typically handled by hardcoded template definitions instead.
// Fields with json:"-" are skipped to match encoding/json behavior.
func shouldSkipField(field reflect.StructField) bool {
	if field.Type.Kind() == reflect.Interface {
		return true
	}
	return field.Tag.Get("json") == "-"
}

func (g *Generator) goTypeToTS(t reflect.Type) string {
	// Check if this is a registered enum type
	if enumInfo := g.registry.GetEnum(t); enumInfo != nil {
		return enumInfo.Name + "Type"
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
		return elemType + "[]"
	case reflect.Map:
		keyType := g.goTypeToTS(t.Key())
		valType := g.goTypeToTS(t.Elem())
		return "Record<" + keyType + ", " + valType + ">"
	case reflect.Ptr:
		return g.goTypeToTS(t.Elem())
	case reflect.Struct:
		if t == timeType {
			return "string"
		}
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
