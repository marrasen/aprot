package aprot

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marrasen/aprot/client"
	"github.com/marrasen/aprot/internal/gentestpkg"
	"github.com/marrasen/aprot/internal/gogentest/other"
)

// --- Fixture types for the type-mapping tests -----------------------------

type ggElem struct {
	V int `json:"v"`
}

// Custom marshalers with every wire shape the zero-value probe recognizes.
type ggMoney struct{}

func (ggMoney) MarshalJSON() ([]byte, error) { return []byte(`"0.00"`), nil }

type ggFloat struct{}

func (ggFloat) MarshalJSON() ([]byte, error) { return []byte(`1.5`), nil }

type ggInt int

func (ggInt) MarshalJSON() ([]byte, error) { return []byte(`3`), nil }

type ggBool struct{}

func (ggBool) MarshalJSON() ([]byte, error) { return []byte(`true`), nil }

type ggObj struct{}

func (ggObj) MarshalJSON() ([]byte, error) { return []byte(`{"a":1}`), nil }

type ggNull struct{}

func (ggNull) MarshalJSON() ([]byte, error) { return []byte(`null`), nil }

type ggList []ggElem

func (l ggList) MarshalJSON() ([]byte, error) { return []byte(`[]`), nil }

type ggMap map[string]int

func (m ggMap) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type ggText struct{ s string }

func (t ggText) MarshalText() ([]byte, error) { return []byte(t.s), nil }

type ggInlined struct {
	A int `json:"a"`
}

type ggNamedEmbed struct {
	B int `json:"b"`
}

type ggPtrEmbed struct {
	C int `json:"c"`
}

type ggStatus string

type ggLevel int8

func (l ggLevel) String() string { return [...]string{"Low", "High"}[l] }

type ggBytes []byte

type ggAll struct {
	ggInlined
	ggNamedEmbed `json:"named"`
	*ggPtrEmbed
	sync.Mutex

	Time     time.Time               `json:"time"`
	Dur      time.Duration           `json:"dur"`
	DurUnits time.Duration           `json:"durUnits,format:units"`
	Raw      json.RawMessage         `json:"raw,omitempty"`
	Value    jsontext.Value          `json:"value"`
	NS       sql.NullString          `json:"ns"`
	NI32     sql.NullInt32           `json:"ni32"`
	NI16     sql.NullInt16           `json:"ni16"`
	NB       sql.NullBool            `json:"nb"`
	NF       sql.NullFloat64         `json:"nf"`
	NT       sql.NullTime            `json:"nt"`
	NBy      sql.NullByte            `json:"nby"`
	GS       sql.Null[string]        `json:"gs"`
	GX       sql.Null[ggElem]        `json:"gx"`
	PNS      *sql.NullString         `json:"pns"`
	Blob     Blob                    `json:"blob"`
	Money    ggMoney                 `json:"money"`
	Float    ggFloat                 `json:"float"`
	Int      ggInt                   `json:"int"`
	Bool     ggBool                  `json:"bool"`
	Obj      ggObj                   `json:"obj"`
	Null     ggNull                  `json:"null"`
	List     ggList                  `json:"list"`
	Map      ggMap                   `json:"map"`
	Text     ggText                  `json:"text"`
	Any      any                     `json:"any"`
	Stringer fmt.Stringer            `json:"stringer"`
	Nested   map[string][]*[3]ggElem `json:"nested"`
	Status   ggStatus                `json:"status"`
	Level    ggLevel                 `json:"level"`
	ByStatus map[ggStatus]ggLevel    `json:"byStatus"`
	Bytes    []byte                  `json:"bytes,format:hex"`
	Named    ggBytes                 `json:"namedBytes"`
	Omit     *int                    `json:"omit,omitzero"`
	Str      int64                   `json:"str,string"`
	Extra    map[string]any          `json:",inline"`
	Anon     struct{ X, Y int }      `json:"anon"`
	Color    gentestpkg.Color        `json:"color"`
	Self     *ggAll                  `json:"self,omitempty"`
	Fn       func()                  `json:"-"`
	Untagged string
	hidden   int // nolint:unused // unexported fields are skipped
}

type ggHandlers struct{}

// Get returns everything.
//
// It has a second paragraph.
func (h *ggHandlers) Get(_ context.Context, id int, filter string) (*ggAll, error) { return nil, nil }

func (h *ggHandlers) Values(_ context.Context, items []ggElem) ([]ggElem, error) { return nil, nil }

func (h *ggHandlers) Remove(_ context.Context, id int) error { return nil }

func (h *ggHandlers) Rows(_ context.Context) (iter.Seq[ggElem], error) { return nil, nil }

