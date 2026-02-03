//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/example/vanilla/api"
)

func main() {
	// Create handlers and registry for code generation
	tokenStore := api.NewTokenStore()
	handlers := api.NewHandlers(tokenStore)
	registry := api.NewRegistry(handlers)

	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		OutputDir: "../../client/static/api",
		Mode:      aprot.OutputVanilla,
	})

	files, err := gen.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate: %v\n", err)
		os.Exit(1)
	}

	for filename := range files {
		fmt.Printf("Generated client/static/api/%s\n", filename)
	}
}
