package aprot

// Regression tests for the Go client generator, from an adversarial review.
//
// The wire tests generate a client for a probe registry into a dot-directory
// inside the module, write a _test.go file next to it that decodes the
// server's JSON into the generated result type of each probe method (using
// the runtime client's wire options) and re-encodes it, then compare that
// with the server's encoding. They also send the client encoding back
// through the server's decoder and check that the server value survives.

import (
	"context"
	"database/sql"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marrasen/aprot/internal/gogentest/other"
)

// --- Probe types ------------------------------------------------------------

type rvElem struct {
	V int `json:"v"`
}

// rvPrio is an int enum that marshals as its name, a very common pattern.
type rvPrio int

const (
	rvPrioLow rvPrio = iota
	rvPrioHigh
)

func (p rvPrio) String() string { return [...]string{"Low", "High"}[p] }

func (p rvPrio) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

func (p *rvPrio) UnmarshalText(b []byte) error {
	switch string(b) {
	case "Low":
		*p = rvPrioLow
	case "High":
		*p = rvPrioHigh
	default:
		return fmt.Errorf("bad prio %q", b)
	}
	return nil
}

// rvScore marshals as a JSON number through its own MarshalJSON.
type rvScore int

func (s rvScore) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(s))), nil }

// UnmarshalJSON pairs with MarshalJSON. Without it the server's decoder
// would honor the `,string` option on rvWithEnumText.Score that its encoder
// ignores, and the server could not read its own output.
func (s *rvScore) UnmarshalJSON(b []byte) error {
	n, err := strconv.Atoi(string(b))
	*s = rvScore(n)
	return err
}

// rvCents is an integer kind that marshals as a decimal number.
type rvCents int64

func (c rvCents) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d.%02d", c/100, c%100)), nil
}

func (c *rvCents) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(string(b), 64)
	*c = rvCents(f*100 + 0.5)
	return err
}

// rvSnowflake is a struct that marshals as a JSON integer (an ID type).
type rvSnowflake struct{ v uint64 }

func (s rvSnowflake) MarshalJSON() ([]byte, error) { return []byte(strconv.FormatUint(s.v, 10)), nil }

func (s *rvSnowflake) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseUint(string(b), 10, 64)
	s.v = v
	return err
}

// rvIDs is a slice of int64 that marshals its elements as strings, the usual
// way to keep 64-bit IDs exact for JavaScript.
type rvIDs []int64

func (ids rvIDs) MarshalJSON() ([]byte, error) {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return json.Marshal(s)
}

func (ids *rvIDs) UnmarshalJSON(b []byte) error {
	var s []string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*ids = nil
	for _, x := range s {
		v, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return err
		}
		*ids = append(*ids, v)
	}
	return nil
}

// rvAppender implements only encoding.TextAppender, which json/v2 honors.
type rvAppender struct{ s string }

func (a rvAppender) AppendText(b []byte) ([]byte, error) { return append(b, a.s...), nil }

func (a *rvAppender) UnmarshalText(b []byte) error { a.s = string(b); return nil }

type rvWithEnumText struct {
	P     rvPrio         `json:"p"`
	ByP   map[rvPrio]int `json:"byP"`
	Score rvScore        `json:"score,string"`
}

type rvPtrNull struct {
	X *sql.Null[rvElem] `json:"x"`
}

type rvEmbedsNull struct {
	sql.NullString
	A int `json:"a"`
}

type rvEmbedsBlob struct {
	Blob
	Name string `json:"name"`
}

type rvStringNull struct {
	N sql.NullInt64 `json:"n,string"` //nolint:staticcheck // SA5008: the misplaced option is the point of the test
}

// rvFormatNull has a format tag on a flattened sql.NullTime.
type rvFormatNull struct {
	T sql.NullTime `json:"t,format:unix"`
}

type rvNumbers struct {
	Cents rvCents     `json:"cents"`
	ID    rvSnowflake `json:"id"`
	IDs   rvIDs       `json:"ids"`
	App   rvAppender  `json:"app"`
}

// Depth and conflict rules for embedded fields.
type rvDeep struct {
	A int `json:"a"`
	B int `json:"b"`
}
type rvSame1 struct {
	X int
}
type rvSame2 struct {
	X int
}
type rvEmbedRules struct {
	rvDeep
	rvSame1
	rvSame2
	A    int    `json:"a"`
	Case string `json:"case"`
}

