//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/example/react/api"
)

func main() {
	registry, _, _ := api.NewRegistry()

	// Generate TypeScript client + Zod schemas
	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		OutputDir: "../../client/src/api",
		Mode:      aprot.OutputReact,
		Zod:       true,
	})

	files, err := gen.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate: %v\n", err)
		os.Exit(1)
	}

	for filename := range files {
		fmt.Printf("Generated client/src/api/%s\n", filename)
	}

	// Generate OpenAPI spec (only includes RegisterREST handlers)
	oag := aprot.NewOpenAPIGenerator(registry, "aprot React Example API", "1.0.0")
	data, err := oag.GenerateJSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate OpenAPI: %v\n", err)
		os.Exit(1)
	}

	outPath := filepath.Join("../../client/src/api", "openapi.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write openapi.json: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Generated client/src/api/openapi.json")
}
