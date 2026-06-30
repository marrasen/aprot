package aprot

import (
	"fmt"
	"strconv"
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
	// A bare pointer field (no json omitempty) is nullable on the wire — its TS
	// type is `T | null`, so its schema must accept null too (#207).
	if f.Nullable {
		isNullable = true
	}

	// Issue 1 (#163): validate-tag omitempty on strings wraps the chain in
	// z.union([z.literal(""), ...]) so empty strings pass the schema — the
	// go-playground/validator skips subsequent rules on the zero value ("")
	// and client forms almost always emit "" rather than undefined.
	//
	// Issue #178: validate-omitempty does NOT imply Zod .optional(). It only
	// tells the Go validator to skip rules on the zero value; a non-pointer,
	// non-json-omitempty field is still always marshaled to the wire. The TS
	// interface generator (isOptional) agrees — it only flags pointer /
	// json:,omitempty fields as optional, so Zod must do the same to keep
	// z.infer assignable to the generated params type. Optionality stays
	// gated on f.Optional alone.
	hasValidateOmitempty := false
	for _, r := range rules {
		if r.Tag == "omitempty" {
			hasValidateOmitempty = true
			break
		}
	}
	optional := f.Optional

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

	// oneof restricts the value to a fixed set, so (like a registered enum) it
	// replaces the base type with z.enum(...) / z.union(z.literal(...)) rather
	// than appending a chain method. Honoring it here keeps z.infer matching
	// the documented "value must be one of" semantics instead of silently
	// dropping the rule.
	for _, r := range rules {
		if r.Tag != "oneof" {
			continue
		}
		body := zodOneof(effectiveKind, r.Param)
		if body == "" {
			break
		}
		if isNullable {
			body += ".nullable()"
		}
		if hasValidateOmitempty && effectiveKind == "string" {
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
	chain = append(chain, zodBaseType(effectiveKind, f.Type, f.ElemGoKind, f.ElemTypeName, f.ElemEnum, f.ElemLazy, knownSchemas))

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
func zodBaseType(goKind string, tsType string, elemGoKind string, elemTypeName string, elemEnum *EnumInfo, elemLazy bool, knownSchemas map[string]bool) string {
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
		return "array(" + zodElemExpr(elemGoKind, elemTypeName, elemEnum, elemLazy, knownSchemas) + ")"
	case "map":
		return "record(z.string(), " + zodElemExpr(elemGoKind, elemTypeName, elemEnum, elemLazy, knownSchemas) + ")"
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
func zodElemExpr(elemGoKind string, elemTypeName string, elemEnum *EnumInfo, elemLazy bool, knownSchemas map[string]bool) string {
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
			if elemLazy {
				// Cyclic reference — defer evaluation so the cyclic const is
				// not dereferenced before it is initialized (#207).
				return "z.lazy(() => " + elemTypeName + "Schema)"
			}
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
			parts = append(parts, strconv.Quote(s))
		}
		return "enum([" + strings.Join(parts, ", ") + "])"
	}
	parts := make([]string, 0, len(info.Values))
	for _, v := range info.Values {
		parts = append(parts, fmt.Sprintf("z.literal(%d)", v.Value))
	}
	return "union([" + strings.Join(parts, ", ") + "])"
}

// zodOneof builds the Zod expression for a oneof validate rule. The param is a
// space-separated value list (go-playground syntax). String kinds become
// z.enum([...]); numeric kinds become a z.union of z.literal(...) (or a single
// z.literal when there is only one value, since z.union requires two members).
// Returns "" for kinds where oneof has no literal representation.
func zodOneof(goKind string, param string) string {
	vals := strings.Fields(param)
	if len(vals) == 0 {
		return ""
	}
	switch goKind {
	case "string":
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = strconv.Quote(v)
		}
		return "z.enum([" + strings.Join(parts, ", ") + "])"
	case "int", "uint", "float":
		if len(vals) == 1 {
			return "z.literal(" + vals[0] + ")"
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = "z.literal(" + v + ")"
		}
		return "z.union([" + strings.Join(parts, ", ") + "])"
	default:
		return ""
	}
}

// zodConstraint maps a single validate rule to a Zod chain method.
func zodConstraint(goKind string, rule ValidateRule) string {
	// z.record() exposes no length/size constraint methods, so a size rule on
	// a map would otherwise emit invalid TS like z.record(...).min(...). Drop
	// size constraints for maps rather than emit something that won't parse.
	if goKind == "map" {
		return ""
	}
	// For strings and slices the go-playground validator's gt/lt operate on
	// length, but Zod strings and arrays have no numeric .gt()/.lt() — those
	// exist only on z.number(). Translate the strict length comparison to the
	// inclusive .min()/.max() form (gt=n → min(n+1), lt=n → max(n-1)).
	if goKind == "string" || goKind == "slice" {
		switch rule.Tag {
		case "gt":
			if n, err := strconv.Atoi(rule.Param); err == nil {
				return fmt.Sprintf("min(%d)", n+1)
			}
			return ""
		case "lt":
			if n, err := strconv.Atoi(rule.Param); err == nil {
				return fmt.Sprintf("max(%d)", n-1)
			}
			return ""
		}
	}
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
		// Handled in fieldToZod via zodOneof — oneof replaces the base type
		// (z.enum / z.union of literals) rather than appending a chain method.
		return ""
	case "contains":
		return fmt.Sprintf(`refine(v => v.includes(%s), { message: %s })`, strconv.Quote(rule.Param), strconv.Quote("must contain "+rule.Param))
	case "startswith":
		return fmt.Sprintf(`refine(v => v.startsWith(%s), { message: %s })`, strconv.Quote(rule.Param), strconv.Quote("must start with "+rule.Param))
	case "endswith":
		return fmt.Sprintf(`refine(v => v.endsWith(%s), { message: %s })`, strconv.Quote(rule.Param), strconv.Quote("must end with "+rule.Param))
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

	// Collect each schema's slice/map element dependencies (only those that are
	// themselves generated schemas). This drives both the cycle analysis below
	// and the topological sort.
	deps := make(map[string][]string, len(interfaces))
	for _, iface := range interfaces {
		if !knownSchemas[iface.Name] {
			continue
		}
		var schemaDeps []string
		seen := make(map[string]bool)
		for _, f := range iface.Fields {
			if f.ElemTypeName != "" && knownSchemas[f.ElemTypeName] && !seen[f.ElemTypeName] {
				schemaDeps = append(schemaDeps, f.ElemTypeName)
				seen[f.ElemTypeName] = true
			}
		}
		deps[iface.Name] = schemaDeps
	}

	// Build the schema list in source order. A field whose element schema can
	// reach back to the schema being built participates in a reference cycle
	// (self-referential or mutually recursive); mark it ElemLazy so fieldToZod
	// wraps the reference in z.lazy(() => XSchema) and avoids a temporal-dead-
	// zone ReferenceError at module load (#207).
	bySource := make([]zodSchemaData, 0, len(interfaces))
	for _, iface := range interfaces {
		if !knownSchemas[iface.Name] {
			continue
		}
		schema := zodSchemaData{Name: iface.Name}
		for _, f := range iface.Fields {
			if f.ElemTypeName != "" && knownSchemas[f.ElemTypeName] && depReaches(deps, f.ElemTypeName, iface.Name) {
				f.ElemLazy = true
			}
			schema.Fields = append(schema.Fields, zodFieldData{
				Name:    f.Name,
				ZodType: fieldToZod(f, knownSchemas),
			})
		}
		bySource = append(bySource, schema)
	}

	// Topologically sort so leaf schemas come before any schema that
	// references them. Cycles fall back to source order; their cross-references
	// are emitted lazily (see ElemLazy above), so the runtime reference still
	// resolves regardless of declaration order.
	return topoSortSchemas(bySource, deps)
}

// depReaches reports whether schema `from` can reach schema `to` by following
// element dependency edges. Used to detect reference cycles: an edge A→B is
// cyclic (and must be emitted lazily) exactly when B can reach A.
func depReaches(deps map[string][]string, from, to string) bool {
	visited := make(map[string]bool)
	stack := []string{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, dep := range deps[n] {
			if dep == to {
				return true
			}
			if !visited[dep] {
				visited[dep] = true
				stack = append(stack, dep)
			}
		}
	}
	return false
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