type rvStd struct {
	U   url.URL       `json:"u"`
	PU  *url.URL      `json:"pu"`
	IP  net.IP        `json:"ip"`
	B   *big.Int      `json:"b"`
	D   time.Duration `json:"d,format:sec"`
	PD  *time.Duration
	NT  sql.Null[time.Time] `json:"nt"`
	Arr [4]byte             `json:"arr"`
	// J is a real jsontext.Value field.
	J jsontext.Value `json:"j"`
}

type rvHandlers struct{}

func (h *rvHandlers) EnumText(_ context.Context) (rvWithEnumText, error) {
	return rvWithEnumText{}, nil
}
func (h *rvHandlers) PtrNull(_ context.Context) (rvPtrNull, error)       { return rvPtrNull{}, nil }
func (h *rvHandlers) EmbedsNull(_ context.Context) (rvEmbedsNull, error) { return rvEmbedsNull{}, nil }
func (h *rvHandlers) EmbedsBlob(_ context.Context) (rvEmbedsBlob, error) { return rvEmbedsBlob{}, nil }
func (h *rvHandlers) StringNull(_ context.Context) (rvStringNull, error) { return rvStringNull{}, nil }
func (h *rvHandlers) FormatNull(_ context.Context) (rvFormatNull, error) { return rvFormatNull{}, nil }
func (h *rvHandlers) Numbers(_ context.Context) (rvNumbers, error)       { return rvNumbers{}, nil }
func (h *rvHandlers) EmbedRules(_ context.Context) (rvEmbedRules, error) { return rvEmbedRules{}, nil }
func (h *rvHandlers) Std(_ context.Context) (rvStd, error)               { return rvStd{}, nil }

func newReviewRegistry() *Registry {
	r := NewRegistry()
	h := &rvHandlers{}
	r.Register(h)
	r.RegisterEnumFor(h, []rvPrio{rvPrioLow, rvPrioHigh})
	return r
}

// --- Harness ----------------------------------------------------------------

type rvCase struct {
	method string
	value  any // server value; must be the handler's result type
}

type rvResult struct {
	DecodeErr string            `json:"decodeErr"`
	JSON      jsonv1.RawMessage `json:"json"`
}

const rvDumpTest = `package api

import (
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"os"
	"reflect"
	"testing"

	experimentjson "github.com/go-json-experiment/json"
)

var opts = json.JoinOptions(
	jsonv1.FormatDurationAsNano(true),
	experimentjson.ExperimentalSupportFormatTag(true),
)

func TestDump(t *testing.T) {
	in, err := os.ReadFile("cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string]jsonv1.RawMessage
	if err := json.Unmarshal(in, &cases); err != nil {
		t.Fatal(err)
	}
	group := reflect.TypeFor[GROUP]()
	out := map[string]map[string]any{}
	for name, raw := range cases {
		m, ok := group.MethodByName(name)
		if !ok {
			t.Fatalf("no method %s", name)
		}
		v := reflect.New(m.Type.Out(0))
		res := map[string]any{}
		if err := json.Unmarshal(raw, v.Interface(), opts); err != nil {
			res["decodeErr"] = err.Error()
		}
		enc, err := json.Marshal(v.Interface(), opts)
		if err != nil {
			res["decodeErr"] = "encode: " + err.Error()
		} else {
			res["json"] = jsonv1.RawMessage(enc)
		}
		out[name] = res
	}
	b, _ := json.Marshal(out)
	if err := os.WriteFile("out.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
}
`

func rvGoTool(t *testing.T) string {
	t.Helper()
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goTool); err != nil {
		if goTool, err = exec.LookPath("go"); err != nil {
			t.Skip("go tool not found")
		}
	}
	return goTool
}

// rvGenerateInModule writes the generated client into a dot-directory in the
// module and returns the directory and the generated source.
func rvGenerateInModule(t *testing.T, r *Registry) (string, string) {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".gogenreview")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	files, err := NewGoGenerator(r).WithOptions(GoGeneratorOptions{OutputDir: dir, PackageName: "api"}).Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return dir, files[goClientFileName]
}

