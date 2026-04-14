package aprot

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

// ValidateTransformTags statically checks every `transform:""` tag
// reachable from t (a struct type or pointer to one). It catches
// unknown op names, ops used on unsupported field kinds, and
// `removeempty` on anything other than `[]string` — all at registration
// time, so the problem surfaces when the server boots rather than on
// the first request that happens to hit the handler.
//
// Non-struct types are a no-op; caller is responsible for passing the
// param type. Recursion into nested structs, *struct, and slices/arrays
// of struct (or *struct) elements mirrors the runtime walker. Cycles
// are broken by tracking visited types.
func ValidateTransformTags(t reflect.Type) error {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	return validateTransformTagsType(t, map[reflect.Type]bool{})
}

func validateTransformTagsType(t reflect.Type, seen map[reflect.Type]bool) error {
	if seen[t] {
		return nil
	}
	seen[t] = true

	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("transform")
		if tag != "" {
			if err := validateTransformTagOnField(sf, ParseValidateTag(tag)); err != nil {
				return err
			}
		}
		if err := validateTransformTagsChildren(sf.Type, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateTransformTagsChildren(ft reflect.Type, seen map[reflect.Type]bool) error {
	switch ft.Kind() {
	case reflect.Struct:
		return validateTransformTagsType(ft, seen)
	case reflect.Ptr:
		if ft.Elem().Kind() == reflect.Struct {
			return validateTransformTagsType(ft.Elem(), seen)
		}
	case reflect.Slice, reflect.Array:
		et := ft.Elem()
		if et.Kind() == reflect.Struct {
			return validateTransformTagsType(et, seen)
		}
		if et.Kind() == reflect.Ptr && et.Elem().Kind() == reflect.Struct {
			return validateTransformTagsType(et.Elem(), seen)
		}
	}
	return nil
}

func validateTransformTagOnField(sf reflect.StructField, rules []ValidateRule) error {
	ft := sf.Type
	isStringField := ft.Kind() == reflect.String ||
		(ft.Kind() == reflect.Ptr && ft.Elem().Kind() == reflect.String)
	isStringSlice := ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String

	if !isStringField && !isStringSlice {
		return fmt.Errorf("aprot: transform tag on field %q has unsupported type %s (only string, *string, and []string are supported)", sf.Name, ft)
	}

	for _, r := range rules {
		switch r.Tag {
		case "trim", "trimleft", "trimright", "uppercase", "lowercase":
			// valid on string, *string, and []string (per-element)
		case "removeempty":
			if !isStringSlice {
				return fmt.Errorf("aprot: transform tag on field %q uses removeempty on non-[]string type %s", sf.Name, ft)
			}
		default:
			return fmt.Errorf("aprot: transform tag on field %q has unknown op %q", sf.Name, r.Tag)
		}
	}
	return nil
}

// ApplyTransforms walks v (a struct or pointer to struct) and applies the
// operations declared in `transform:""` tags on its exported fields. It
// mutates the value in place.
//
// Supported ops:
//   - trim                  strings.TrimSpace
//   - trimleft[=cutset]     TrimLeft (default cutset: whitespace)
//   - trimright[=cutset]    TrimRight (default cutset: whitespace)
//   - uppercase             strings.ToUpper
//   - lowercase             strings.ToLower
//   - removeempty           ([]string only) drop empty elements
//
// Ops apply in the order listed in the tag, so `transform:"trim,removeempty"`
// on a []string first trims each element and then drops the empties.
//
// Non-struct inputs are a no-op. Unknown op names or type mismatches
// (e.g. removeempty on a non-slice field) return an *ProtocolError with
// CodeInvalidParams.
func ApplyTransforms(v any) error {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	return applyTransformsValue(rv)
}

// applyTransformsValue recursively walks a struct value. rv must be a
// struct (not a pointer) and addressable so that fields can be mutated.
func applyTransformsValue(rv reflect.Value) error {
	t := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := rv.Field(i)
		tag := sf.Tag.Get("transform")
		rules := ParseValidateTag(tag)

		if len(rules) > 0 {
			if err := applyFieldRules(sf, fv, rules); err != nil {
				return err
			}
		}

		// Always recurse into nested structs / struct containers so
		// nested `transform` tags are discovered even when the parent
		// field has no tag of its own.
		if err := recurseIntoField(fv); err != nil {
			return err
		}
	}
	return nil
}

func recurseIntoField(fv reflect.Value) error {
	switch fv.Kind() {
	case reflect.Struct:
		return applyTransformsValue(fv)
	case reflect.Ptr:
		if fv.IsNil() {
			return nil
		}
		if fv.Elem().Kind() == reflect.Struct {
			return applyTransformsValue(fv.Elem())
		}
	case reflect.Slice, reflect.Array:
		et := fv.Type().Elem()
		if et.Kind() == reflect.Struct {
			for i := 0; i < fv.Len(); i++ {
				if err := applyTransformsValue(fv.Index(i)); err != nil {
					return err
				}
			}
		} else if et.Kind() == reflect.Ptr && et.Elem().Kind() == reflect.Struct {
			for i := 0; i < fv.Len(); i++ {
				el := fv.Index(i)
				if el.IsNil() {
					continue
				}
				if err := applyTransformsValue(el.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func applyFieldRules(sf reflect.StructField, fv reflect.Value, rules []ValidateRule) error {
	switch fv.Kind() {
	case reflect.String:
		s, err := applyStringOps(sf.Name, fv.String(), rules, false)
		if err != nil {
			return err
		}
		fv.SetString(s)
		return nil

	case reflect.Ptr:
		if fv.Type().Elem().Kind() != reflect.String {
			return nil
		}
		if fv.IsNil() {
			return nil
		}
		s, err := applyStringOps(sf.Name, fv.Elem().String(), rules, false)
		if err != nil {
			return err
		}
		fv.Elem().SetString(s)
		return nil

	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			// removeempty is only valid on []string; bail with a helpful
			// error if the user put transform ops on a non-string slice.
			return ErrInvalidParams(fmt.Sprintf("transform: field %q has slice type %s, only []string is supported", sf.Name, fv.Type()))
		}
		return applySliceStringOps(sf.Name, fv, rules)

	default:
		return ErrInvalidParams(fmt.Sprintf("transform: field %q has unsupported type %s", sf.Name, fv.Type()))
	}
}

func applyStringOps(field, s string, rules []ValidateRule, inSlice bool) (string, error) {
	for _, r := range rules {
		switch r.Tag {
		case "trim":
			s = strings.TrimSpace(s)
		case "trimleft":
			if r.Param == "" {
				s = strings.TrimLeftFunc(s, unicode.IsSpace)
			} else {
				s = strings.TrimLeft(s, r.Param)
			}
		case "trimright":
			if r.Param == "" {
				s = strings.TrimRightFunc(s, unicode.IsSpace)
			} else {
				s = strings.TrimRight(s, r.Param)
			}
		case "uppercase":
			s = strings.ToUpper(s)
		case "lowercase":
			s = strings.ToLower(s)
		case "removeempty":
			if !inSlice {
				return "", ErrInvalidParams(fmt.Sprintf("transform: field %q uses removeempty on non-slice type", field))
			}
			// handled by caller
		default:
			return "", ErrInvalidParams(fmt.Sprintf("transform: field %q has unknown op %q", field, r.Tag))
		}
	}
	return s, nil
}

func applySliceStringOps(field string, fv reflect.Value, rules []ValidateRule) error {
	// Walk rules once, applying per-element string ops and filtering
	// empties in place when `removeempty` appears. Order matters: ops
	// before `removeempty` run on every element; ops after it run only
	// on the surviving elements.
	n := fv.Len()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fv.Index(i).String())
	}

	for _, r := range rules {
		if r.Tag == "removeempty" {
			filtered := out[:0]
			for _, s := range out {
				if s != "" {
					filtered = append(filtered, s)
				}
			}
			out = filtered
			continue
		}
		// Apply this single op to each element via applyStringOps with a
		// one-rule slice so param handling stays centralized.
		one := []ValidateRule{r}
		for i, s := range out {
			ns, err := applyStringOps(field, s, one, true)
			if err != nil {
				return err
			}
			out[i] = ns
		}
	}

	// Write back. If the input slice was nil and no elements survive,
	// leave it nil to avoid surprising the caller.
	if fv.IsNil() && len(out) == 0 {
		return nil
	}
	result := reflect.MakeSlice(fv.Type(), len(out), len(out))
	for i, s := range out {
		result.Index(i).SetString(s)
	}
	fv.Set(result)
	return nil
}
