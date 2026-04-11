//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/example/react/api"
)

func main() {
	registry, _, _ := api.NewRegistry()

	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		OutputDir:      "../../client/src/api",
		Mode:           aprot.OutputReact,
		Zod:            true,
		OpenAPI:        true,
		OpenAPITitle:   "aprot React Example API",
		OpenAPIVersion: "1.0.0",
	})

	files, err := gen.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate: %v\n", err)
		os.Exit(1)
	}

	for filename := range files {
		fmt.Printf("Generated client/src/api/%s\n", filename)
	}
}