// rvRoundTrip runs every case through the generated client and returns the
// mismatches, keyed by method.
func rvRoundTrip(t *testing.T, r *Registry, groupType string, cases []rvCase) map[string]string {
	t.Helper()
	dir, src := rvGenerateInModule(t, r)
	serverJSON := map[string]jsonv1.RawMessage{}
	for _, c := range cases {
		b, err := MarshalWire(c.value)
		if err != nil {
			t.Fatalf("%s: server marshal: %v", c.method, err)
		}
		serverJSON[c.method] = b
	}
	b, _ := json.Marshal(serverJSON)
	if err := os.WriteFile(filepath.Join(dir, "cases.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	dump := strings.Replace(rvDumpTest, "GROUP", groupType, 1)
	if err := os.WriteFile(filepath.Join(dir, "dump_test.go"), []byte(dump), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(rvGoTool(t), "test", "-count=1", "-run", "TestDump", "./"+filepath.ToSlash(dir)) // #nosec G204 -- test tool
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running generated client: %v\n%s\n--- generated:\n%s", err, out, src)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "out.json"))
	if err != nil {
		t.Fatal(err)
	}
	var results map[string]rvResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}

	bad := map[string]string{}
	for _, c := range cases {
		res := results[c.method]
		want := serverJSON[c.method]
		var problems []string
		if res.DecodeErr != "" {
			problems = append(problems, "client decode: "+res.DecodeErr)
		}
		if len(res.JSON) > 0 && !rvSameJSON(want, res.JSON) {
			problems = append(problems, fmt.Sprintf("client re-encode differs\n  server: %s\n  client: %s", want, res.JSON))
		}
		// Client → server: decode the client's encoding with the server's
		// decoder and re-marshal.
		if len(res.JSON) > 0 {
			back := reflect.New(reflect.TypeOf(c.value))
			if err := unmarshalJSON(res.JSON, back.Interface()); err != nil {
				problems = append(problems, "server decode of client JSON: "+err.Error())
			} else if again, err := MarshalWire(back.Elem().Interface()); err != nil {
				problems = append(problems, "server re-marshal: "+err.Error())
			} else if !rvSameJSON(want, again) {
				problems = append(problems, fmt.Sprintf("server value changed after client round trip\n  before: %s\n  after:  %s", want, again))
			}
		}
		if len(problems) > 0 {
			bad[c.method] = strings.Join(problems, "\n")
		}
	}
	return bad
}

func rvSameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

var rvCasesOnce struct {
	bad map[string]string
	src string
}

func rvReviewResults(t *testing.T) map[string]string {
	t.Helper()
	if rvCasesOnce.bad != nil {
		return rvCasesOnce.bad
	}
	five := 5 * time.Second
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []rvCase{
		{"EnumText", rvWithEnumText{P: rvPrioHigh, ByP: map[rvPrio]int{rvPrioHigh: 1}, Score: 7}},
		{"PtrNull", rvPtrNull{X: nil}},
		{"EmbedsNull", rvEmbedsNull{NullString: sql.NullString{String: "s", Valid: true}, A: 1}},
		{"EmbedsBlob", rvEmbedsBlob{Blob: Blob{ContentType: "text/plain", Data: []byte("x")}, Name: "n"}},
		{"StringNull", rvStringNull{N: sql.NullInt64{Int64: 5, Valid: true}}},
		{"FormatNull", rvFormatNull{T: sql.NullTime{Time: when, Valid: true}}},
		{"Numbers", rvNumbers{Cents: 1234, ID: rvSnowflake{v: 1<<60 + 1}, IDs: rvIDs{1, 2}, App: rvAppender{s: "hello"}}},
		{"EmbedRules", rvEmbedRules{rvDeep: rvDeep{A: 1, B: 2}, rvSame1: rvSame1{X: 3}, rvSame2: rvSame2{X: 4}, A: 5, Case: "c"}},
		{"Std", rvStd{
			U:   url.URL{Scheme: "https", Host: "x"},
			PU:  &url.URL{Path: "/p"},
			IP:  net.ParseIP("10.0.0.1"),
			B:   new(big.Int).Lsh(big.NewInt(1), 70),
			D:   90 * time.Second,
			PD:  &five,
			NT:  sql.Null[time.Time]{V: when, Valid: true},
			Arr: [4]byte{1, 2, 3, 4},
			J:   jsontext.Value(`{"z":1}`),
		}},
	}
	rvCasesOnce.bad = rvRoundTrip(t, newReviewRegistry(), "RvHandlersClient", cases)
	return rvCasesOnce.bad
}

func rvExpectRoundTrip(t *testing.T, method string) {
	t.Helper()
	if msg, ok := rvReviewResults(t)[method]; ok {
		t.Errorf("%s does not round-trip:\n%s", method, msg)
	}
}

// --- Round-trip findings ----------------------------------------------------

// An int enum with MarshalText travels as its name ("High"), so it is
// declared as a string type with the marshaled values, also as a map key.
func TestGoWireIntEnumWithTextMarshaler(t *testing.T) {
	rvExpectRoundTrip(t, "EnumText")
	src := generateGo(t, newReviewRegistry(), GoGeneratorOptions{PackageName: "api"})
	for _, want := range []string{"type RvPrio string", `RvPrioHigh RvPrio = "High"`, "ByP   map[RvPrio]int"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
}

// A nil *sql.Null[T] for an unflattened T is null on the wire, so the
// pointer stays.
func TestGoWirePointerToUnflattenedSQLNull(t *testing.T) { rvExpectRoundTrip(t, "PtrNull") }

// An embedded sql.NullString is inlined by the server
// ({"String":..,"Valid":..}), not flattened.
func TestGoWireEmbeddedSQLNull(t *testing.T) { rvExpectRoundTrip(t, "EmbedsNull") }

// An embedded aprot.Blob must not become an embedded client.Blob, whose
// UnmarshalJSON would be promoted to the outer type and swallow every other
// field.
func TestGoWireEmbeddedBlob(t *testing.T) { rvExpectRoundTrip(t, "EmbedsBlob") }

// A `,string` option on a field whose type has its own marshaler (or is a
// flattened sql.Null*) is ignored by the server, so it is not copied to the
// generated plain type, which would honor it.
func TestGoWireStringOptionOnReplacedType(t *testing.T) { rvExpectRoundTrip(t, "StringNull") }

// A marshaler's zero value cannot tell the precision of a number or the
// element encoding of an array, so anything but a string or bool is raw JSON;
// a TextAppender-only type is a string.
func TestGoWireMarshalerShapes(t *testing.T) { rvExpectRoundTrip(t, "Numbers") }

// A `format:` tag on a flattened sql.NullTime is ignored by the server
// (its marshaler encodes the time.Time with defaults), so it is not copied.
func TestGoWireFormatTagOnSQLNull(t *testing.T) { rvExpectRoundTrip(t, "FormatNull") }

// Depth and conflict rules for embedded fields, and standard library types.
func TestGoWireEmbedRules(t *testing.T)  { rvExpectRoundTrip(t, "EmbedRules") }
func TestGoWireStdlibTypes(t *testing.T) { rvExpectRoundTrip(t, "Std") }

// --- Generation-level findings ----------------------------------------------

// rvGenerateAndVet generates r into the module and runs go vet on it.
func rvGenerateAndVet(t *testing.T, r *Registry) {
	t.Helper()
	dir, src := rvGenerateInModule(t, r)
	out, err := exec.Command(rvGoTool(t), "vet", "./"+filepath.ToSlash(dir)).CombinedOutput() // #nosec G204 -- test tool
	if err != nil {
		t.Fatalf("generated client does not build: %v\n%s\n--- generated:\n%s", err, out, src)
	}
}

type rvOnlyScore struct {
	S rvScore `json:"s"`
}

type rvScoreHandlers struct{}

func (h *rvScoreHandlers) Get(_ context.Context) (rvOnlyScore, error) { return rvOnlyScore{}, nil }

// A marshaler shape that is not raw JSON must not import jsontext, or a
// client without a jsontext.Value field fails to compile.
func TestGoGenerateNoUnusedJSONTextImport(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvScoreHandlers{})
	rvGenerateAndVet(t, r)
}

type rvPage[T any] struct {
	Items []T `json:"items"`
}

type rvGenericHandlers struct{}

func (h *rvGenericHandlers) Values(_ context.Context) (rvPage[rvElem], error) {
	return rvPage[rvElem]{}, nil
}
func (h *rvGenericHandlers) Pointers(_ context.Context) (rvPage[*rvElem], error) {
	return rvPage[*rvElem]{}, nil
}
func (h *rvGenericHandlers) Others(_ context.Context) (rvPage[other.Item], error) {
	return rvPage[other.Item]{}, nil
}
func (h *rvGenericHandlers) Locals(_ context.Context) (rvPage[Item], error) {
	return rvPage[Item]{}, nil
}

// Generic instantiations spell out pointers in their names, and type
// arguments with the same name from different packages keep their package.
func TestGoGenerateGenericInstantiationNames(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvGenericHandlers{})
	src := generateGo(t, r, GoGeneratorOptions{PackageName: "api"})
	for _, want := range []string{
		"type RvPageRvElem struct", "type RvPagePtrRvElem struct",
		"type RvPageOtherItem struct", "type RvPageAprotItem struct",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
	rvGenerateAndVet(t, r)
}

type rvDup struct {
	A int `json:"a"`
}

type RvDup struct {
	B int `json:"b"`
}

type rvDupHandlers struct{}

func (h *rvDupHandlers) Lower(_ context.Context) (rvDup, error) { return rvDup{}, nil }
func (h *rvDupHandlers) Upper(_ context.Context) (RvDup, error) { return RvDup{}, nil }

// An unexported type and an exported type that differ only in the case of
// the first letter (rvDup, RvDup) both want the name RvDup, and the package
// prefix does not separate them. A numeric suffix does, in a fixed order.
func TestGoGenerateSamePackageNameClash(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvDupHandlers{})
	src := generateGo(t, r, GoGeneratorOptions{PackageName: "api"})
	for _, want := range []string{
		"func (h RvDupHandlersClient) Upper(ctx context.Context) (AprotRvDup, error)",
		"func (h RvDupHandlersClient) Lower(ctx context.Context) (AprotRvDup2, error)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
}

type rvInner struct {
	S string `json:"s"`
}

type rvClash struct {
	rvInner
	RvInner string `json:"x"`
}

type rvClashHandlers struct{}

func (h *rvClashHandlers) Get(_ context.Context) (rvClash, error) { return rvClash{}, nil }

// An unexported embedded struct is re-exported under its generated type
// name, which can collide with a sibling field the server is allowed to have.
// Its fields are inlined instead.
func TestGoGenerateEmbeddedNameClashesWithField(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvClashHandlers{})
	src := generateGo(t, r, GoGeneratorOptions{PackageName: "api"})
	if got := fieldLine(t, src, "S"); got != "S string `json:\"s\"`" {
		t.Errorf("inlined field = %s", got)
	}
	if got := fieldLine(t, src, "RvInner"); got != "RvInner string `json:\"x\"`" {
		t.Errorf("sibling field = %s", got)
	}
}

type rvParamHandlers struct{}

// ByElem has a parameter spelled like the generated type it returns.
func (h *rvParamHandlers) ByElem(_ context.Context, RvElem int) (rvElem, error) {
	return rvElem{}, nil
}

// A parameter named like a generated type would shadow that type inside
// the method body, where client.Call[T] uses it, so it is renamed.
func TestGoGenerateParamShadowsGeneratedType(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvParamHandlers{})
	rvGenerateAndVet(t, r)
}

