package aprot

import (
	"context"
	"errors"
	"testing"
)

type SetAgeRequest struct {
	UserID int `json:"userId" validate:"required,min=1"`
	Age    int `json:"age"    validate:"required,gte=12,lte=110"`
}

type ValidatedHandlers struct{}

func (h *ValidatedHandlers) SetAge(ctx context.Context, req *SetAgeRequest) error {
	return nil
}

func TestValidation_RejectsInvalidStruct(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ValidatedHandlers{})
	registry.SetValidator(NewPlaygroundValidator())

	info, ok := registry.Get("ValidatedHandlers.SetAge")
	if !ok {
		t.Fatal("handler not found")
	}

	// Age=5 should fail (gte=12)
	_, err := info.Call(context.Background(), []byte(`[{"userId": 1, "age": 5}]`))
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	var perr *ProtocolError
	if !errors.As(err, &perr) {
		t.Fatalf("expected *ProtocolError, got %T: %v", err, err)
	}
	if perr.Code != CodeValidationFailed {
		t.Errorf("expected code %d, got %d", CodeValidationFailed, perr.Code)
	}

	fields, ok := perr.Data.([]FieldError)
	if !ok {
		t.Fatalf("expected []FieldError in Data, got %T", perr.Data)
	}
	if len(fields) != 1 {
		t.Fatalf("expected 1 field error, got %d: %+v", len(fields), fields)
	}
	if fields[0].Field != "age" {
		t.Errorf("expected field 'age', got %q", fields[0].Field)
	}
	if fields[0].Tag != "gte" {
		t.Errorf("expected tag 'gte', got %q", fields[0].Tag)
	}
}

func TestValidation_AcceptsValidStruct(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ValidatedHandlers{})
	registry.SetValidator(NewPlaygroundValidator())

	info, ok := registry.Get("ValidatedHandlers.SetAge")
	if !ok {
		t.Fatal("handler not found")
	}

	_, err := info.Call(context.Background(), []byte(`[{"userId": 1, "age": 25}]`))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidation_SkippedWhenNoValidator(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ValidatedHandlers{})
	// No SetValidator call

	info, ok := registry.Get("ValidatedHandlers.SetAge")
	if !ok {
		t.Fatal("handler not found")
	}

	// Invalid data should pass through without validation
	_, err := info.Call(context.Background(), []byte(`[{"userId": 0, "age": 5}]`))
	if err != nil {
		t.Fatalf("expected no error without validator, got: %v", err)
	}
}

func TestValidation_MultipleErrors(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&ValidatedHandlers{})
	registry.SetValidator(NewPlaygroundValidator())

	info, ok := registry.Get("ValidatedHandlers.SetAge")
	if !ok {
		t.Fatal("handler not found")
	}

	// Both userId=0 (min=1) and age=5 (gte=12) should fail
	_, err := info.Call(context.Background(), []byte(`[{"userId": 0, "age": 5}]`))
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	var perr *ProtocolError
	if !errors.As(err, &perr) {
		t.Fatalf("expected *ProtocolError, got %T: %v", err, err)
	}

	fields, ok := perr.Data.([]FieldError)
	if !ok {
		t.Fatalf("expected []FieldError, got %T", perr.Data)
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 field errors, got %d: %+v", len(fields), fields)
	}
}

func TestParseValidateTag(t *testing.T) {
	tests := []struct {
		tag  string
		want []ValidateRule
	}{
		{"", nil},
		{"required", []ValidateRule{{Tag: "required"}}},
		{"required,email", []ValidateRule{{Tag: "required"}, {Tag: "email"}}},
		{"gte=12,lte=110", []ValidateRule{{Tag: "gte", Param: "12"}, {Tag: "lte", Param: "110"}}},
		{"oneof=red green blue", []ValidateRule{{Tag: "oneof", Param: "red green blue"}}},
	}

	for _, tt := range tests {
		got := ParseValidateTag(tt.tag)
		if len(got) != len(tt.want) {
			t.Errorf("ParseValidateTag(%q): got %d rules, want %d", tt.tag, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseValidateTag(%q)[%d]: got %+v, want %+v", tt.tag, i, got[i], tt.want[i])
			}
		}
	}
}
