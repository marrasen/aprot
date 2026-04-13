package aprot

import (
	"fmt"
	"strings"
)

// zodSchemaData holds data for generating a Zod schema file.
type zodSchemaData struct {
	Name   string         // e.g., "SetAgeRequest"
	Fields []zodFieldData // schema fields
	Source string         // source handler file for imports
}

// zodFieldData holds Zod type info for a single field.
type zodFieldData struct {
	Name    string // JSON field name
	ZodType string // e.g., "z.string().min(3).max(100)"
}

// fieldToZod converts a fieldData (with all the type/kind/tag info collected
// during reflection) to a Zod schema string. knownSchemas is the set of
// interface names that will produce a generated schema, used to substitute
// element schemas into z.array(...) / z.record(...) for slice and map fields
// whose element type is itself a generated schema.
func fieldToZod(f fieldData, knownSchemas map[string]bool) string {
	rules := ParseValidateTag(f.ValidateTag)

	// Issue 3 (#163): sql.Null* wrappers. Use the unwrapped kind for base +
	// constraints, then append .nullable() after constraints but before
	// .optional().
	effectiveKind := f.GoType
	isNullable := false
	if f.SQLNullKind != "" {
		effectiveKind = f.SQLNullKind
		isNullable = true
	}

	// Issue 1 (#163): validate-tag omitempty forces .optional(). For string
	// kind, also wrap the chain in z.union([z.literal(""), ...]) so empty
	// strings pass — go-playground/validator skips subsequent rules on the
	// zero value ("" for strings), and client forms almost always emit ""
	// rather than undefined.
	hasValidateOmitempty := false
	for _, r := range rules {
		if r.Tag == "omitempty" {
			hasValidateOmitempty = true
			break
		}
	}
	optional := f.Optional
	if hasValidateOmitempty {
		optional = true
	}

	// Issue #176: registered enum fields emit z.enum([...]) (string) or
	// z.union([z.literal(...), ...]) (int) so z.infer matches the branded
	// enum type the TS interface generator emits. Validate constraints like
	// min/max/email don't apply to enum literals, so we skip them and only
	// honor nullability, optionality, and the omitempty empty-string wrap.
	if f.Enum != nil {
		body := "z." + zodEnumBase(f.Enum)
		if isNullable {
			body += ".nullable()"
		}
		if hasValidateOmitempty && f.Enum.IsString {
			body = `z.union([z.literal(""), ` + body + `])`
		}
		if optional {
			body += ".optional()"
		}
		return body
	}

	// Issue 2 (#163): if an explicit min rule is present on a string, skip
	// the implicit required -> min(1) so we don't emit .min(1).min(N).
	hasExplicitMin := false
	for _, r := range rules {
		if r.Tag == "min" {
			hasExplicitMin = true
			break
		}
	}

	var chain []string
	chain = append(chain, zodBaseType(effectiveKind, f.Type, f.ElemGoKind, f.ElemTypeName, f.ElemEnum, knownSchemas))

	for _, r := range rules {
		if r.Tag == "omitempty" {
			continue
		}
		if r.Tag == "required" && effectiveKind == "string" && hasExplicitMin {
			continue
		}
		if zod := zodConstraint(effectiveKind, r); zod != "" {
			chain = append(chain, zod)
		}
	}

	if isNullable {
		chain = append(chain, "nullable()")
	}

	body := "z." + strings.Join(chain, ".")

	if hasValidateOmitempty && effectiveKind == "string" {
		body = `z.union([z.literal(""), ` + body + `])`
	}

	if optional {
		body += ".optional()"
	}

	return body
}