// rvTree is a self-referencing slice type with its own MarshalJSON.
type rvTree []rvTree

func (t rvTree) MarshalJSON() ([]byte, error) {
	if t == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]rvTree(t))
}

type rvTreeHandlers struct{}

func (h *rvTreeHandlers) Get(_ context.Context) (rvTree, error) { return nil, nil }

// A self-referencing slice type with its own marshaler must not recurse
// until the stack overflows. That would be a fatal error, so the case runs
// in a child process.
func TestGoGenerateSelfRecursiveMarshalerSlice(t *testing.T) {
	if os.Getenv("APROT_RV_TREE") == "1" {
		r := NewRegistry()
		r.Register(&rvTreeHandlers{})
		_, _ = NewGoGenerator(r).WithOptions(GoGeneratorOptions{PackageName: "api"}).Generate()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGoGenerateSelfRecursiveMarshalerSlice$") // #nosec G204 -- re-runs this test binary
	cmd.Env = append(os.Environ(), "APROT_RV_TREE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		lines := strings.SplitN(string(out), "\n", 4)
		t.Fatalf("Generate crashed the process: %v\n%s", err, strings.Join(lines[:min(3, len(lines))], "\n"))
	}
}

type rvImportNullHandlers struct{}

func (h *rvImportNullHandlers) Get(_ context.Context) (other.WithNull, error) {
	return other.WithNull{}, nil
}

// A type from an ImportTypes package is used as-is on the client. If it
// holds a sql.Null* field, the server flattens it and the client's decoder
// does not, so generation fails with an explanation.
func TestGoGenerateImportTypesRejectsSQLNull(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvImportNullHandlers{})
	_, err := NewGoGenerator(r).WithOptions(GoGeneratorOptions{
		PackageName: "api",
		ImportTypes: []string{"github.com/marrasen/aprot/internal/gogentest/other"},
	}).Generate()
	if err == nil || !strings.Contains(err.Error(), "sql.NullString") || !strings.Contains(err.Error(), "flatten") {
		t.Fatalf("want an error explaining the sql.NullString field, got %v", err)
	}
}

