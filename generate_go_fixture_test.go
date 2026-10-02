package aprot_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/internal/gogentest"
)

// goClientFixtureDir holds the committed client generated from the
// gogentest registry. The round-trip tests compile against it.
const goClientFixtureDir = "internal/goclienttest"

// generateGoClientFixture generates the fixture client in memory.
func generateGoClientFixture(t *testing.T) map[string]string {
	t.Helper()
	registry, _ := gogentest.NewRegistry()
	files, err := aprot.NewGoGenerator(registry).WithOptions(aprot.GoGeneratorOptions{
		PackageName: "goclienttest",
		ImportTypes: gogentest.ImportTypes,
	}).Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return files
}

// TestGoClientFixtureUpToDate fails when internal/goclienttest is stale.
// Regenerate it with:
//
//	APROT_UPDATE_GO_FIXTURE=1 go test -run TestGoClientFixtureUpToDate .
func TestGoClientFixtureUpToDate(t *testing.T) {
	files := generateGoClientFixture(t)
	update := os.Getenv("APROT_UPDATE_GO_FIXTURE") != ""
	for name, want := range files {
		path := filepath.Join(goClientFixtureDir, name)
		if update {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading committed fixture: %v (regenerate with APROT_UPDATE_GO_FIXTURE=1 go test -run TestGoClientFixtureUpToDate .)", err)
		}
		if string(got) != want {
			t.Errorf("%s is stale; regenerate with APROT_UPDATE_GO_FIXTURE=1 go test -run TestGoClientFixtureUpToDate .", path)
		}
	}
}