// zodBaseType returns the base Zod type for a Go kind. For slice and map
// kinds, elemGoKind/elemTypeName provide the element's type info so the array
// or record can substitute the element schema (#169) instead of falling
// through to z.any(). elemEnum is non-nil when the slice/map element is a
// registered enum; in that case zodElemExpr emits z.enum(...) / z.union(...)
// for the element (#176).
func zodBaseType(goKind string, tsType string, elemGoKind string, elemTypeName string, elemEnum *EnumInfo, knownSchemas map[string]bool) string {
	switch goKind {
	case "string":
		return "string()"
	case "int", "uint":
		return "number().int()"
	case "float":
		return "number()"
	case "bool":
		return "boolean()"
	case "slice":
		return "array(" + zodElemExpr(elemGoKind, elemTypeName, elemEnum, knownSchemas) + ")"
	case "map":
		return "record(z.string(), " + zodElemExpr(elemGoKind, elemTypeName, elemEnum, knownSchemas) + ")"
	default:
		// Struct or unknown — use any()
		return "any()"
	}
}

// zodElemExpr builds a Zod expression for a slice or map element. Returns
// "z.any()" when the element type is unknown or unsupported (recursive types,
// anonymous structs, slices of slices) so callers can wrap it directly.
// When elemEnum is non-nil, the element is a registered enum and is emitted
// as z.enum([...]) / z.union([...]) (#176).
func zodElemExpr(elemGoKind string, elemTypeName string, elemEnum *EnumInfo, knownSchemas map[string]bool) string {
	if elemEnum != nil {
		return "z." + zodEnumBase(elemEnum)
	}
	switch elemGoKind {
	case "string":
		return "z.string()"
	case "int", "uint":
		return "z.number().int()"
	case "float":
		return "z.number()"
	case "bool":
		return "z.boolean()"
	case "struct":
		if elemTypeName != "" && knownSchemas[elemTypeName] {
			return elemTypeName + "Schema"
		}
		return "z.any()"
	default:
		return "z.any()"
	}
}

// zodEnumBase returns the Zod expression body (without the leading "z.") for
// a registered enum. String enums become enum(["a", "b", ...]) so z.infer
// produces the matching string literal union; int enums become
// union([z.literal(0), z.literal(1), ...]) so z.infer produces the matching
// number literal union. (#176)
func zodEnumBase(info *EnumInfo) string {
	if info.IsString {
		parts := make([]string, 0, len(info.Values))
		for _, v := range info.Values {
			s, _ := v.Value.(string)
			parts = append(parts, `"`+s+`"`)
		}
		return "enum([" + strings.Join(parts, ", ") + "])"
	}
	parts := make([]string, 0, len(info.Values))
	for _, v := range info.Values {
		parts = append(parts, fmt.Sprintf("z.literal(%d)", v.Value))
	}
	return "union([" + strings.Join(parts, ", ") + "])"
}

// zodConstraint maps a single validate rule to a Zod chain method.
func zodConstraint(goKind string, rule ValidateRule) string {
	switch rule.Tag {
	case "required":
		// In Zod, fields are required by default; skip unless we want .nonempty()
		if goKind == "string" {
			return "min(1)"
		}
		return ""
	case "min":
		if goKind == "string" {
			return fmt.Sprintf("min(%s)", rule.Param)
		}
		return fmt.Sprintf("min(%s)", rule.Param)
	case "max":
		if goKind == "string" {
			return fmt.Sprintf("max(%s)", rule.Param)
		}
		return fmt.Sprintf("max(%s)", rule.Param)
	case "len":
		return fmt.Sprintf("length(%s)", rule.Param)
	case "gte":
		return fmt.Sprintf("min(%s)", rule.Param)
	case "gt":
		return fmt.Sprintf("gt(%s)", rule.Param)
	case "lte":
		return fmt.Sprintf("max(%s)", rule.Param)
	case "lt":
		return fmt.Sprintf("lt(%s)", rule.Param)
	case "email":
		return "email()"
	case "url":
		return "url()"
	case "uuid":
		return "uuid()"
	case "alpha":
		return `regex(/^[a-zA-Z]+$/)`
	case "alphanum":
		return `regex(/^[a-zA-Z0-9]+$/)`
	case "oneof":
		// "red green blue" -> z.enum(["red", "green", "blue"])
		// This replaces the base type entirely, handled separately
		return ""
	case "contains":
		return fmt.Sprintf(`refine(v => v.includes("%s"), { message: "must contain %s" })`, rule.Param, rule.Param)
	case "startswith":
		return fmt.Sprintf(`refine(v => v.startsWith("%s"), { message: "must start with %s" })`, rule.Param, rule.Param)
	case "endswith":
		return fmt.Sprintf(`refine(v => v.endsWith("%s"), { message: "must end with %s" })`, rule.Param, rule.Param)
	default:
		return ""
	}
}

