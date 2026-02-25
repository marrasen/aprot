package tasks

import (
	"embed"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"time"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var taskTemplates = template.Must(
	template.New("").ParseFS(templateFS, "templates/*.tmpl"),
)

// taskTemplateData holds all data needed for task template rendering.
type taskTemplateData struct {
	IsMultiFile    bool
	IsReact        bool
	MetaInterfaces []metaInterfaceData
}

type metaInterfaceData struct {
	Name   string
	Fields []metaFieldData
}

type metaFieldData struct {
	Name     string
	Type     string
	Optional bool
}

// AppendConvenienceCode generates convenience TypeScript code for the task system.
// For multi-file mode it creates a tasks.ts file; for single-file mode it appends to client.ts.
func AppendConvenienceCode(results map[string]string, isReact bool, metaType reflect.Type) {
	_, hasHandlerFile := results["task-cancel-handler.ts"]
	isMultiFile := hasHandlerFile

	data := taskTemplateData{
		IsMultiFile: isMultiFile,
		IsReact:     isReact,
	}

	if metaType != nil {
		data.MetaInterfaces = buildMetaInterfaces(metaType)
	}

	var buf strings.Builder
	if err := taskTemplates.ExecuteTemplate(&buf, "tasks.ts.tmpl", data); err != nil {
		panic(fmt.Sprintf("tasks template execution failed: %v", err))
	}

	code := buf.String()

	if isMultiFile {
		results["tasks.ts"] = code
	} else {
		results["client.ts"] += code
	}
}

var timeType = reflect.TypeOf(time.Time{})

// buildMetaInterfaces converts a meta type to template data via reflection.
func buildMetaInterfaces(t reflect.Type) []metaInterfaceData {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	// Collect nested types first (depth-first so dependencies come first)
	var nested []reflect.Type
	collectNestedStructs(t, &nested, map[reflect.Type]bool{})

	var result []metaInterfaceData

	// Nested interfaces first
	for _, nt := range nested {
		result = append(result, buildInterfaceData(nt))
	}

	// Main interface
	result = append(result, buildInterfaceData(t))

	return result
}

func buildInterfaceData(t reflect.Type) metaInterfaceData {
	iface := metaInterfaceData{Name: t.Name()}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}

		name := field.Name
		optional := false
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				name = parts[0]
			}
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					optional = true
				}
			}
		}
		if field.Type.Kind() == reflect.Ptr {
			optional = true
		}

		iface.Fields = append(iface.Fields, metaFieldData{
			Name:     name,
			Type:     goTypeToTS(field.Type),
			Optional: optional,
		})
	}

	return iface
}

func collectNestedStructs(t reflect.Type, nested *[]reflect.Type, seen map[reflect.Type]bool) {
	if seen[t] {
		return
	}
	seen[t] = true

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Tag.Get("json") == "-" {
			continue
		}
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && ft != timeType {
			collectNestedStructs(ft, nested, seen)
			*nested = append(*nested, ft)
		}
	}
}

func goTypeToTS(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		return goTypeToTS(t.Elem())
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
		return goTypeToTS(t.Elem()) + "[]"
	case reflect.Map:
		return fmt.Sprintf("Record<%s, %s>", goTypeToTS(t.Key()), goTypeToTS(t.Elem()))
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
