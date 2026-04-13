package aprot

import (
	"context"
	"iter"
	"reflect"
	"testing"
)

func TestIsIterSeq(t *testing.T) {
	type User struct{ Name string }

	tests := []struct {
		name    string
		t       reflect.Type
		wantOK  bool
		wantElm reflect.Type
	}{
		{
			name:    "iter.Seq[int]",
			t:       reflect.TypeOf((iter.Seq[int])(nil)),
			wantOK:  true,
			wantElm: reflect.TypeOf(int(0)),
		},
		{
			name:    "iter.Seq[*User]",
			t:       reflect.TypeOf((iter.Seq[*User])(nil)),
			wantOK:  true,
			wantElm: reflect.TypeOf((*User)(nil)),
		},
		{
			name:    "anonymous func(func(int) bool)",
			t:       reflect.TypeOf((func(func(int) bool))(nil)),
			wantOK:  true,
			wantElm: reflect.TypeOf(int(0)),
		},
		{
			name:   "iter.Seq2[string, int] is not Seq",
			t:      reflect.TypeOf((iter.Seq2[string, int])(nil)),
			wantOK: false,
		},
		{
			name:   "plain func() bool",
			t:      reflect.TypeOf((func() bool)(nil)),
			wantOK: false,
		},
		{
			name:   "func(func(int))",
			t:      reflect.TypeOf((func(func(int)))(nil)),
			wantOK: false,
		},
		{
			name:   "func(func(int) int) - wrong yield return",
			t:      reflect.TypeOf((func(func(int) int))(nil)),
			wantOK: false,
		},
		{
			name:   "variadic func(...func(int) bool)",
			t:      reflect.TypeOf((func(...func(int) bool))(nil)),
			wantOK: false,
		},
		{
			name:   "int",
			t:      reflect.TypeOf(0),
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			elem, ok := isIterSeq(tc.t)
			if ok != tc.wantOK {
				t.Fatalf("isIterSeq ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && elem != tc.wantElm {
				t.Fatalf("isIterSeq elem = %v, want %v", elem, tc.wantElm)
			}
		})
	}
}

func TestIsIterSeq2(t *testing.T) {
	type User struct{ Name string }

	tests := []struct {
		name    string
		t       reflect.Type
		wantOK  bool
		wantKey reflect.Type
		wantVal reflect.Type
	}{
		{
			name:    "iter.Seq2[string, int]",
			t:       reflect.TypeOf((iter.Seq2[string, int])(nil)),
			wantOK:  true,
			wantKey: reflect.TypeOf(""),
			wantVal: reflect.TypeOf(int(0)),
		},
		{
			name:    "iter.Seq2[int, *User]",
			t:       reflect.TypeOf((iter.Seq2[int, *User])(nil)),
			wantOK:  true,
			wantKey: reflect.TypeOf(int(0)),
			wantVal: reflect.TypeOf((*User)(nil)),
		},
		{
			name:    "anonymous func(func(string, int) bool)",
			t:       reflect.TypeOf((func(func(string, int) bool))(nil)),
			wantOK:  true,
			wantKey: reflect.TypeOf(""),
			wantVal: reflect.TypeOf(int(0)),
		},
		{
			name:   "iter.Seq[int] is not Seq2",
			t:      reflect.TypeOf((iter.Seq[int])(nil)),
			wantOK: false,
		},
		{
			name:   "func(func(int, int))",
			t:      reflect.TypeOf((func(func(int, int)))(nil)),
			wantOK: false,
		},
		{
			name:   "func(func(int, int) int)",
			t:      reflect.TypeOf((func(func(int, int) int))(nil)),
			wantOK: false,
		},
		{
			name:   "int",
			t:      reflect.TypeOf(0),
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, val, ok := isIterSeq2(tc.t)
			if ok != tc.wantOK {
				t.Fatalf("isIterSeq2 ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				if key != tc.wantKey {
					t.Fatalf("isIterSeq2 key = %v, want %v", key, tc.wantKey)
				}
				if val != tc.wantVal {
					t.Fatalf("isIterSeq2 val = %v, want %v", val, tc.wantVal)
				}
			}
		})
	}
}

// Sentinel types for validateMethod tests
type streamTestUser struct {
	Name string `json:"name"`
}

type streamTestHandlers struct{}

func (streamTestHandlers) StreamInts(ctx context.Context) (iter.Seq[int], error) {
	return nil, nil
}

func (streamTestHandlers) StreamUsers(ctx context.Context, limit int) (iter.Seq[*streamTestUser], error) {
	return nil, nil
}

func (streamTestHandlers) StreamPairs(ctx context.Context) (iter.Seq2[string, streamTestUser], error) {
	return nil, nil
}

func (streamTestHandlers) Unary(ctx context.Context) (*streamTestUser, error) {
	return nil, nil
}

func TestValidateMethod_StreamKind(t *testing.T) {
	hv := reflect.ValueOf(streamTestHandlers{})
	ht := hv.Type()

	byName := map[string]*HandlerInfo{}
	for i := 0; i < ht.NumMethod(); i++ {
		m := ht.Method(i)
		info := validateMethod(m, hv, "streamTestHandlers")
		if info == nil {
			t.Fatalf("validateMethod(%s) returned nil, want HandlerInfo", m.Name)
		}
		byName[m.Name] = info
	}

	if got := byName["StreamInts"].Kind; got != HandlerKindStream {
		t.Errorf("StreamInts Kind = %v, want HandlerKindStream", got)
	}
	if got := byName["StreamInts"].ResponseType; got != reflect.TypeOf(int(0)) {
		t.Errorf("StreamInts ResponseType = %v, want int", got)
	}

	if got := byName["StreamUsers"].Kind; got != HandlerKindStream {
		t.Errorf("StreamUsers Kind = %v, want HandlerKindStream", got)
	}
	// *streamTestUser should be unwrapped to the struct type like unary (*T, error) is.
	if got := byName["StreamUsers"].ResponseType; got != reflect.TypeOf(streamTestUser{}) {
		t.Errorf("StreamUsers ResponseType = %v, want streamTestUser (unwrapped)", got)
	}

	if got := byName["StreamPairs"].Kind; got != HandlerKindStream2 {
		t.Errorf("StreamPairs Kind = %v, want HandlerKindStream2", got)
	}
	if got := byName["StreamPairs"].ResponseType; got != reflect.TypeOf(streamTestUser{}) {
		t.Errorf("StreamPairs ResponseType = %v, want streamTestUser", got)
	}
	if got := byName["StreamPairs"].StreamKeyType; got != reflect.TypeOf("") {
		t.Errorf("StreamPairs StreamKeyType = %v, want string", got)
	}

	if got := byName["Unary"].Kind; got != HandlerKindUnary {
		t.Errorf("Unary Kind = %v, want HandlerKindUnary", got)
	}
}
