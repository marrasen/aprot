package tasks

import (
	"embed"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"text/template"

	"github.com/marrasen/aprot"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var taskTemplates = template.Must(
	template.New("").ParseFS(templateFS, "templates/*.tmpl"),
)

// taskTemplateData holds all data needed for task template rendering.
type taskTemplateData struct {
	IsMultiFile   bool
	IsReact       bool
	MetaReExports []metaReExportData
}

// metaReExportData is one `export type { Names } from 'Module';` line in the
// generated tasks.ts.
type metaReExportData struct {
	Module string
	Names  string // comma-joined interface names declared by Module
}

// appendTaskConvenienceCode generates convenience TypeScript code for the task system.
func appendTaskConvenienceCode(results map[string]string, mode aprot.OutputMode, metaType reflect.Type) {
	_, hasHandlerFile := results["tasks-handler.ts"]
	isMultiFile := hasHandlerFile

	data := taskTemplateData{
		IsMultiFile: isMultiFile,
		IsReact:     mode == aprot.OutputReact,
	}

	if metaType != nil && isMultiFile {
		data.MetaReExports = buildMetaReExports(results, metaType)
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

// buildMetaReExports locates the meta interfaces among the already-generated
// files and groups them by declaring module. The main generator declares them
// where the meta fields live — tasks-handler.ts, or a shared per-package file
// when the meta type is also used by other handler groups. tasks.ts
// re-exports the names so consumer imports from './tasks' — where the
// interfaces lived before they were wired into the handler file — keep
// resolving. In single-file mode everything shares one module, so no
// re-export is needed.
func buildMetaReExports(results map[string]string, metaType reflect.Type) []metaReExportData {
	names := metaTypeNames(metaType)
	if len(names) == 0 {
		return nil
	}

	files := make([]string, 0, len(results))
	for f := range results {
		if strings.HasSuffix(f, ".ts") {
			files = append(files, f)
		}
	}
	sort.Strings(files)

	byModule := make(map[string][]string)
	var moduleOrder []string
	for _, name := range names {
		for _, f := range files {
			if !strings.Contains(results[f], "export interface "+name+" {") {
				continue
			}
			module := "./" + strings.TrimSuffix(f, ".ts")
			if _, ok := byModule[module]; !ok {
				moduleOrder = append(moduleOrder, module)
			}
			byModule[module] = append(byModule[module], name)
			break
		}
	}

	reExports := make([]metaReExportData, 0, len(moduleOrder))
	for _, module := range moduleOrder {
		reExports = append(reExports, metaReExportData{
			Module: module,
			Names:  strings.Join(byModule[module], ", "),
		})
	}
	return reExports
}

// hasMarshalOverride returns true if t has a custom JSON/text marshaler that
// overrides the default struct serialization.
func hasMarshalOverride(t reflect.Type) bool {
	return aprot.InferTypeFromMarshal(t) != nil || aprot.SQLNullTSType(t, goTypeToTS) != ""
}

// metaTypeNames returns the TS interface names the main generator declares
// for meta type t: nested struct names first, then t itself. Empty when t is
// not a struct (a map or slice meta produces an inline TS type, not a named
// interface).
func metaTypeNames(t reflect.Type) []string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var nested []reflect.Type
	collectNestedStructs(t, &nested, map[reflect.Type]bool{})

	names := make([]string, 0, len(nested)+1)
	for _, nt := range nested {
		names = append(names, nt.Name())
	}
	return append(names, t.Name())
}

// underlyingStructType peels pointer, slice, array, and map-value wrappers off
// t to reach the underlying element type. A struct used only as a map value
// (e.g. map[string]Info) must be reached so its interface gets declared,
// otherwise the generated TS references an undeclared type.
func underlyingStructType(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}
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
		ft := underlyingStructType(field.Type)
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && !hasMarshalOverride(ft) {
			collectNestedStructs(ft, nested, seen)
			*nested = append(*nested, ft)
		}
	}
}

// goTypeToTS maps a Go type to its TypeScript spelling. Meta interfaces are
// declared by the main generator (via Registry.OverrideFieldType), so this is
// only used as the kind resolver for SQL-null detection in hasMarshalOverride.
func goTypeToTS(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		return goTypeToTS(t.Elem())
	}

	// Check if the type has a custom JSON/text marshaler that produces a primitive.
	if mt := aprot.InferTypeFromMarshal(t); mt != nil {
		// For slice-kind types with marshalers (e.g. NonNilSlice[T]), refine
		// the element type using Go reflection instead of the zero-value marshal
		// output, which can only produce any[].
		if t.Kind() == reflect.Slice && strings.HasSuffix(mt.TSType, "[]") {
			elemType := goTypeToTS(t.Elem())
			if strings.Contains(elemType, " | ") {
				return "(" + elemType + ")[]"
			}
			return elemType + "[]"
		}
		return mt.TSType
	}

	// Check for database/sql nullable types.
	if tsType := aprot.SQLNullTSType(t, goTypeToTS); tsType != "" {
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
		elemType := goTypeToTS(t.Elem())
		if strings.Contains(elemType, " | ") {
			return "(" + elemType + ")[]"
		}
		return elemType + "[]"
	case reflect.Array:
		// [N]byte (named or not) is base64-encoded as a string by
		// go-json-experiment/json v2, same as unnamed []byte (#240).
		if t.Elem().Kind() == reflect.Uint8 {
			return "string"
		}
		length := t.Len()
		elemType := goTypeToTS(t.Elem())
		// Above the tuple cap, fall back to the plain element-array form —
		// mirrors aprot's maxTSTupleLen (#240).
		if length > 16 {
			if strings.Contains(elemType, " | ") {
				return "(" + elemType + ")[]"
			}
			return elemType + "[]"
		}
		// For nested types containing |, wrap in parens. Otherwise generate tuple directly.
		if strings.Contains(elemType, " | ") {
			elemType = "(" + elemType + ")"
		}
		// Generate a tuple: [T, T, ..., T] with length repetitions
		elements := make([]string, length)
		for i := 0; i < length; i++ {
			elements[i] = elemType
		}
		return "[" + strings.Join(elements, ", ") + "]"
	case reflect.Map:
		return fmt.Sprintf("Record<%s, %s>", goTypeToTS(t.Key()), goTypeToTS(t.Elem()))
	case reflect.Struct:
		if t.PkgPath() == "" {
			return "unknown"
		}
		return t.Name()
	case reflect.Interface:
		return "unknown"
	default:
		return "unknown"
	}
}
