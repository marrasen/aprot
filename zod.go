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
func goTypeToZod(goKind string, tsType string, validateTag string, optional bool) string {
	base := zodBaseType(goKind, tsType)
	rules := ParseValidateTag(validateTag)

	var chain []string
	chain = append(chain, base)

	for _, r := range rules {
		if zod := zodConstraint(goKind, r); zod != "" {
			chain = append(chain, zod)
		}
	}

	if optional {
		chain = append(chain, "optional()")
	}

	return "z." + strings.Join(chain, ".")
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
				ZodType: goTypeToZod(f.GoType, f.Type, f.ValidateTag, f.Optional),
			})
		}
		schemas = append(schemas, schema)
	}
	return schemas
}
