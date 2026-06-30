package tasks

import (
	"bytes"
	"strings"
	"testing"

	"github.com/marrasen/aprot"
)

// MapValueInfo is a plain struct used only as a map value, never directly as a
// field type.
type MapValueInfo struct {
	Label    string `json:"label"`
	Priority int    `json:"priority"`
}

// MetaWithMapValues reaches MapValueInfo only through a map value.
type MetaWithMapValues struct {
	ByKey map[string]MapValueInfo `json:"byKey"`
}

// A struct reachable only through a map value must still have its interface
// declared, or the generated TS references an undeclared type. (#207 P2)
func TestGenerateWithMetaMapValueStruct(t *testing.T) {
	registry := aprot.NewRegistry()
	registry.Register(&genTestHandler{})
	EnableWithMeta[MetaWithMapValues](registry)

	gen := aprot.NewGenerator(registry)
	var buf bytes.Buffer
	if err := gen.GenerateTo(&buf); err != nil {
		t.Fatalf("GenerateTo failed: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "interface MapValueInfo") {
		t.Error("expected MapValueInfo interface to be generated (struct reached only through a map value)")
	}
	if !strings.Contains(out, "Record<string, MapValueInfo>") {
		t.Errorf("expected byKey field typed as Record<string, MapValueInfo>")
	}
}
