package aprot

import (
	"embed"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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

// SQLNullGoKind returns the unwrapped Go kind string ("string", "int", "float",
// "bool") for database/sql nullable wrappers, parallel to SQLNullTSType. Returns
// "" if t is not a sql.Null type. kindResolver handles the generic Null[T] case
// and is typically goKindString.
//
// This is the Zod-side companion to SQLNullTSType: rather than sniffing the
// TypeScript output for a " | null" suffix, we derive the unwrapped kind
// directly from reflection so fieldData carries the information explicitly.
func SQLNullGoKind(t reflect.Type, kindResolver func(reflect.Type) string) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.PkgPath() != "database/sql" {
		return ""
	}
	switch t.Name() {
	case "NullString", "NullTime":
		return "string"
	case "NullInt64", "NullInt32", "NullInt16", "NullByte":
		return "int"
	case "NullFloat64":
		return "float"
	case "NullBool":
		return "bool"
	default:
		if strings.HasPrefix(t.Name(), "Null[") {
			if vField, ok := t.FieldByName("V"); ok {
				return kindResolver(vField.Type)
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

// generatedFileMarker is the first line of every file the generator emits.
// [Generator.Generate] uses it to recognize (and remove) files from earlier
// runs that no longer correspond to any handler group.
const generatedFileMarker = "// Code generated by aprot. DO NOT EDIT."

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
	"paramTypes": func(params []paramData) string {
		var parts []string
		for _, p := range params {
			parts = append(parts, p.Type)
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
	"hasStreamMethods": func(methods []methodData) bool {
		for _, m := range methods {
			if m.IsStream || m.IsStream2 {
				return true
			}
		}
		return false
	},
	"hasNonStreamMethods": func(methods []methodData) bool {
		for _, m := range methods {
			if !m.IsStream && !m.IsStream2 {
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

	// Zod enables generation of Zod validation schemas alongside TypeScript interfaces.
	// When enabled, {handler-name}.schema.ts files are generated for structs with validate tags.
	Zod bool
}

// Generator generates TypeScript client code from a registry.
type Generator struct {
	registry       *Registry
	options        GeneratorOptions
	types          map[reflect.Type]string
	collectedEnums map[reflect.Type]*EnumInfo // enums used by current handler
	marshalCache   map[reflect.Type]*MarshalTSType
	marshalChecked map[reflect.Type]bool
	// genErrors accumulates generation-time errors (e.g. fields whose Go type
	// has no working wire representation). Collected during interface building
	// and surfaced by Generate/GenerateTo so callers fail at generation time
	// rather than at runtime. Reset at the start of each generation.
	genErrors []error
}

// recordGenError appends a generation-time error.
func (g *Generator) recordGenError(err error) {
	g.genErrors = append(g.genErrors, err)
}

// genError joins all accumulated generation-time errors, or returns nil.
func (g *Generator) genError() error {
	if len(g.genErrors) == 0 {
		return nil
	}
	return errors.Join(g.genErrors...)
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
	Name        string
	Type        string
	Optional    bool
	GoType      string // Go kind for Zod/OpenAPI mapping (e.g., "string", "int", "float64")
	ValidateTag string // raw validate tag, e.g., "required,min=3,max=100"
	// SQLNullKind is non-empty when the underlying Go type is a database/sql
	// nullable wrapper (NullString, NullInt64, Null[T], etc.). Its value is the
	// unwrapped Go kind ("string", "int", "float", "bool"); Zod codegen uses it
	// to emit the correct base type + a trailing .nullable(). Empty otherwise.
	SQLNullKind string
	// ElemGoKind is non-empty when the field is a slice or map. Its value is the
	// Go kind of the element type ("string", "int", "struct", etc.) so Zod codegen
	// can emit z.array(z.string()), z.array(<TypeName>Schema), etc. instead of
	// falling through to z.array(z.any()). Empty otherwise.
	ElemGoKind string
	// ElemTypeName is the TS type name of the slice/map element when the element
	// is a named struct (e.g., "EventLinkInput"). Used by Zod codegen to look up
	// the element's generated schema. Empty for primitive elements.
	ElemTypeName string
	// ArrayLen is the fixed length of a [N]T array field, valid only when
	// GoType is "array". Zod codegen uses it to emit z.tuple([...]) with N
	// element schemas (or z.array(...).length(N) above the tuple cap) so the
	// inferred type matches the TS tuple the interface generator emits (#240).
	ArrayLen int
	// Enum is non-nil when the field's type is a registered enum. Zod codegen
	// uses it to emit z.enum([...]) (string enums) or z.union([z.literal(...),
	// ...]) (int enums), matching the branded enum type the TS interface
	// generator emits. Without this, enum fields would fall through to plain
	// z.string() / z.number().int() and the inferred Zod type would not be
	// assignable to the generated params type (aprot issue #176).
	Enum *EnumInfo
	// ElemEnum is non-nil when the field is a slice or map whose element type
	// is a registered enum. Zod codegen uses it to emit the enum expression
	// inside z.array(...) / z.record(...).
	ElemEnum *EnumInfo
	// Nullable is set when the field is a bare pointer without json omitempty:
	// such a field is always present on the wire and carries null when nil, so
	// its TypeScript type gets a `| null` and its Zod schema a .nullable().
	// (A pointer *with* omitempty is omitted when nil, so it is optional, not
	// nullable.) Set by collectInterfaceFields.
	Nullable bool
	// ElemLazy is set when the slice/map element references a generated schema
	// that is part of a reference cycle (self-referential or mutually
	// recursive). Zod codegen then wraps the element reference in
	// z.lazy(() => XSchema) so the cyclic const is not dereferenced before it
	// is initialized (which would be a temporal-dead-zone ReferenceError).
	// Set by buildZodSchemas after cycle analysis; the per-field reflection
	// pass leaves it false.
	ElemLazy bool
}

type paramData struct {
	Name     string // parameter name (from AST or "arg0")
	Type     string // TypeScript type string
	Variadic bool
}

type methodData struct {
	Name          string // short method name (e.g., "CreateUser")
	WireMethod    string // qualified wire name (e.g., "PublicHandlers.CreateUser")
	MethodName    string // camelCase function name (e.g., "createUser" or "numbers")
	HookName      string // React hook name (e.g., "useCreateUser" or "useNumbers")
	SubscribeName string // subscribe function name (e.g., "subscribeCreateUser") — unary only
	ResponseType  string // TS type of the response (unary) or per-item type (streams)
	ItemKeyType   string // TS type of the K in iter.Seq2 — only for IsStream2
	IsVoid        bool
	IsStream      bool // handler returns iter.Seq[T]
	IsStream2     bool // handler returns iter.Seq2[K, V]
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

// builtinErrorHelpers are the ApiError type-guard method names the client
// template emits unconditionally. A custom error whose generated helper name
// (see NamingPlugin.ErrorMethodName) equals one of these would produce a
// duplicate method — a TypeScript error — so such a registration is rejected at
// generation time (validateErrorCodes) and skipped in the emitted data
// (buildCustomErrorCodes).
var builtinErrorHelpers = map[string]bool{
	"isUnauthorized":       true,
	"isForbidden":          true,
	"isNotFound":           true,
	"isInvalidParams":      true,
	"isValidationFailed":   true,
	"isCanceled":           true,
	"isConnectionRejected": true,
	"isTooManyRequests":    true,
	"isAuthFailed":         true,
}

// validateErrorCodes records a generation error for each custom error whose
// generated helper name collides with a built-in ApiError helper. Called once
// per generation so the collision is reported a single time.
func (g *Generator) validateErrorCodes() {
	for _, ec := range g.registry.ErrorCodes() {
		method := g.naming().ErrorMethodName(ec.Name)
		if builtinErrorHelpers[method] {
			g.recordGenError(fmt.Errorf(
				"custom error %q generates helper %q, which collides with a built-in ApiError method; register the error under a different name (e.g. %q)",
				ec.Name, method, ec.Name+"Error"))
		}
	}
}

// buildCustomErrorCodes converts the registry's custom error codes to template
// data, skipping any whose generated helper name collides with a built-in
// ApiError method. The collision itself is reported once by validateErrorCodes;
// skipping here keeps the emitted client valid even if a caller ignores the
// generation error.
func (g *Generator) buildCustomErrorCodes() []errorCodeData {
	var out []errorCodeData
	for _, ec := range g.registry.ErrorCodes() {
		method := g.naming().ErrorMethodName(ec.Name)
		if builtinErrorHelpers[method] {
			continue
		}
		out = append(out, errorCodeData{Name: ec.Name, Code: ec.Code, MethodName: method})
	}
	return out
}

// Generate writes TypeScript client code for all handler groups.
// Returns a map of filename to content, or writes to OutputDir if set.
// Generates:
//   - client.ts: Base client with ApiClient, ApiError, ErrorCode, etc.
//   - {handler-name}.ts: Handler-specific interfaces and methods for each handler group
func (g *Generator) Generate() (map[string]string, error) {
	results := make(map[string]string)
	g.genErrors = nil
	g.validateErrorCodes()

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

	// Shared package files live in the same flat output directory as handler
	// files. If a package's short name resolves to the same file as a handler
	// (e.g. package "settings" + handler "Settings" both -> "settings.ts"), the
	// handler file — written later — would silently overwrite the shared file,
	// dropping its type/enum definitions and leaving every referencing file
	// importing a type nobody defines (issue #206). Give any colliding shared
	// file a distinct "{pkg}.types" base instead. A kebab-cased handler file
	// name can never contain a dot, so the alternate base is collision-free.
	handlerFileNames := make(map[string]bool)
	for _, group := range g.registry.Groups() {
		handlerFileNames[g.naming().FileName(group.Name)] = true
	}
	pkgBase := make(map[string]string, len(sortedPkgs))
	for _, pkg := range sortedPkgs {
		base := pkg
		if handlerFileNames[base] {
			base = pkg + ".types"
		}
		pkgBase[pkg] = base
	}

	// Pass 1: build pkgData for every shared package and accumulate
	// sharedTypeNames across all of them. Rendering is deferred so that Pass 2
	// can resolve cross-package references using the complete name map —
	// otherwise a shared file processed first would fail to import types
	// declared by a shared file processed later.
	pkgDataByPkg := make(map[string]*templateData, len(sortedPkgs))
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

		pkgData := &templateData{
			StructName: pkg,
			FileName:   pkgBase[pkg] + ".ts",
		}
		pkgData.Interfaces = g.buildInterfaces()
		pkgData.Enums = g.buildEnums()

		module := "./" + pkgBase[pkg]
		for _, iface := range pkgData.Interfaces {
			sharedTypeNames[iface.Name] = module
		}
		for _, enum := range pkgData.Enums {
			sharedTypeNames[enum.Name+"Type"] = module
		}

		pkgDataByPkg[pkg] = pkgData
	}

	// Pass 2: resolve SharedImports for each shared file against the complete
	// name map and render. Self-references (types defined in the same file)
	// are excluded so a file never imports from itself.
	for _, pkg := range sortedPkgs {
		pkgData := pkgDataByPkg[pkg]
		selfModule := "./" + pkgBase[pkg]
		selfNames := make(map[string]bool, len(pkgData.Interfaces)+len(pkgData.Enums))
		for _, iface := range pkgData.Interfaces {
			selfNames[iface.Name] = true
		}
		for _, enum := range pkgData.Enums {
			selfNames[enum.Name+"Type"] = true
		}
		scoped := make(map[string]string, len(sharedTypeNames))
		for name, module := range sharedTypeNames {
			if module == selfModule && selfNames[name] {
				continue
			}
			scoped[name] = module
		}
		pkgData.SharedImports = findSharedTypeImports(pkgData, scoped)

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
	baseData.CustomErrorCodes = g.buildCustomErrorCodes()

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

	// Extract source metadata from AST
	meta := g.extractAllSourceMeta()

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

		data := g.buildTemplateData(group, meta)
		data.BaseTypeImports = findBaseTypeImports(&data, baseTypeNames)
		data.SharedImports = findSharedTypeImports(&data, sharedTypeNames)

		var buf strings.Builder
		if err := templates.ExecuteTemplate(&buf, handlerTemplateName, data); err != nil {
			return nil, err
		}

		results[data.FileName] = buf.String()

		// Generate Zod schema file if enabled and handler has validated types
		if g.options.Zod {
			schemas := buildZodSchemas(data.Interfaces)
			if len(schemas) > 0 {
				schemaData := struct {
					Schemas []zodSchemaData
				}{Schemas: schemas}
				var schemaBuf strings.Builder
				if err := templates.ExecuteTemplate(&schemaBuf, "client-schema.ts.tmpl", schemaData); err != nil {
					return nil, err
				}
				schemaFileName := strings.TrimSuffix(data.FileName, ".ts") + ".schema.ts"
				results[schemaFileName] = schemaBuf.String()
			}
		}
	}

	// Render base client
	var baseBuf strings.Builder
	if err := templates.ExecuteTemplate(&baseBuf, baseTemplateName, baseData); err != nil {
		return nil, err
	}
	results["client.ts"] = baseBuf.String()

	// Surface any generation-time errors before writing anything.
	if err := g.genError(); err != nil {
		return nil, err
	}

	// Run generate hooks
	for _, hook := range g.registry.generateHooks {
		hook(results, g.options.Mode)
	}

	// Write to files if OutputDir is set
	if g.options.OutputDir != "" {
		// Generated TypeScript client source is non-sensitive and meant to be
		// read by the developer's toolchain (and usually committed), so the
		// conventional world-readable source perms are intentional here.
		if err := os.MkdirAll(g.options.OutputDir, 0o755); err != nil { // #nosec G301 -- generated client source dir, not sensitive
			return nil, err
		}
		for filename, content := range results {
			path := filepath.Join(g.options.OutputDir, filename)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil { // #nosec G306 -- generated client source file, not sensitive
				return nil, err
			}
		}
		if err := g.removeStaleFiles(results); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// removeStaleFiles deletes top-level .ts files in OutputDir that carry the
// generated-code marker but were not produced by the current run — leftovers
// from an earlier generation of a handler group that has since been renamed
// or removed. Left in place, such files reference types that no longer exist
// and break the TypeScript build. Hand-written files (no marker on the first
// line), non-.ts files, and subdirectories are never touched.
func (g *Generator) removeStaleFiles(written map[string]string) error {
	entries, err := os.ReadDir(g.options.OutputDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".ts") {
			continue
		}
		if _, ok := written[name]; ok {
			continue
		}
		path := filepath.Join(g.options.OutputDir, name)
		content, err := os.ReadFile(path) // #nosec G304 -- path is confined to OutputDir
		if err != nil || !isGeneratedByAprot(content) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// isGeneratedByAprot reports whether content starts with generatedFileMarker
// as its complete first line.
func isGeneratedByAprot(content []byte) bool {
	rest, ok := strings.CutPrefix(string(content), generatedFileMarker)
	if !ok {
		return false
	}
	return rest == "" || rest[0] == '\n' || rest[0] == '\r'
}

// GenerateTo writes TypeScript client code to a single writer.
// This combines all handler groups into one file (legacy behavior).
func (g *Generator) GenerateTo(w io.Writer) error {
	g.genErrors = nil
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
			if info.Kind == HandlerKindStream2 && info.StreamKeyType != nil {
				g.collectNestedType(info.StreamKeyType)
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

	// Extract source metadata from AST
	meta := g.extractAllSourceMeta()

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
		data.Methods = append(data.Methods, g.buildMethodData(info, qualifiedName, info.Name, meta))
	}

	// Build push events from all groups
	for _, event := range g.registry.PushEvents() {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: g.naming().HandlerName(event.Name),
			HookName:    g.naming().HookName(event.Name),
			DataType:    sanitizeTSIdent(event.DataType.Name()),
		})
	}

	// Build custom error codes
	data.CustomErrorCodes = g.buildCustomErrorCodes()

	// Build enums
	data.Enums = g.buildEnums()

	templateName := "client.ts.tmpl"
	if g.options.Mode == OutputReact {
		templateName = "client-react.ts.tmpl"
	}

	// Surface any generation-time errors before emitting output.
	if err := g.genError(); err != nil {
		return err
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

// buildMethodData constructs methodData for a single handler, handling unary
// and streaming (iter.Seq / iter.Seq2) return shapes. For streaming handlers
// ResponseType holds the per-item TS type (the template adds AsyncIterable).
func (g *Generator) buildMethodData(info *HandlerInfo, wireMethod, shortName string, meta *sourceMeta) methodData {
	isStream := info.Kind == HandlerKindStream
	isStream2 := info.Kind == HandlerKindStream2
	isVoid := !isStream && !isStream2 && info.ResponseType == voidResponseType

	var respType string
	switch {
	case isVoid:
		respType = "void"
	case isStream, isStream2:
		respType = g.goTypeToTS(info.ResponseType)
	case isBlobResponse(info.ResponseType):
		// Top-level Blob results are delivered as binary frames (or the $blob
		// JSON fallback) and always reach the caller as a DOM Blob.
		respType = "Blob"
	default:
		respType = g.goTypeToTS(info.ResponseType)
	}

	var keyType string
	if isStream2 && info.StreamKeyType != nil {
		keyType = g.goTypeToTS(info.StreamKeyType)
	}

	return methodData{
		Name:          shortName,
		WireMethod:    wireMethod,
		MethodName:    g.naming().MethodName(shortName),
		HookName:      g.naming().HookName(shortName),
		SubscribeName: "subscribe" + shortName,
		ResponseType:  respType,
		ItemKeyType:   keyType,
		IsVoid:        isVoid,
		IsStream:      isStream,
		IsStream2:     isStream2,
		Params:        g.buildParamData(info, meta),
	}
}

func (g *Generator) buildTemplateData(group *HandlerGroup, meta *sourceMeta) templateData {
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
		iface := interfaceData{Name: sanitizeTSIdent(t.Name())}
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
		data.Methods = append(data.Methods, g.buildMethodData(info, group.Name+"."+name, name, meta))
	}

	// Build push events
	for _, event := range group.PushEvents {
		data.PushEvents = append(data.PushEvents, pushEventData{
			Name:        event.Name,
			HandlerName: g.naming().HandlerName(event.Name),
			HookName:    g.naming().HookName(event.Name),
			DataType:    sanitizeTSIdent(event.DataType.Name()),
		})
	}

	// Build custom error codes
	data.CustomErrorCodes = g.buildCustomErrorCodes()

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
				Name:  tsObjectKey(v.Name),
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
		iface := interfaceData{Name: sanitizeTSIdent(t.Name())}
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
				Name:  tsObjectKey(v.Name),
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

// isValidJSIdent reports whether name is a valid (ASCII) JavaScript
// identifier, safe to use as an unquoted object-literal key. Deliberately
// ASCII-only: any name that falls outside this set is emitted as a quoted
// string key instead, which is always syntactically valid.
func isValidJSIdent(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$'
		if i > 0 {
			ok = ok || (c >= '0' && c <= '9')
		}
		if !ok {
			return false
		}
	}
	return true
}

// tsObjectKey renders s as a TypeScript object-literal key. Valid JS
// identifiers are returned bare; anything else — including the empty
// string, hyphens, leading digits, spaces — is returned as a double-quoted
// string literal.
func tsObjectKey(s string) string {
	if isValidJSIdent(s) {
		return s
	}
	return strconv.Quote(s)
}

// tsReservedWords are JavaScript/TypeScript keywords and reserved words that
// cannot be used as a bare parameter identifier in generated code. A Go param
// whose name collides with one of these is renamed (see tsSafeParamName).
var tsReservedWords = map[string]bool{
	"break": true, "case": true, "catch": true, "class": true, "const": true,
	"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
	"else": true, "enum": true, "export": true, "extends": true, "false": true,
	"finally": true, "for": true, "function": true, "if": true, "import": true,
	"in": true, "instanceof": true, "new": true, "null": true, "return": true,
	"super": true, "switch": true, "this": true, "throw": true, "true": true,
	"try": true, "typeof": true, "var": true, "void": true, "while": true,
	"with": true, "implements": true, "interface": true, "let": true,
	"package": true, "private": true, "protected": true, "public": true,
	"static": true, "yield": true, "await": true,
}

// tsSafeParamName returns a parameter name safe to emit in generated TypeScript.
// Names that collide with a reserved word, or that aren't valid JS identifiers,
// are suffixed with "_" (parameter positions are matched by order in the
// generated client, so the rename is purely cosmetic and stays consistent
// across the signature, body, and call site).
func tsSafeParamName(name string) string {
	if tsReservedWords[name] || !isValidJSIdent(name) {
		return name + "_"
	}
	return name
}

// sanitizeTSIdent converts a Go type name into a valid TypeScript identifier.
// Instantiated generics reflect as names like "Box[int]" or "Pair[string,int]"
// which are not valid TS identifiers; the bracketed type arguments are folded
// into the name as TitleCased words ("BoxInt", "PairStringInt"). Names that are
// already valid identifiers are returned unchanged. Applied consistently at
// every emission site (interface declarations, type references, OpenAPI schema
// names and $refs) so a generic type and its references stay in sync.
func sanitizeTSIdent(name string) string {
	if isValidJSIdent(name) {
		return name
	}
	var b strings.Builder
	newWord := false
	for i, r := range name {
		switch {
		case r == '_' || r == '$' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)):
			if newWord {
				r = unicode.ToUpper(r)
				newWord = false
			}
			b.WriteRune(r)
		default:
			// Any other rune (brackets, commas, dots, slashes, spaces, '*')
			// is a word separator that is dropped; the next letter starts a
			// new TitleCased word.
			newWord = true
		}
	}
	return b.String()
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
	// Names declared by this file itself. A local declaration shadows a shared
	// type of the same short name (e.g. a handler-local Point next to a shared
	// focus_bank.Point, or package analysis' Exports next to package filter's
	// Exports): importing the shared one alongside the local declaration is a
	// TS2440 conflict, so references in this file bind to the local type and
	// the import is skipped.
	localNames := make(map[string]bool, len(data.Interfaces)+len(data.Enums))
	for _, iface := range data.Interfaces {
		localNames[iface.Name] = true
	}
	for _, enum := range data.Enums {
		localNames[enum.Name+"Type"] = true
	}

	// Collect referenced type names grouped by module
	byModule := make(map[string]map[string]bool)
	scanRef := func(text string) {
		for name, module := range sharedTypeNames {
			if localNames[name] {
				continue
			}
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
		// time.Duration has no default JSON representation in jsonv2 and fails
		// at runtime (both marshal and unmarshal) unless an explicit json
		// `format:` option is given. Fail at generation time instead so the
		// developer learns of it up front rather than on the wire.
		if dt := field.Type; dt != nil {
			ft := dt
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.PkgPath() == "time" && ft.Name() == "Duration" && jsonFormatOption(field) == "" {
				g.recordGenError(fmt.Errorf("%s.%s: time.Duration has no default JSON representation and fails at runtime; add a json format option (e.g. `json:\"%s,format:nano\"`) or use a different type", t.Name(), field.Name, g.getJSONName(field)))
			}
		}

		elemGoKind, elemTypeName := elemTypeInfo(field.Type)
		goType := goKindString(field.Type)
		tsType := g.goTypeToTS(field.Type)
		optional := g.isOptional(field)
		// Fixed-size array length ([N]T, possibly behind a pointer) — drives
		// tuple emission in Zod codegen (#240).
		arrayLen := 0
		if at := field.Type; at.Kind() == reflect.Ptr && at.Elem().Kind() == reflect.Array {
			arrayLen = at.Elem().Len()
		} else if at.Kind() == reflect.Array {
			arrayLen = at.Len()
		}
		// A bare pointer (no json omitempty) is always serialized — as null when
		// nil — so it is required and nullable, not absent. Reflect that in both
		// the TS type (`| null`) and the optionality flag.
		nullable := false
		if field.Type.Kind() == reflect.Ptr && !strings.Contains(field.Tag.Get("json"), "omitempty") {
			nullable = true
			optional = false
			if !strings.HasSuffix(tsType, " | null") {
				tsType += " | null"
			}
		}
		if isByteSlice(field.Type) || isByteArray(field.Type) {
			// go-json-experiment/json v2 per-field `format:` tag (issue
			// #174). A tag value wins over the issue #174 default — it can
			// force an unnamed []byte to serialize as a number array, or
			// force a named byte slice to serialize as a base-N string.
			// Unrecognized tag values fall through so the default shape
			// still applies.
			if format := jsonFormatOption(field); format != "" {
				if ts, gk, ek, ok := byteSliceFormatShape(format); ok {
					tsType = ts
					goType = gk
					elemGoKind = ek
					elemTypeName = ""
					if format == "array" && isByteArray(field.Type) {
						// A [N]byte forced onto the number-array wire shape
						// keeps its fixed length: tuple below the cap, plain
						// number[] above it (#240).
						tsType = tsTupleOrArray("number", arrayLen)
						goType = "array"
					}
				}
			} else if isUnnamedByteSlice(field.Type) || isByteArray(field.Type) {
				// Unnamed []byte — and every [N]byte, named or not — is
				// encoded as a base64 string on the wire. Report it as a
				// string kind so Zod codegen emits z.string() instead of
				// z.array(z.number()), and clear the element info that would
				// drive z.array(...).
				goType = "string"
				elemGoKind = ""
				elemTypeName = ""
				arrayLen = 0
			}
		}
		fields = append(fields, fieldData{
			// tsObjectKey quotes names that aren't valid JS identifiers (e.g.
			// json:"my-field") so both the TS interface and the Zod schema emit
			// a syntactically valid key.
			Name:         tsObjectKey(g.getJSONName(field)),
			Type:         tsType,
			Optional:     optional,
			Nullable:     nullable,
			GoType:       goType,
			ValidateTag:  field.Tag.Get("validate"),
			SQLNullKind:  SQLNullGoKind(field.Type, goKindString),
			ElemGoKind:   elemGoKind,
			ElemTypeName: elemTypeName,
			ArrayLen:     arrayLen,
			Enum:         g.lookupEnum(field.Type),
			ElemEnum:     g.lookupElemEnum(field.Type),
		})
	}
	return fields
}

func (g *Generator) collectType(t reflect.Type) {
	if t == nil || t == voidResponseType {
		return
	}
	// Blob is never emitted as an interface: top-level results are typed as
	// the DOM Blob (which an interface of the same name would shadow), and
	// nested occurrences use the inline wire shape from goTypeToTS.
	if t == blobType {
		return
	}
	if _, ok := g.types[t]; ok {
		return
	}
	g.types[t] = t.Name()

	// Recursively collect field types, flattening embedded structs.
	// Mirror collectInterfaceFields: unexported and json:"-" fields never
	// appear in the emitted interfaces, so their types must not be collected
	// either — an unexported `mu sync.Mutex` field would otherwise emit empty
	// Mutex/noCopy interfaces into a shared sync.ts.
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
			if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && !g.hasMarshalOverride(ft) {
				// Don't register as separate interface — fields are flattened.
				// But recurse into its fields to collect nested types.
				for j := 0; j < ft.NumField(); j++ {
					ef := ft.Field(j)
					if !ef.IsExported() || shouldSkipField(ef) {
						continue
					}
					g.collectNestedType(ef.Type)
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
		// Streaming handlers using iter.Seq2[K, V] expose K as the tuple's
		// first element in TS; collect it so shared types and enums referenced
		// via K are emitted alongside the usual response types.
		if info.Kind == HandlerKindStream2 && info.StreamKeyType != nil {
			g.collectNestedType(info.StreamKeyType)
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
	case reflect.Slice, reflect.Array:
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

// lookupEnum returns the registered EnumInfo for t (unwrapping a pointer),
// or nil if t is not a registered enum. Used by collectInterfaceFields to
// populate fieldData.Enum so Zod codegen can emit z.enum(...) instead of
// falling through to z.string() / z.number().int() on the underlying kind
// (aprot issue #176).
func (g *Generator) lookupEnum(t reflect.Type) *EnumInfo {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return g.registry.GetEnum(t)
}

// lookupElemEnum returns the registered EnumInfo for the element type of a
// slice or map (unwrapping pointers), or nil otherwise. Used by
// collectInterfaceFields to populate fieldData.ElemEnum.
func (g *Generator) lookupElemEnum(t reflect.Type) *EnumInfo {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice && t.Kind() != reflect.Map && t.Kind() != reflect.Array {
		return nil
	}
	elem := t.Elem()
	if elem.Kind() == reflect.Ptr {
		elem = elem.Elem()
	}
	return g.registry.GetEnum(elem)
}

// elemTypeInfo inspects a slice, map, or fixed-size array type and returns the
// element's Go kind (e.g., "string", "int", "struct") and TS type name (for
// named structs only). Returns ("", "") for other types or for elements that
// aren't usefully describable for Zod codegen (e.g., interfaces, anonymous
// structs, slices of slices). The TS type name is only set when the element is
// a named struct type — primitive elements have an empty type name and rely on
// the kind alone.
//
// Used by Zod codegen to substitute element schemas into z.array(...) /
// z.record(...) / z.tuple([...]) instead of falling through to z.any().
func elemTypeInfo(t reflect.Type) (goKind string, typeName string) {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice && t.Kind() != reflect.Map && t.Kind() != reflect.Array {
		return "", ""
	}
	elem := t.Elem()
	if elem.Kind() == reflect.Ptr {
		elem = elem.Elem()
	}
	kind := goKindString(elem)
	if kind == "struct" {
		// Only named structs from a real package can be referenced as
		// <Name>Schema. Anonymous structs (PkgPath == "") and types with no
		// name fall through to z.any() at the call site.
		if elem.Name() != "" && elem.PkgPath() != "" {
			// Sanitize so a generic element type's schema/interface reference
			// matches its (sanitized) declaration name.
			return kind, sanitizeTSIdent(elem.Name())
		}
		return "", ""
	}
	// Primitive elements (string, int, etc.) — return the kind, no name.
	// Slices of slices, slices of maps, and nested fixed-size arrays are
	// punted to z.any(): the recursive case requires a structured nested
	// fieldData and is out of scope for the initial fix.
	if kind == "slice" || kind == "map" || kind == "array" {
		return "", ""
	}
	return kind, ""
}

// isUnnamedByteSlice reports whether t is the unnamed type []byte. Under
// both encoding/json v1 and go-json-experiment/json v2, unnamed byte slices
// marshal as base64 strings, not number arrays — so TS/Zod/OpenAPI codegen
// must emit a string shape. Named byte slices (`type Foo []byte`) are
// treated as number arrays by v2 per Go issue #24746, so those intentionally
// fall through to the default slice path.
func isUnnamedByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 && t.Name() == ""
}

// isByteSlice reports whether t is any byte slice — named or unnamed. Used
// to gate the go-json-experiment/json v2 `format:` tag override, which
// applies to every []byte / `type Foo []byte` regardless of naming.
func isByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}

// isByteArray reports whether t is a fixed-size byte array ([N]byte, named or
// not). go-json-experiment/json v2 encodes byte arrays as base64 strings by
// default — unlike named byte slices, which encode as number arrays — so
// TS/Zod/OpenAPI codegen must emit a string shape (#240).
func isByteArray(t reflect.Type) bool {
	return t.Kind() == reflect.Array && t.Elem().Kind() == reflect.Uint8
}

// maxTSTupleLen is the largest fixed-size Go array rendered as a TS tuple
// ([T, T, ...]). Longer arrays fall back to the plain T[] form — the length
// is still statically known, but a 100-element tuple type hurts more than it
// helps (#240).
const maxTSTupleLen = 16

// tsTupleOrArray renders the TS type for a [N]T array whose element renders
// as elemTS: a tuple [T, T, ...] when N is at most maxTSTupleLen, otherwise
// T[].
func tsTupleOrArray(elemTS string, n int) string {
	if n > maxTSTupleLen {
		if strings.Contains(elemTS, " | ") {
			return "(" + elemTS + ")[]"
		}
		return elemTS + "[]"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = elemTS
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// jsonFormatOption parses the `format:X` option from a go-json-experiment
// json struct tag and returns X, or "" if the tag has no format option.
// The tag grammar is `name,opt1,opt2,...` where each option is a bare word
// or `key:value`. Only the first format: option is honored.
func jsonFormatOption(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return ""
	}
	parts := strings.Split(tag, ",")
	for _, p := range parts[1:] { // skip the name
		if strings.HasPrefix(p, "format:") {
			return strings.TrimPrefix(p, "format:")
		}
	}
	return ""
}

// byteSliceFormatShape returns the TS / Go-kind / element-kind tuple that a
// byte slice should use under a given v2 json format tag value. Returns
// ok=false for an empty or unrecognized format (caller falls through to the
// default behavior). Supported formats per the v2 package docs:
//
//   - "array": JSON array of per-byte numbers → TS number[]
//   - "base64" (default for unnamed []byte), "base64url", "base32",
//     "base32hex", "base16", "hex": base-N string → TS string
func byteSliceFormatShape(format string) (tsType string, goKind string, elemGoKind string, ok bool) {
	switch format {
	case "array":
		return "number[]", "slice", "uint", true
	case "base64", "base64url", "base32", "base32hex", "base16", "hex":
		return "string", "string", "", true
	default:
		return "", "", "", false
	}
}

// goKindString returns a simplified Go kind string for type mapping in Zod/OpenAPI.
// Resolves through pointers and named types to the underlying kind.
func goKindString(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "uint"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Bool:
		return "bool"
	case reflect.Slice:
		return "slice"
	case reflect.Array:
		return "array"
	case reflect.Map:
		return "map"
	case reflect.Struct:
		return "struct"
	default:
		return t.Kind().String()
	}
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

	// json.RawMessage is a named []byte but carries arbitrary embedded JSON, so
	// number[] (the named-byte-slice default) is wrong. Type it as `unknown`.
	if t.PkgPath() == "encoding/json" && t.Name() == "RawMessage" {
		return "unknown"
	}

	// Blob outside a top-level result position travels as plain JSON.
	// buildMethodData overrides top-level unary results to the DOM Blob.
	if t == blobType {
		return blobTSWireShape
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
		if isUnnamedByteSlice(t) {
			return "string"
		}
		elemType := g.goTypeToTS(t.Elem())
		if strings.Contains(elemType, " | ") {
			return "(" + elemType + ")[]"
		}
		return elemType + "[]"
	case reflect.Array:
		if isByteArray(t) {
			return "string"
		}
		return tsTupleOrArray(g.goTypeToTS(t.Elem()), t.Len())
	case reflect.Map:
		valType := g.goTypeToTS(t.Elem())
		// boolean is not a valid TypeScript index signature, so Record<boolean,
		// T> doesn't compile. jsonv2 encodes bool map keys as the strings
		// "true"/"false", so model them as an optional string-literal record.
		if t.Key().Kind() == reflect.Bool {
			return `Partial<Record<"true" | "false", ` + valType + ">>"
		}
		keyType := g.goTypeToTS(t.Key())
		return "Record<" + keyType + ", " + valType + ">"
	case reflect.Ptr:
		return g.goTypeToTS(t.Elem())
	case reflect.Struct:
		if t.PkgPath() == "" {
			return "any"
		}
		return sanitizeTSIdent(t.Name())
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
func (g *Generator) buildParamData(info *HandlerInfo, meta *sourceMeta) []paramData {
	if len(info.Params) == 0 {
		return nil
	}

	astNames := meta.paramNames(info.StructName, info.Name)

	params := make([]paramData, len(info.Params))
	for i, p := range info.Params {
		name := fmt.Sprintf("arg%d", i)
		if i < len(astNames) {
			name = astNames[i]
		}
		name = tsSafeParamName(name)
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

// sourceMeta holds godoc and parameter-name metadata collected from handler source files.
type sourceMeta struct {
	// Handlers is keyed by receiver struct name, then method name.
	Handlers map[string]map[string]handlerMeta
	// Types is keyed by struct type name.
	Types map[string]typeMeta
}

// handlerMeta is the per-method metadata extracted from the AST.
type handlerMeta struct {
	ParamNames []string // parameter names, excluding the first context.Context param
	Doc        string   // raw godoc text above the method's FuncDecl
}

// typeMeta is the per-struct metadata extracted from the AST.
type typeMeta struct {
	Doc       string            // godoc text above the TypeSpec (or its GenDecl)
	FieldDocs map[string]string // Go field name → godoc (Doc preferred, Comment as fallback)
}

// ParamNames returns the parameter names extracted for a handler method.
func (m *sourceMeta) paramNames(structName, methodName string) []string {
	if m == nil {
		return nil
	}
	if methods, ok := m.Handlers[structName]; ok {
		return methods[methodName].ParamNames
	}
	return nil
}

// HandlerDoc returns the godoc comment extracted above a handler method.
func (m *sourceMeta) handlerDoc(structName, methodName string) string {
	if m == nil {
		return ""
	}
	if methods, ok := m.Handlers[structName]; ok {
		return methods[methodName].Doc
	}
	return ""
}

// TypeDoc returns the godoc comment extracted above a struct type declaration.
func (m *sourceMeta) typeDoc(typeName string) string {
	if m == nil {
		return ""
	}
	return m.Types[typeName].Doc
}

// FieldDoc returns the godoc comment extracted for a struct field (by Go field name).
func (m *sourceMeta) fieldDoc(typeName, fieldName string) string {
	if m == nil {
		return ""
	}
	if t, ok := m.Types[typeName]; ok {
		return t.FieldDocs[fieldName]
	}
	return ""
}

// extractAllSourceMeta extracts source metadata for all registered handler groups.
func (g *Generator) extractAllSourceMeta() *sourceMeta {
	dirs := make(map[string]bool)
	for _, group := range g.registry.Groups() {
		if dir := group.SourceDir(); dir != "" {
			dirs[dir] = true
		}
	}
	return extractSourceMeta(dirs)
}

// extractSourceMeta parses Go source files and collects parameter names and godoc comments
// for handler methods and struct types declared within the given directories.
func extractSourceMeta(dirs map[string]bool) *sourceMeta {
	result := &sourceMeta{
		Handlers: make(map[string]map[string]handlerMeta),
		Types:    make(map[string]typeMeta),
	}

	for dir := range dirs {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				for _, decl := range file.Decls {
					switch d := decl.(type) {
					case *ast.FuncDecl:
						collectHandlerMeta(d, result)
					case *ast.GenDecl:
						if d.Tok == token.TYPE {
							collectTypeMeta(d, result)
						}
					}
				}
			}
		}
	}

	return result
}

func collectHandlerMeta(fn *ast.FuncDecl, result *sourceMeta) {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return
	}

	recvType := fn.Recv.List[0].Type
	if star, ok := recvType.(*ast.StarExpr); ok {
		recvType = star.X
	}
	ident, ok := recvType.(*ast.Ident)
	if !ok {
		return
	}
	structName := ident.Name
	methodName := fn.Name.Name

	// Extract parameter names, skipping the first (context.Context) param.
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

	if result.Handlers[structName] == nil {
		result.Handlers[structName] = make(map[string]handlerMeta)
	}
	result.Handlers[structName][methodName] = handlerMeta{
		ParamNames: names,
		Doc:        strings.TrimSpace(fn.Doc.Text()),
	}
}

func collectTypeMeta(gd *ast.GenDecl, result *sourceMeta) {
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			continue
		}

		// Go attaches the comment to TypeSpec.Doc for grouped `type (…)` blocks,
		// and to GenDecl.Doc for single-spec `type Foo struct { … }` declarations.
		typeDoc := ts.Doc.Text()
		if typeDoc == "" {
			typeDoc = gd.Doc.Text()
		}

		fieldDocs := make(map[string]string)
		if st.Fields != nil {
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					continue
				}
				doc := strings.TrimSpace(f.Doc.Text())
				if doc == "" {
					doc = strings.TrimSpace(f.Comment.Text())
				}
				if doc == "" {
					continue
				}
				for _, name := range f.Names {
					fieldDocs[name.Name] = doc
				}
			}
		}

		result.Types[ts.Name.Name] = typeMeta{
			Doc:       strings.TrimSpace(typeDoc),
			FieldDocs: fieldDocs,
		}
	}
}
