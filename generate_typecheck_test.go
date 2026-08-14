package aprot

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file compiles the generated TypeScript with tsc. Nothing else does:
// the Go tests assert on substrings, so a client that references a method it
// does not define (see issue #305) passes them. Every output mode is covered
// because the single-file and multi-file templates render the same shared
// blocks and any drift between them shows up as a compile error here.
//
// The test needs a TypeScript compiler. It borrows the one installed for the
// React example (`cd example/react/client && npm ci`) and skips when that is
// missing, so `go test ./...` stays dependency-free for contributors who only
// touch Go. CI installs it before running the Go tests.

// --- Handlers covering every generated call shape -------------------------

type tcItem struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	State TaskStatus `json:"state"`
}

type tcCreateRequest struct {
	Name string `json:"name"`
}

type tcItemChangedEvent struct {
	ID string `json:"id"`
}

type tcHandlers struct{}

// Unary with a struct param — generates request/subscribe/useQuery with params.
func (h *tcHandlers) Create(_ context.Context, req *tcCreateRequest) (*tcItem, error) {
	return &tcItem{ID: "1", Name: req.Name}, nil
}

// Unary without params — generates the no-param useQuery overload.
func (h *tcHandlers) List(_ context.Context) ([]*tcItem, error) { return nil, nil }

// Void response — generates Promise<void>.
func (h *tcHandlers) Remove(_ context.Context, id string) error { return nil }

// iter.Seq — generates requestStream + useStream.
func (h *tcHandlers) Numbers(_ context.Context, count int) (iter.Seq[*tcItem], error) {
	return func(yield func(*tcItem) bool) {}, nil
}

// Reserved word — the emitted function name must be escaped, or the module
// does not parse (issue #309). "delete" is reserved everywhere.
func (h *tcHandlers) Delete(_ context.Context, id string) error { return nil }

// Reserved in strict-mode code only; generated modules are always strict, so
// this fails the same way an unconditionally reserved word does.
func (h *tcHandlers) Static(_ context.Context) (*tcItem, error) { return nil, nil }

// iter.Seq2 — generates the keyed requestStream + useStream.
func (h *tcHandlers) Pairs(_ context.Context) (iter.Seq2[string, *tcItem], error) {
	return func(yield func(string, *tcItem) bool) {}, nil
}

// newTypecheckRegistry builds a registry that exercises unary, void, stream,
// stream2, push events and enums in one place, so a single compile covers
// every branch of the handler-function and hook templates.
func newTypecheckRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	registry.Register(&tcHandlers{})
	registry.RegisterPushEventFor(&tcHandlers{}, tcItemChangedEvent{})
	registry.RegisterEnumFor(&tcHandlers{}, TaskStatusValues())
	return registry
}

// --- The test -------------------------------------------------------------

func TestGeneratedClientsTypecheck(t *testing.T) {
	tsc, nodeModules := findTypeScript(t)

	cases := []struct {
		name      string
		mode      OutputMode
		multiFile bool
	}{
		{"single-file/vanilla", OutputVanilla, false},
		{"single-file/react", OutputReact, false},
		{"multi-file/vanilla", OutputVanilla, true},
		{"multi-file/react", OutputReact, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// tsc resolves 'react' and its types by walking up from the
			// source file, so the sources need a node_modules next to them.
			if err := os.Symlink(nodeModules, filepath.Join(dir, "node_modules")); err != nil {
				t.Skipf("cannot symlink node_modules: %v", err)
			}

			gen := NewGenerator(newTypecheckRegistry(t)).WithOptions(GeneratorOptions{
				OutputDir: dir,
				Mode:      tc.mode,
			})

			if tc.multiFile {
				if _, err := gen.Generate(); err != nil {
					t.Fatalf("Generate failed: %v", err)
				}
			} else {
				f, err := os.Create(filepath.Join(dir, "client.ts"))
				if err != nil {
					t.Fatalf("create client.ts: %v", err)
				}
				if err := gen.GenerateTo(f); err != nil {
					f.Close()
					t.Fatalf("GenerateTo failed: %v", err)
				}
				if err := f.Close(); err != nil {
					t.Fatalf("close client.ts: %v", err)
				}
			}

			writeTypecheckConfig(t, dir, tc.mode)
			runTSC(t, tsc, dir)
		})
	}
}

// writeTypecheckConfig mirrors the example clients' tsconfig: strict, DOM
// libs, and (for React output) the JSX runtime. noUnusedLocals is left off —
// it flags style, not the correctness this test is about.
func writeTypecheckConfig(t *testing.T, dir string, mode OutputMode) {
	t.Helper()
	opts := map[string]any{
		"target":           "ES2020",
		"lib":              []string{"ES2020", "DOM", "DOM.Iterable"},
		"module":           "ESNext",
		"moduleResolution": "bundler",
		"strict":           true,
		"noEmit":           true,
		"skipLibCheck":     true,
	}
	if mode == OutputReact {
		opts["jsx"] = "react-jsx"
	}
	cfg, err := json.MarshalIndent(map[string]any{
		"compilerOptions": opts,
		"include":         []string{"*.ts"},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal tsconfig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), cfg, 0o600); err != nil {
		t.Fatalf("write tsconfig: %v", err)
	}
}

func runTSC(t *testing.T, tsc, dir string) {
	t.Helper()
	cmd := exec.Command(tsc, "--noEmit", "--project", "tsconfig.json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		names, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
		for i, n := range names {
			names[i] = filepath.Base(n)
		}
		t.Fatalf("tsc failed for %s:\n%s", strings.Join(names, ", "), out)
	}
}

// findTypeScript locates the tsc binary and node_modules installed for the
// React example, skipping the test when the example's dependencies have not
// been installed.
func findTypeScript(t *testing.T) (tsc, nodeModules string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("typecheck test runs on unix-like systems")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate repository root")
	}
	root := filepath.Dir(thisFile)
	nodeModules = filepath.Join(root, "example", "react", "client", "node_modules")
	tsc = filepath.Join(nodeModules, ".bin", "tsc")
	if _, err := os.Stat(tsc); err != nil {
		t.Skipf("tsc not found at %s — run `cd example/react/client && npm ci` to enable this test", tsc)
	}
	return tsc, nodeModules
}
