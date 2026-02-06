package aprot

import (
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"toLowerCamel": toLowerCamel,
	"toKebab":      toKebab,
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
	StructName       string
	FileName         string
	Interfaces       []interfaceData
	Methods          []methodData
	PushEvents       []pushEventData
	CustomErrorCodes []errorCodeData
	Enums            []enumTemplateData
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

type methodData struct {
	Name         string
	MethodName   string
	HookName     string
	RequestType  string
	ResponseType string
	IsVoid       bool
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
	}
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

	for _, group := range g.registry.Groups() {
		g.types = make(map[reflect.Type]string)           // Reset types per group
		g.collectedEnums = make(map[reflect.Type]*EnumInfo) // Reset enums per group

		// Collect types for this group
		for _, info := range group.Handlers {
			g.collectType(info.RequestType)
			g.collectType(info.ResponseType)
		}
		for _, event := range group.PushEvents {
			g.collectType(event.DataType)
		}

		data := g.buildTemplateData(group)

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
			g.collectType(info.RequestType)
			g.collectType(info.ResponseType)
		}
		for _, event := range group.PushEvents {
			g.collectType(event.DataType)
		}
	}

	// Build combined template data
	data := templateData{
		StructName: "Combined",
		FileName:   "client.ts",
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
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			iface.Fields = append(iface.Fields, fieldData{
				Name:     g.getJSONName(field),
				Type:     g.goTypeToTS(field.Type),
				Optional: g.isOptional(field),
			})
		}
		data.Interfaces = append(data.Interfaces, iface)
	}

	// Build methods from all groups
	handlers := g.registry.Handlers()
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		info := handlers[name]
		isVoid := info.ResponseType == voidResponseType
		respType := info.ResponseType.Name()
		if isVoid {
			respType = "void"
		}
		data.Methods = append(data.Methods, methodData{
			Name:         name,
			MethodName:   toLowerCamel(name),
			HookName:     "use" + name,
			RequestType:  info.RequestType.Name(),
			ResponseType: respType,
			IsVoid:       isVoid,
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

	// Build enums
	for _, enumInfo := range g.collectedEnums {
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

func (g *Generator) buildTemplateData(group *HandlerGroup) templateData {
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
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			iface.Fields = append(iface.Fields, fieldData{
				Name:     g.getJSONName(field),
				Type:     g.goTypeToTS(field.Type),
				Optional: g.isOptional(field),
			})
		}
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
		respType := info.ResponseType.Name()
		if isVoid {
			respType = "void"
		}
		data.Methods = append(data.Methods, methodData{
			Name:         name,
			MethodName:   toLowerCamel(name),
			HookName:     "use" + name,
			RequestType:  info.RequestType.Name(),
			ResponseType: respType,
			IsVoid:       isVoid,
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

	// Build enums
	for _, enumInfo := range g.collectedEnums {
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

func (g *Generator) collectType(t reflect.Type) {
	if t == nil || t == voidResponseType {
		return
	}
	if _, ok := g.types[t]; ok {
		return
	}
	g.types[t] = t.Name()

	// Recursively collect field types
	for i := 0; i < t.NumField(); i++ {
		g.collectNestedType(t.Field(i).Type)
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
		if ft.PkgPath() != "" {
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
		return toLowerCamel(field.Name)
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" || parts[0] == "-" {
		return toLowerCamel(field.Name)
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
		if t.PkgPath() == "" {
			if t.String() == "time.Time" {
				return "string"
			}
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