func (h *ggHandlers) Keyed(_ context.Context) (iter.Seq2[ggStatus, *ggElem], error) {
	return nil, nil
}

func (h *ggHandlers) File(_ context.Context) (Blob, error) { return Blob{}, nil }

func (h *ggHandlers) Spread(_ context.Context, ctx string, nums ...int) (int, error) {
	return 0, nil
}

type ggChanged struct {
	ID int `json:"id"`
}

func newGoGenRegistry() *Registry {
	r := NewRegistry()
	h := &ggHandlers{}
	r.Register(h)
	r.RegisterEnumFor(h, []ggStatus{"open", "in-review"})
	r.RegisterEnum([]ggLevel{0, 1})
	r.RegisterPushEventFor(h, ggChanged{})
	r.RegisterError(fmt.Errorf("eof"), "EndOfFile")
	r.OverrideFieldType(ggAll{}, "Any", ggElem{})
	return r
}

func generateGo(t *testing.T, r *Registry, opts GoGeneratorOptions) string {
	t.Helper()
	files, err := NewGoGenerator(r).WithOptions(opts).Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return files[goClientFileName]
}

// fieldLine finds the declaration of a field in the generated source,
// collapsing gofmt's alignment spaces.
func fieldLine(t *testing.T, src, field string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(field) + `\s.*$`)
	m := re.FindString(src)
	if m == "" {
		t.Fatalf("field %s not found in:\n%s", field, src)
	}
	return strings.Join(strings.Fields(m), " ")
}

