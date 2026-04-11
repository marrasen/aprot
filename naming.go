package aprot

import (
	"strings"
	"unicode"
)

// NamingPlugin controls how Go names are transformed into TypeScript names
// during code generation and REST path construction.
type NamingPlugin interface {
	// FileName converts a handler group name (e.g. "PublicHandlers") to a
	// filename stem (without extension). The generator appends ".ts".
	FileName(groupName string) string

	// MethodName converts a Go handler method name (e.g. "CreateUser") to a
	// TypeScript function name (e.g. "createUser" or "CreateUser").
	MethodName(name string) string

	// HookName converts a Go handler/event name to a React hook name.
	// Default: "use" + name (e.g. "useCreateUser").
	HookName(name string) string

	// HandlerName converts a push event name to an event handler callback name.
	// Default: "on" + name (e.g. "onUserCreated").
	HandlerName(eventName string) string

	// ErrorMethodName converts a custom error code name to a type-guard method name.
	// Default: "is" + name (e.g. "isNotFound").
	ErrorMethodName(errorName string) string

	// PathPrefix converts a handler group name to a URL path prefix.
	// Default: "/" + kebab-case (e.g. "UserHandlers" → "/user-handlers").
	PathPrefix(groupName string) string

	// PathSegment converts a method name to a URL path segment.
	// Default: kebab-case (e.g. "UpdateUser" → "update-user").
	PathSegment(methodName string) string
}

// DefaultNaming reproduces the current naming behavior.
// Set FixAcronyms to true to treat consecutive uppercase letters as a single
// word (e.g. "BulkXMLHandlers" → "bulk-xml-handlers" instead of "bulk-x-m-l-handlers").
type DefaultNaming struct {
	FixAcronyms bool
}

func (d DefaultNaming) FileName(groupName string) string {
	if d.FixAcronyms {
		return toKebabAcronyms(groupName)
	}
	return toKebab(groupName)
}

func (d DefaultNaming) MethodName(name string) string {
	if d.FixAcronyms {
		return toLowerCamelAcronyms(name)
	}
	return toLowerCamel(name)
}

func (d DefaultNaming) HookName(name string) string {
	return "use" + name
}

func (d DefaultNaming) HandlerName(eventName string) string {
	return "on" + eventName
}

func (d DefaultNaming) ErrorMethodName(errorName string) string {
	return "is" + errorName
}

func (d DefaultNaming) PathPrefix(groupName string) string {
	return "/" + d.FileName(groupName)
}

func (d DefaultNaming) PathSegment(methodName string) string {
	if d.FixAcronyms {
		return toKebabAcronyms(methodName)
	}
	return toKebab(methodName)
}

// PreserveNaming keeps Go PascalCase method names unchanged in the generated
// TypeScript. Filenames still use kebab-case (filesystem convention).
type PreserveNaming struct {
	FixAcronyms bool
}

func (p PreserveNaming) FileName(groupName string) string {
	if p.FixAcronyms {
		return toKebabAcronyms(groupName)
	}
	return toKebab(groupName)
}

func (p PreserveNaming) MethodName(name string) string {
	return name
}

func (p PreserveNaming) HookName(name string) string {
	return "use" + name
}

func (p PreserveNaming) HandlerName(eventName string) string {
	return "on" + eventName
}

func (p PreserveNaming) ErrorMethodName(errorName string) string {
	return "is" + errorName
}

func (p PreserveNaming) PathPrefix(groupName string) string {
	return "/" + p.FileName(groupName)
}

func (p PreserveNaming) PathSegment(methodName string) string {
	if p.FixAcronyms {
		return toKebabAcronyms(methodName)
	}
	return toKebab(methodName)
}

// toKebabAcronyms converts PascalCase to kebab-case, treating consecutive
// uppercase letters as a single acronym word.
//
// Examples:
//
//	"PublicHandlers"  → "public-handlers"
//	"BulkXMLHandlers" → "bulk-xml-handlers"
//	"XMLParser"       → "xml-parser"
//	"MyHTTPSServer"   → "my-https-server"
func toKebabAcronyms(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	var result strings.Builder
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !unicode.IsUpper(r) {
			result.WriteRune(r)
			continue
		}
		// r is uppercase. Determine if it starts a new word.
		if i > 0 {
			result.WriteRune('-')
		}
		// Collect the uppercase run.
		j := i
		for j < len(runes) && unicode.IsUpper(runes[j]) {
			j++
		}
		runLen := j - i
		if runLen == 1 || j == len(runes) {
			// Single uppercase letter or acronym at end of string — emit all lowercase.
			for k := i; k < j; k++ {
				result.WriteRune(unicode.ToLower(runes[k]))
			}
			i = j - 1
		} else {
			// Multiple uppercase followed by lowercase: last uppercase starts the next word.
			for k := i; k < j-1; k++ {
				result.WriteRune(unicode.ToLower(runes[k]))
			}
			// The last uppercase char of the run starts a new word.
			i = j - 2 // -1 because the loop does i++
		}
	}
	return result.String()
}

// toLowerCamelAcronyms converts PascalCase to camelCase, lowercasing a leading
// acronym run rather than just the first character.
//
// Examples:
//
//	"CreateUser"      → "createUser"
//	"XMLParser"       → "xmlParser"
//	"HTTPSConnection" → "httpsConnection"
func toLowerCamelAcronyms(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	// Find the length of the leading uppercase run.
	upper := 0
	for upper < len(runes) && unicode.IsUpper(runes[upper]) {
		upper++
	}
	if upper == 0 {
		return s
	}
	// If the entire string is uppercase, lowercase all.
	if upper == len(runes) {
		for i := 0; i < len(runes); i++ {
			runes[i] = unicode.ToLower(runes[i])
		}
		return string(runes)
	}
	// If only one uppercase letter, just lowercase it.
	if upper == 1 {
		runes[0] = unicode.ToLower(runes[0])
		return string(runes)
	}
	// Multiple uppercase: lowercase all except the last (which starts the next word).
	for i := 0; i < upper-1; i++ {
		runes[i] = unicode.ToLower(runes[i])
	}
	return string(runes)
}