type rvRESTOnly struct{}

func (h *rvRESTOnly) Hidden(_ context.Context) (int, error) { return 0, nil }

type rvMCPOnly struct{}

func (h *rvMCPOnly) AlsoHidden(_ context.Context) (int, error) { return 0, nil }

// REST-only and MCP-only groups are not in the Go client, and output is
// stable across runs.
func TestGoGenerateSkipsNonSocketGroups(t *testing.T) {
	build := func() *Registry {
		r := newReviewRegistry()
		r.RegisterREST(&rvRESTOnly{})
		r.RegisterMCP(&rvMCPOnly{})
		return r
	}
	first := generateGo(t, build(), GoGeneratorOptions{PackageName: "api"})
	for _, unwanted := range []string{"Hidden", "AlsoHidden", "RvRESTOnly", "RvMCPOnly"} {
		if strings.Contains(first, unwanted) {
			t.Errorf("non-socket group leaked into Go client: %q", unwanted)
		}
	}
	for range 20 {
		if generateGo(t, build(), GoGeneratorOptions{PackageName: "api"}) != first {
			t.Fatal("output differs between runs")
		}
	}
}

type rvGenericPtrHandlers struct{}

func (h *rvGenericPtrHandlers) Values(_ context.Context) (rvPage[rvElem], error) {
	return rvPage[rvElem]{}, nil
}
func (h *rvGenericPtrHandlers) Pointers(_ context.Context) (rvPage[*rvElem], error) {
	return rvPage[*rvElem]{}, nil
}