func TestGoGenerateTypeMapping(t *testing.T) {
	src := generateGo(t, newGoGenRegistry(), GoGeneratorOptions{
		PackageName: "api",
		ImportTypes: []string{"github.com/marrasen/aprot/internal/gentestpkg"},
	})

	fields := map[string]string{
		// Standard library types are used directly.
		"Time":     "Time time.Time `json:\"time\"`",
		"Dur":      "Dur time.Duration `json:\"dur\"`",
		"DurUnits": "DurUnits time.Duration `json:\"durUnits,format:units\"`",
		"Raw":      "Raw jsontext.Value `json:\"raw,omitempty\"`",
		"Value":    "Value jsontext.Value `json:\"value\"`",
		// sql.Null* become pointers to their value type.
		"NS":   "NS *string `json:\"ns\"`",
		"NI32": "NI32 *int32 `json:\"ni32\"`",
		"NI16": "NI16 *int16 `json:\"ni16\"`",
		"NB":   "NB *bool `json:\"nb\"`",
		"NF":   "NF *float64 `json:\"nf\"`",
		"NT":   "NT *time.Time `json:\"nt\"`",
		"NBy":  "NBy *byte `json:\"nby\"`",
		"GS":   "GS *string `json:\"gs\"`",
		"PNS":  "PNS *string `json:\"pns\"`",
		// aprot.Blob becomes client.Blob.
		"Blob": "Blob client.Blob `json:\"blob\"`",
		// Custom marshalers: a string or bool shape is kept, anything else
		// is raw JSON.
		"Money": "Money string `json:\"money\"`",
		"Float": "Float jsontext.Value `json:\"float\"`",
		"Int":   "Int jsontext.Value `json:\"int\"`",
		"Bool":  "Bool bool `json:\"bool\"`",
		"Obj":   "Obj jsontext.Value `json:\"obj\"`",
		"Null":  "Null jsontext.Value `json:\"null\"`",
		"List":  "List jsontext.Value `json:\"list\"`",
		"Map":   "Map jsontext.Value `json:\"map\"`",
		"Text":  "Text string `json:\"text\"`",
		// Interfaces are any; an override replaces the type, as a pointer
		// because the interface can be nil.
		"Any":      "Any *GgElem `json:\"any\"`",
		"Stringer": "Stringer any `json:\"stringer\"`",
		// Structure is preserved.
		"Nested": "Nested map[string][]*[3]GgElem `json:\"nested\"`",
		// Enums.
		"Status":   "Status GgStatus `json:\"status\"`",
		"Level":    "Level GgLevel `json:\"level\"`",
		"ByStatus": "ByStatus map[GgStatus]GgLevel `json:\"byStatus\"`",
		// Tags are copied verbatim.
		"Bytes": "Bytes []byte `json:\"bytes,format:hex\"`",
		"Named": "Named GgBytes `json:\"namedBytes\"`",
		"Omit":  "Omit *int `json:\"omit,omitzero\"`",
		"Str":   "Str int64 `json:\"str,string\"`",
		"Extra": "Extra map[string]any `json:\",inline\"`",
		// ImportTypes are referenced directly.
		"Color": "Color gentestpkg.Color `json:\"color\"`",
		// Recursion.
		"Self": "Self *GgAll `json:\"self,omitempty\"`",
		// No tag stays no tag.
		"Untagged": "Untagged string",
		// Embedded struct with a JSON name: a named field, even though its
		// type is unexported.
		"GgNamedEmbed": "GgNamedEmbed GgNamedEmbed `json:\"named\"`",
	}
	for name, want := range fields {
		if got := fieldLine(t, src, name); got != want {
			t.Errorf("field %s:\n got: %s\nwant: %s", name, got, want)
		}
	}

	for _, want := range []string{
		// Embedded structs without a JSON name stay embedded (inlined).
		"\tGgInlined\n",
		"\t*GgPtrEmbed\n",
		// Generic sql.Null of a type the server does not flatten keeps the
		// default struct shape.
		"V     GgElem",
		// Anonymous structs are reproduced inline.
		"Anon     struct {\n\t\tX int\n\t\tY int\n\t} `json:\"anon\"`",
		// Enums.
		"type GgStatus string",
		"GgStatusOpen     GgStatus = \"open\"",
		"GgStatusInReview GgStatus = \"in-review\"",
		"type GgLevel int8",
		"GgLevelLow  GgLevel = 0",
		// Named byte slices keep their name (json/v2 encodes them as arrays).
		"type GgBytes []byte",
		// Error codes.
		"ErrCodeEndOfFile = 1000",
		// Methods.
		"func (h GgHandlersClient) Get(ctx context.Context, id int, filter string) (*GgAll, error) {",
		`return client.Call[*GgAll](ctx, h.c, "ggHandlers.Get", []any{id, filter})`,
		"func (h GgHandlersClient) SubscribeGet(ctx context.Context, id int, filter string, opts ...client.SubscribeOption[*GgAll]) *client.Subscription[*GgAll] {",
		"func (h GgHandlersClient) Remove(ctx context.Context, id int) error {",
		`_, err := client.Call[struct{}](ctx, h.c, "ggHandlers.Remove", []any{id})`,
		"func (h GgHandlersClient) Rows(ctx context.Context) *client.StreamResult[GgElem] {",
		"func (h GgHandlersClient) Keyed(ctx context.Context) *client.Stream2Result[GgStatus, *GgElem] {",
		"func (h GgHandlersClient) File(ctx context.Context) (client.Blob, error) {",
		// A parameter named like a generated identifier is renamed, and a
		// variadic parameter is spread.
		"func (h GgHandlersClient) Spread(ctx context.Context, ctx_ string, nums ...int) (int, error) {",
		"func (h GgHandlersClient) SubscribeSpread(ctx context.Context, ctx_ string, nums []int, opts ...client.SubscribeOption[int])",
		// Push events.
		"func (c *Client) OnGgChanged(fn func(GgChanged)) (remove func()) {",
		`return client.OnPush(c.Client, "ggChanged", fn)`,
		// Group fields.
		"GgHandlers GgHandlersClient",
		// Handler docs are copied.
		"// Get returns everything.\n//\n// It has a second paragraph.\nfunc (h GgHandlersClient) Get(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}

	for _, unwanted := range []string{
		"SubscribeRemove", "SubscribeRows", "SubscribeKeyed", // no Subscribe for void or streams
		"hidden", "Fn ", "Mutex", "type GgMoney", "type GgText",
		"\"github.com/marrasen/aprot\"", "sql.", "GgList",
	} {
		if strings.Contains(src, unwanted) {
			t.Errorf("generated source should not contain %q", unwanted)
		}
	}

	if !strings.HasPrefix(src, generatedFileMarker+"\n") {
		t.Error("generated source must start with the generated-code marker")
	}
}

func TestGoGenerateDeterministic(t *testing.T) {
	opts := GoGeneratorOptions{PackageName: "api", ImportTypes: []string{"github.com/marrasen/aprot/internal/gentestpkg"}}
	first := generateGo(t, newGoGenRegistry(), opts)
	for range 5 {
		if got := generateGo(t, newGoGenRegistry(), opts); got != first {
			t.Fatal("output differs between runs")
		}
	}
	// Reusing one generator gives the same output too.
	g := NewGoGenerator(newGoGenRegistry()).WithOptions(opts)
	a, err := g.Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if a[goClientFileName] != b[goClientFileName] || a[goClientFileName] != first {
		t.Fatal("output differs when the generator is reused")
	}
}

// Two types named Item from different packages are both prefixed with their
// package name.
type ggCollideHandlers struct{}

type Item struct {
	Name string `json:"name"`
}

func (h *ggCollideHandlers) Local(_ context.Context) (*Item, error) { return nil, nil }

func (h *ggCollideHandlers) Shared(_ context.Context) (*other.Item, error) { return nil, nil }

// Client collides with the generated Client type.
type Client struct {
	ID int `json:"id"`
}

func (h *ggCollideHandlers) Owner(_ context.Context) (*Client, error) { return nil, nil }

func TestGoGenerateNameCollisions(t *testing.T) {
	r := NewRegistry()
	r.Register(&ggCollideHandlers{})
	src := generateGo(t, r, GoGeneratorOptions{PackageName: "api"})
	for _, want := range []string{
		"type AprotItem struct",
		"type OtherItem struct",
		"(*AprotItem, error)",
		"(*OtherItem, error)",
		"type AprotClient struct",
		"(*AprotClient, error)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
}

// A group whose field would shadow a method promoted from *client.Client is
// an error.
type refreshAuth struct{}

func (h *refreshAuth) Ping(_ context.Context) error { return nil }

func TestGoGenerateGroupCollidesWithClientMethod(t *testing.T) {
	r := NewRegistry()
	r.Register(&refreshAuth{})
	_, err := NewGoGenerator(r).WithOptions(GoGeneratorOptions{PackageName: "api"}).Generate()
	if err == nil || !strings.Contains(err.Error(), "Client.RefreshAuth") {
		t.Fatalf("want an error naming Client.RefreshAuth, got %v", err)
	}
}

// Subscribe<Name> of one handler colliding with another handler's name is an
// error.
type ggSubCollide struct{}

func (h *ggSubCollide) List(_ context.Context) (int, error)          { return 0, nil }
func (h *ggSubCollide) SubscribeList(_ context.Context) (int, error) { return 0, nil }

func TestGoGenerateMethodCollision(t *testing.T) {
	r := NewRegistry()
	r.Register(&ggSubCollide{})
	_, err := NewGoGenerator(r).WithOptions(GoGeneratorOptions{PackageName: "api"}).Generate()
	if err == nil || !strings.Contains(err.Error(), "SubscribeList") {
		t.Fatalf("want a collision error for SubscribeList, got %v", err)
	}
}

// The promoted-member list must match the runtime client's exported methods.
func TestGoPromotedClientMembersMatchRuntime(t *testing.T) {
	var got []string
	ct := reflect.TypeFor[*client.Client]()
	for i := range ct.NumMethod() {
		got = append(got, ct.Method(i).Name)
	}
	got = append(got, "Client")
	slices.Sort(got)
	want := slices.Clone(goPromotedClientMembers)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("goPromotedClientMembers = %v, runtime *client.Client has %v", want, got)
	}
}

func TestGoGenerateRejectsImportingAprot(t *testing.T) {
	_, err := NewGoGenerator(newGoGenRegistry()).WithOptions(GoGeneratorOptions{
		PackageName: "api",
		ImportTypes: []string{aprotPkgPath},
	}).Generate()
	if err == nil {
		t.Fatal("want an error for ImportTypes listing the aprot package")
	}
}

func TestGoGenerateWritesAndRemovesStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myapi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "old.gen.go")
	handWritten := filepath.Join(dir, "extra.go")
	if err := os.WriteFile(stale, []byte(generatedFileMarker+"\n\npackage myapi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handWritten, []byte("package myapi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGoGenerator(newGoGenRegistry()).WithOptions(GoGeneratorOptions{OutputDir: dir}).Generate(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, goClientFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "\npackage myapi\n") {
		t.Error("package name should default to the output directory's name")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale generated file was not removed")
	}
	if _, err := os.Stat(handWritten); err != nil {
		t.Error("hand-written file was removed")
	}
}

// TestGoGeneratedClientCompiles builds the generated client for the broad
// registry above with the go tool. It writes into a dot-directory inside the
// module, so the import of github.com/marrasen/aprot/client resolves while
// ./... patterns ignore the directory.
func TestGoGeneratedClientCompiles(t *testing.T) {
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goTool); err != nil {
		if goTool, err = exec.LookPath("go"); err != nil {
			t.Skip("go tool not found")
		}
	}
	dir, err := os.MkdirTemp(".", ".gogencompile")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := NewGoGenerator(newGoGenRegistry()).WithOptions(GoGeneratorOptions{
		OutputDir:   dir,
		PackageName: "api",
		ImportTypes: []string{"github.com/marrasen/aprot/internal/gentestpkg"},
	}).Generate(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(goTool, "vet", "./"+filepath.ToSlash(dir)).CombinedOutput() // #nosec G204 -- test runs the go tool on its own output
	if err != nil {
		t.Fatalf("go vet of generated client failed: %v\n%s", err, out)
	}
}
