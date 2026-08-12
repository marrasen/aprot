package aprot

import (
	"bytes"
	"strings"
	"testing"
)

// DrawOption is the case EnumNamer exists for: a string enum whose values are
// one character each, so the member name derived from the value says nothing
// the Go constant said.
type DrawOption string

const (
	DrawOptionFilled       DrawOption = "F"
	DrawOptionOutline      DrawOption = "O"
	DrawOptionBoundingRect DrawOption = "B"
)

func (d DrawOption) EnumMemberName() string {
	switch d {
	case DrawOptionFilled:
		return "Filled"
	case DrawOptionOutline:
		return "Outline"
	case DrawOptionBoundingRect:
		return "BoundingRect"
	}
	return string(d)
}

func DrawOptionValues() []DrawOption {
	return []DrawOption{DrawOptionFilled, DrawOptionOutline, DrawOptionBoundingRect}
}

// ClashingOption names two values the same, which cannot be generated.
type ClashingOption string

const (
	ClashingOptionOne ClashingOption = "1"
	ClashingOptionTwo ClashingOption = "2"
)

func (c ClashingOption) EnumMemberName() string { return "Same" }

func TestEnumNamerNamesMembersAfterTheConstants(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterEnum(DrawOptionValues())

	enums := registry.Enums()
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}

	e := enums[0]
	if !e.IsString {
		t.Error("DrawOption should be a string enum")
	}

	want := []EnumValueInfo{
		{Name: "Filled", Value: "F"},
		{Name: "Outline", Value: "O"},
		{Name: "BoundingRect", Value: "B"},
	}
	if len(e.Values) != len(want) {
		t.Fatalf("expected %d values, got %d", len(want), len(e.Values))
	}
	for i, w := range want {
		if e.Values[i].Name != w.Name {
			t.Errorf("value %d: expected name %q, got %q", i, w.Name, e.Values[i].Name)
		}
		// The wire value is untouched: this renames the member, not the data.
		if e.Values[i].Value != w.Value {
			t.Errorf("value %d: expected value %q, got %q", i, w.Value, e.Values[i].Value)
		}
	}
}

// TestEnumNamerIsOptional: a string enum without the interface keeps the name
// it has always had, so adopting this cannot rename anyone else's members.
func TestEnumNamerIsOptional(t *testing.T) {
	registry := NewRegistry()
	handler := &TaskHandlers{}
	registry.Register(handler)
	registry.RegisterEnumFor(handler, TaskStatusValues())

	for _, e := range registry.Enums() {
		if e.Name != "TaskStatus" {
			continue
		}
		if e.Values[0].Name != "Created" {
			t.Errorf("expected the value capitalised, got %q", e.Values[0].Name)
		}
	}
}

func TestEnumNamerRejectsDuplicateNames(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic for two values named the same")
		}
		if !strings.Contains(toString(r), "ClashingOption") {
			t.Errorf("the panic should name the enum, got %v", r)
		}
	}()

	registry := NewRegistry()
	registry.RegisterEnum([]ClashingOption{ClashingOptionOne, ClashingOptionTwo})
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return ""
}

// TestEnumNamerGeneratesNamedMembers checks the whole way out to TypeScript:
// the member is named after the constant and still carries the wire value.
func TestEnumNamerGeneratesNamedMembers(t *testing.T) {
	registry := NewRegistry()
	handler := &TaskHandlers{}
	registry.Register(handler)
	registry.RegisterEnumFor(handler, DrawOptionValues())

	var buf bytes.Buffer
	if err := NewGenerator(registry).GenerateTo(&buf); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	out := buf.String()
	for _, want := range []string{`Filled: "F"`, `Outline: "O"`, `BoundingRect: "B"`} {
		if !strings.Contains(out, want) {
			t.Errorf("generated client is missing %s\n%s", want, out)
		}
	}
	if strings.Contains(out, `F: "F"`) {
		t.Error("the member should be named after the constant, not the value")
	}
}
