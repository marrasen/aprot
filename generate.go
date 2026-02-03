package aprot

import (
	"embed"
	"io"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var tsTemplate = template.Must(template.ParseFS(templateFS, "templates/client.ts.tmpl"))

// PushEventInfo describes a push event for code generation.
type PushEventInfo struct {
	Name     string
	DataType reflect.Type
}

// Generator generates TypeScript client code from a registry.
type Generator struct {
	registry   *Registry
	pushEvents []PushEventInfo
	types      map[reflect.Type]string
}

// NewGenerator creates a new TypeScript generator.
func NewGenerator(registry *Registry) *Generator {
	return &Generator{
		registry:   registry,
		pushEvents: []PushEventInfo{},
		types:      make(map[reflect.Type]string),
	}
}

// RegisterPushEvent registers a push event type for generation.
func (g *Generator) RegisterPushEvent(name string, dataType any) {
	t := reflect.TypeOf(dataType)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	g.pushEvents = append(g.pushEvents, PushEventInfo{
		Name:     name,
		DataType: t,
	})
}

// templateData holds all data needed for template rendering.
type templateData struct {
	Interfaces []interfaceData
	Methods    []methodData
	PushEvents []pushEventData
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
	RequestType  string
	ResponseType string
}

type pushEventData struct {
	Name        string
	HandlerName string
	DataType    string
}

// Generate writes the TypeScript client code to the writer.
func (g *Generator) Generate(w io.Writer) error {
	// Collect all types
	for _, info := range g.registry.Handlers() {
		g.collectType(info.RequestType)
		g.collectType(info.ResponseType)
	}
	for _, event := range g.pushEvents {
		g.collectType(event.DataType)
	}

	// Build template data
	data := g.buildTemplateData()

	// Execute template
	return tsTemplate.ExecuteTemplate(w, "client.ts.tmpl", data)
}

func (g *Generator) buildTemplateData() templateData {
	data := templateData{}

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
	handlers := g.registry.Handlers()
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		info := handlers[name]
		data.Methods = append(data.Methods, methodData{
			Name:         name,
			MethodName:   g.toLowerCamel(name),
			RequestType:  info.RequestType.Name(),
			ResponseType: info.ResponseType.Name(),
		})
	}

	// Build push events
	for _, event := range g.pushEvents {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: "on" + event.Name,
			DataType:    event.DataType.Name(),
		})
	}

	return data
}

func (g *Generator) collectType(t reflect.Type) {
	if t == nil {
		return
	}
	if _, ok := g.types[t]; ok {
		return
	}
	g.types[t] = t.Name()

	// Recursively collect field types
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "" {
			g.collectType(ft)
		}
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
			g.collectType(ft.Elem())
		}
	}
}

func (g *Generator) getJSONName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return g.toLowerCamel(field.Name)
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" || parts[0] == "-" {
		return g.toLowerCamel(field.Name)
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

func (g *Generator) toLowerCamel(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}
