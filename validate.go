package aprot

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// StructValidator validates struct parameters before handler dispatch.
// Implementations must return a *ProtocolError with CodeValidationFailed
// and structured FieldError data when validation fails.
type StructValidator interface {
	ValidateStruct(v any) error
}

// FieldError describes a single field-level validation failure.
type FieldError struct {
	Field   string `json:"field"`   // JSON field name
	Tag     string `json:"tag"`     // validation tag that failed, e.g. "required", "min"
	Value   any    `json:"value"`   // the rejected value
	Param   string `json:"param"`   // tag parameter, e.g. "3" for min=3
	Message string `json:"message"` // human-readable message
}

// PlaygroundValidator wraps github.com/go-playground/validator/v10.
type PlaygroundValidator struct {
	v *validator.Validate
}

// NewPlaygroundValidator creates a StructValidator backed by go-playground/validator.
func NewPlaygroundValidator() *PlaygroundValidator {
	v := validator.New()
	// Use json tag names for field names in error messages
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := fld.Tag.Get("json")
		if name == "" || name == "-" {
			return ""
		}
		// Strip options like ",omitempty"
		if idx := strings.Index(name, ","); idx != -1 {
			name = name[:idx]
		}
		return name
	})
	return &PlaygroundValidator{v: v}
}

// Validate returns the underlying validator for registering custom validations.
func (p *PlaygroundValidator) Validate() *validator.Validate {
	return p.v
}

// ValidateStruct validates a struct and returns a *ProtocolError on failure.
func (p *PlaygroundValidator) ValidateStruct(v any) error {
	err := p.v.Struct(v)
	if err == nil {
		return nil
	}

	var validationErrors validator.ValidationErrors
	if ok := isValidationErrors(err, &validationErrors); !ok {
		return ErrInvalidParams(err.Error())
	}

	fields := make([]FieldError, len(validationErrors))
	for i, fe := range validationErrors {
		fields[i] = FieldError{
			Field:   fe.Field(),
			Tag:     fe.Tag(),
			Value:   fe.Value(),
			Param:   fe.Param(),
			Message: formatFieldError(fe),
		}
	}

	return &ProtocolError{
		Code:    CodeValidationFailed,
		Message: "validation failed",
		Data:    fields,
	}
}

// isValidationErrors checks if err is a validator.ValidationErrors.
func isValidationErrors(err error, target *validator.ValidationErrors) bool {
	if ve, ok := err.(validator.ValidationErrors); ok {
		*target = ve
		return true
	}
	return false
}

// formatFieldError builds a human-readable message for a validation error.
func formatFieldError(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return fmt.Sprintf("%s is required", fe.Field())
	case "min":
		return fmt.Sprintf("%s must be at least %s", fe.Field(), fe.Param())
	case "max":
		return fmt.Sprintf("%s must be at most %s", fe.Field(), fe.Param())
	case "gte":
		return fmt.Sprintf("%s must be >= %s", fe.Field(), fe.Param())
	case "lte":
		return fmt.Sprintf("%s must be <= %s", fe.Field(), fe.Param())
	case "gt":
		return fmt.Sprintf("%s must be > %s", fe.Field(), fe.Param())
	case "lt":
		return fmt.Sprintf("%s must be < %s", fe.Field(), fe.Param())
	case "email":
		return fmt.Sprintf("%s must be a valid email", fe.Field())
	case "url":
		return fmt.Sprintf("%s must be a valid URL", fe.Field())
	case "uuid":
		return fmt.Sprintf("%s must be a valid UUID", fe.Field())
	case "oneof":
		return fmt.Sprintf("%s must be one of: %s", fe.Field(), fe.Param())
	case "len":
		return fmt.Sprintf("%s must have length %s", fe.Field(), fe.Param())
	default:
		return fmt.Sprintf("%s failed %s validation", fe.Field(), fe.Tag())
	}
}

// ValidateRule represents a single parsed validation constraint.
type ValidateRule struct {
	Tag   string // e.g., "required", "min", "email"
	Param string // e.g., "3" for min=3, "" for email
}

// ParseValidateTag splits a validate tag string into structured rules.
// For example: "required,gte=12,lte=110" -> [{Tag:"required"}, {Tag:"gte", Param:"12"}, {Tag:"lte", Param:"110"}]
func ParseValidateTag(tag string) []ValidateRule {
	if tag == "" {
		return nil
	}
	parts := strings.Split(tag, ",")
	rules := make([]ValidateRule, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "="); idx >= 0 {
			rules = append(rules, ValidateRule{
				Tag:   part[:idx],
				Param: part[idx+1:],
			})
		} else {
			rules = append(rules, ValidateRule{Tag: part})
		}
	}
	return rules
}
