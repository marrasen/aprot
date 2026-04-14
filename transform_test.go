package aprot

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestApplyTransforms_StringBasics(t *testing.T) {
	type req struct {
		Trim  string `transform:"trim"`
		Upper string `transform:"uppercase"`
		Lower string `transform:"lowercase"`
		Combo string `transform:"trim,lowercase"`
		Untag string
		unexp string `transform:"trim"` //nolint:unused // tag on unexported field must be ignored
	}
	r := &req{
		Trim:  "  hello  ",
		Upper: "hello",
		Lower: "HELLO",
		Combo: "  HELLO  ",
		Untag: "  keep  ",
		unexp: "  skip  ",
	}
	if err := ApplyTransforms(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Trim != "hello" {
		t.Errorf("Trim = %q", r.Trim)
	}
	if r.Upper != "HELLO" {
		t.Errorf("Upper = %q", r.Upper)
	}
	if r.Lower != "hello" {
		t.Errorf("Lower = %q", r.Lower)
	}
	if r.Combo != "hello" {
		t.Errorf("Combo = %q", r.Combo)
	}
	if r.Untag != "  keep  " {
		t.Errorf("Untag = %q (should be unchanged)", r.Untag)
	}
	if r.unexp != "  skip  " {
		t.Errorf("unexp = %q (should be unchanged)", r.unexp)
	}
}

func TestApplyTransforms_TrimLeftRight(t *testing.T) {
	type req struct {
		DefaultLeft  string `transform:"trimleft"`
		DefaultRight string `transform:"trimright"`
		CutsetLeft   string `transform:"trimleft=/"`
		CutsetRight  string `transform:"trimright=/"`
		CutsetBoth   string `transform:"trimleft=/,trimright=/"`
	}
	r := &req{
		DefaultLeft:  "  keep me  ",
		DefaultRight: "  keep me  ",
		CutsetLeft:   "///path",
		CutsetRight:  "path///",
		CutsetBoth:   "///path///",
	}
	if err := ApplyTransforms(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.DefaultLeft != "keep me  " {
		t.Errorf("DefaultLeft = %q", r.DefaultLeft)
	}
	if r.DefaultRight != "  keep me" {
		t.Errorf("DefaultRight = %q", r.DefaultRight)
	}
	if r.CutsetLeft != "path" {
		t.Errorf("CutsetLeft = %q", r.CutsetLeft)
	}
	if r.CutsetRight != "path" {
		t.Errorf("CutsetRight = %q", r.CutsetRight)
	}
	if r.CutsetBoth != "path" {
		t.Errorf("CutsetBoth = %q", r.CutsetBoth)
	}
}

func TestApplyTransforms_StringPointer(t *testing.T) {
	type req struct {
		Name *string `transform:"trim,lowercase"`
		Nil  *string `transform:"trim"`
	}
	val := "  HELLO  "
	r := &req{Name: &val}
	if err := ApplyTransforms(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *r.Name != "hello" {
		t.Errorf("Name = %q", *r.Name)
	}
	if r.Nil != nil {
		t.Errorf("Nil should remain nil, got %v", r.Nil)
	}
}

func TestApplyTransforms_StringSliceRemoveEmpty(t *testing.T) {
	type req struct {
		Tags    []string `transform:"trim,removeempty"`
		Upper   []string `transform:"uppercase"`
		Empty   []string `transform:"removeempty"`
		NilTags []string `transform:"trim,removeempty"`
	}
	r := &req{
		Tags:  []string{"  go ", "", "  rust  ", "   "},
		Upper: []string{"a", "b"},
		Empty: []string{"", "x", ""},
	}
	if err := ApplyTransforms(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := r.Tags, []string{"go", "rust"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
	if got, want := r.Upper, []string{"A", "B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Upper = %v, want %v", got, want)
	}
	if got, want := r.Empty, []string{"x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Empty = %v, want %v", got, want)
	}
	if r.NilTags != nil {
		t.Errorf("NilTags should stay nil, got %v", r.NilTags)
	}
}

func TestApplyTransforms_NestedStruct(t *testing.T) {
	type Inner struct {
		Name string `transform:"trim,lowercase"`
	}
	type Outer struct {
		Title string `transform:"trim"`
		Inner Inner
		Ptr   *Inner
	}
	r := &Outer{
		Title: "  T  ",
		Inner: Inner{Name: "  ALICE  "},
		Ptr:   &Inner{Name: "  BOB  "},
	}
	if err := ApplyTransforms(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Title != "T" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.Inner.Name != "alice" {
		t.Errorf("Inner.Name = %q", r.Inner.Name)
	}
	if r.Ptr.Name != "bob" {
		t.Errorf("Ptr.Name = %q", r.Ptr.Name)
	}
}

func TestApplyTransforms_SliceOfStructs(t *testing.T) {
	type Item struct {
		Label string `transform:"trim,uppercase"`
	}
	type req struct {
		Items []Item
	}
	r := &req{Items: []Item{{Label: "  a  "}, {Label: " b "}}}
	if err := ApplyTransforms(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Items[0].Label != "A" || r.Items[1].Label != "B" {
		t.Errorf("Items = %+v", r.Items)
	}
}

func TestApplyTransforms_UnknownOp(t *testing.T) {
	type req struct {
		X string `transform:"trim,frobnicate"`
	}
	err := ApplyTransforms(&req{X: " hi "})
	if err == nil {
		t.Fatal("expected error for unknown op")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error should mention unknown op name: %v", err)
	}
}

func TestApplyTransforms_RemoveEmptyOnNonSlice(t *testing.T) {
	type req struct {
		X string `transform:"removeempty"`
	}
	err := ApplyTransforms(&req{X: "hi"})
	if err == nil {
		t.Fatal("expected error for removeempty on string")
	}
}

func TestApplyTransforms_NonStructInput(t *testing.T) {
	// Non-struct inputs are a no-op (matches validator leniency).
	if err := ApplyTransforms("just a string"); err != nil {
		t.Errorf("unexpected error on non-struct: %v", err)
	}
	if err := ApplyTransforms(nil); err != nil {
		t.Errorf("unexpected error on nil: %v", err)
	}
}

// --- Static tag validation: caught at registration time ---

func TestValidateTransformTags_UnknownOp(t *testing.T) {
	type req struct {
		Name string `transform:"trim,frobnicate"`
	}
	err := ValidateTransformTags(reflect.TypeOf(req{}))
	if err == nil {
		t.Fatal("expected error for unknown op")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error should mention op name: %v", err)
	}
}

func TestValidateTransformTags_WrongFieldType(t *testing.T) {
	type req struct {
		Age int `transform:"trim"`
	}
	err := ValidateTransformTags(reflect.TypeOf(req{}))
	if err == nil {
		t.Fatal("expected error for transform on int")
	}
	if !strings.Contains(err.Error(), "Age") {
		t.Errorf("error should mention field name: %v", err)
	}
}

func TestValidateTransformTags_RemoveEmptyOnNonSlice(t *testing.T) {
	type req struct {
		Name string `transform:"removeempty"`
	}
	err := ValidateTransformTags(reflect.TypeOf(req{}))
	if err == nil {
		t.Fatal("expected error for removeempty on string")
	}
}

func TestValidateTransformTags_NestedStruct(t *testing.T) {
	type inner struct {
		Age int `transform:"trim"`
	}
	type outer struct {
		Inner inner
	}
	err := ValidateTransformTags(reflect.TypeOf(outer{}))
	if err == nil {
		t.Fatal("expected error from nested struct")
	}
}

func TestValidateTransformTags_ValidTags(t *testing.T) {
	type inner struct {
		Label string `transform:"trim,uppercase"`
	}
	type req struct {
		Email string   `transform:"trim,lowercase"`
		Name  *string  `transform:"trim"`
		Tags  []string `transform:"trim,removeempty"`
		Items []inner
	}
	if err := ValidateTransformTags(reflect.TypeOf(req{})); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

type BadTransformHandlers struct{}

type BadReq struct {
	Age int `transform:"trim"`
}

func (h *BadTransformHandlers) Submit(ctx context.Context, req *BadReq) error { return nil }

func TestRegister_PanicsOnBadTransformTag(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on registration with invalid transform tag")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "transform") || !strings.Contains(msg, "Age") {
			t.Errorf("panic message should mention transform and field name: %q", msg)
		}
	}()
	registry := NewRegistry()
	registry.Register(&BadTransformHandlers{})
}

// --- Integration test: transforms run before validation in buildArgs() ---

type TransformValidated struct {
	Name string `json:"name" transform:"trim" validate:"required,min=1"`
}

type TransformHandlers struct{}

func (h *TransformHandlers) Submit(ctx context.Context, req *TransformValidated) error {
	if req.Name != strings.TrimSpace(req.Name) {
		return errors.New("handler saw untrimmed value")
	}
	return nil
}

func TestTransform_RunsBeforeValidation(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&TransformHandlers{})
	registry.SetValidator(NewPlaygroundValidator())

	info, ok := registry.Get("TransformHandlers.Submit")
	if !ok {
		t.Fatal("handler not found")
	}

	// Whitespace-only input should trim to empty, then fail validation.
	_, err := info.Call(context.Background(), []byte(`[{"name":"   "}]`))
	if err == nil {
		t.Fatal("expected validation error after trim, got nil")
	}
	var perr *ProtocolError
	if !errors.As(err, &perr) {
		t.Fatalf("expected *ProtocolError, got %T: %v", err, err)
	}
	if perr.Code != CodeValidationFailed {
		t.Errorf("expected code %d, got %d", CodeValidationFailed, perr.Code)
	}

	// Padded valid input should trim and pass.
	if _, err := info.Call(context.Background(), []byte(`[{"name":"  alice  "}]`)); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}
