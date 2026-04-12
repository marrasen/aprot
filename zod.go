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

// goTypeToZod converts a Go type + validate tag to a Zod schema string.
// goKind is "string", "int", "uint", "float", "bool", "slice", "map", or "struct".
// tsType is the TypeScript type from goTypeToTS.
// validateTag is the raw validate struct tag.
// sqlNullKind is non-empty for database/sql nullable wrappers (NullString, etc.);
// its value is the unwrapped kind ("string", "int", "float", "bool") and the
// emitted chain gets a trailing .nullable().
func goTypeToZod(goKind string, tsType string, validateTag string, optional bool, sqlNullKind string) string {
	rules := ParseValidateTag(validateTag)

	// Issue 3: sql.Null* wrappers. Use the unwrapped kind for base + constraints,
	// then append .nullable() after constraints but before .optional().
	effectiveKind := goKind
	isNullable := false
	if sqlNullKind != "" {
		effectiveKind = sqlNullKind
		isNullable = true
	}

	// Issue 1: validate-tag omitempty forces .optional(). For string kind, also
	// wrap the chain in z.union([z.literal(""), ...]) so empty strings pass —
	// go-playground/validator skips subsequent rules on the zero value ("" for
	// strings), and client forms almost always emit "" rather than undefined.
	hasValidateOmitempty := false
	for _, r := range rules {
		if r.Tag == "omitempty" {
			hasValidateOmitempty = true
			break
		}
	}
	if hasValidateOmitempty {
		optional = true
	}

	// Issue 2: if an explicit min rule is present on a string, skip the
	// implicit required -> min(1) so we don't emit .min(1).min(N).
	hasExplicitMin := false
	for _, r := range rules {
		if r.Tag == "min" {
			hasExplicitMin = true
			break
		}
	}

	var chain []string
	chain = append(chain, zodBaseType(effectiveKind, tsType))

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

// zodBaseType returns the base Zod type for a Go kind.
func zodBaseType(goKind string, tsType string) string {
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
		// For arrays, we wrap the element type
		return "array(z.any())"
	case "map":
		return "record(z.string(), z.any())"
	default:
		// Struct or unknown — use any()
		return "any()"
	}
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
func buildZodSchemas(interfaces []interfaceData) []zodSchemaData {
	var schemas []zodSchemaData
	for _, iface := range interfaces {
		hasValidation := false
		for _, f := range iface.Fields {
			if f.ValidateTag != "" {
				hasValidation = true
				break
			}
		}
		if !hasValidation {
			continue
		}

		schema := zodSchemaData{Name: iface.Name}
		for _, f := range iface.Fields {
			schema.Fields = append(schema.Fields, zodFieldData{
				Name:    f.Name,
				ZodType: goTypeToZod(f.GoType, f.Type, f.ValidateTag, f.Optional, f.SQLNullKind),
			})
		}
		schemas = append(schemas, schema)
	}
	return schemas
}