// buildZodSchemas builds Zod schema data from interface data.
// Only includes interfaces that have at least one field with a validate tag.
//
// Output is topologically sorted so that leaf schemas always appear before
// schemas that reference them. This is required because the generated file
// uses top-level `const X = z.object({...})` declarations: a schema that
// substitutes an element schema (e.g., `Links: z.array(EventLinkInputSchema)`)
// must be evaluated after the element schema's `const` runs, otherwise the
// reference hits a temporal-dead-zone error at module init time.
func buildZodSchemas(interfaces []interfaceData) []zodSchemaData {
	// Pre-pass: collect the names of every interface that will produce a
	// schema, so slice/map element types can be substituted (#169). An
	// interface gets a schema iff it has at least one validate tag on any
	// field.
	knownSchemas := make(map[string]bool)
	for _, iface := range interfaces {
		for _, f := range iface.Fields {
			if f.ValidateTag != "" {
				knownSchemas[iface.Name] = true
				break
			}
		}
	}

	// Build the schema list in source order first.
	bySource := make([]zodSchemaData, 0, len(interfaces))
	for _, iface := range interfaces {
		if !knownSchemas[iface.Name] {
			continue
		}
		schema := zodSchemaData{Name: iface.Name}
		for _, f := range iface.Fields {
			schema.Fields = append(schema.Fields, zodFieldData{
				Name:    f.Name,
				ZodType: fieldToZod(f, knownSchemas),
			})
		}
		bySource = append(bySource, schema)
	}

	// Topologically sort so leaf schemas come before any schema that
	// references them. We collect dependencies by walking each interface's
	// field list once, looking at slice/map element type names. Cycles fall
	// back to source order (cycles are out of scope for #169 — the codegen
	// emits z.any() for self-referential structs).
	deps := make(map[string][]string, len(bySource))
	ifaceByName := make(map[string]interfaceData, len(interfaces))
	for _, iface := range interfaces {
		ifaceByName[iface.Name] = iface
	}
	for _, schema := range bySource {
		iface := ifaceByName[schema.Name]
		var schemaDeps []string
		seen := make(map[string]bool)
		for _, f := range iface.Fields {
			if f.ElemTypeName != "" && knownSchemas[f.ElemTypeName] && !seen[f.ElemTypeName] {
				schemaDeps = append(schemaDeps, f.ElemTypeName)
				seen[f.ElemTypeName] = true
			}
		}
		deps[schema.Name] = schemaDeps
	}

	return topoSortSchemas(bySource, deps)
}

// topoSortSchemas reorders schemas so that every schema appears after its
// dependencies. Uses iterative DFS with three-color marking; on cycles, the
// affected schemas are emitted in their original order (the runtime
// reference still works because cycles can only happen via z.any(), which
// doesn't dereference the cyclic name).
func topoSortSchemas(schemas []zodSchemaData, deps map[string][]string) []zodSchemaData {
	const (
		white = 0 // unvisited
		gray  = 1 // on stack (cycle marker)
		black = 2 // finished
	)
	color := make(map[string]int, len(schemas))
	byName := make(map[string]zodSchemaData, len(schemas))
	for _, s := range schemas {
		color[s.Name] = white
		byName[s.Name] = s
	}

	out := make([]zodSchemaData, 0, len(schemas))
	var visit func(name string)
	visit = func(name string) {
		if color[name] != white {
			return
		}
		color[name] = gray
		for _, dep := range deps[name] {
			if _, ok := byName[dep]; !ok {
				continue
			}
			if color[dep] == gray {
				// Cycle: skip this edge so the original-order fallback
				// is preserved for the affected schemas.
				continue
			}
			visit(dep)
		}
		color[name] = black
		out = append(out, byName[name])
	}

	// Walk in original order so unrelated schemas keep their declared order.
	for _, s := range schemas {
		visit(s.Name)
	}
	return out
}