// Page[Elem] and Page[*Elem] get different names.
func TestGoGenerateGenericPointerArgName(t *testing.T) {
	r := NewRegistry()
	r.Register(&rvGenericPtrHandlers{})
	if _, err := NewGoGenerator(r).WithOptions(GoGeneratorOptions{PackageName: "api"}).Generate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

// rvDate marshals as "2006-01-02", like most date-only types.
type rvDate struct{ t time.Time }

func (d rvDate) MarshalJSON() ([]byte, error) { return json.Marshal(d.t.Format(time.DateOnly)) }

func (d *rvDate) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(time.DateOnly, s)
	d.t = t
	return err
}

type rvBooking struct {
	Name string `json:"name"`
	Due  rvDate `json:"due"`
}

// A type whose marshaler emits a string becomes a bare string on the
// client. Its zero value ("") is not the server's zero encoding
// ("0001-01-01" here), so a request the client builds with the field unset
// can be rejected by the server; the README documents this.
func TestGoGenerateMarshalerStringShape(t *testing.T) {
	src := generateGo(t, func() *Registry {
		r := NewRegistry()
		r.Register(&rvBookingHandlers{})
		return r
	}(), GoGeneratorOptions{PackageName: "api"})
	if got := fieldLine(t, src, "Due"); got != "Due string `json:\"due\"`" {
		t.Fatalf("unexpected mapping: %s", got)
	}
}

type rvBookingHandlers struct{}

func (h *rvBookingHandlers) Book(_ context.Context, b rvBooking) error { return nil }
