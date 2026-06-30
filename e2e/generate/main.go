//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/e2e/e2eapi"
	"github.com/marrasen/aprot/example/vanilla/api"
)

func main() {
	tokenStore := api.NewTokenStore()
	state := api.NewSharedState(tokenStore)
	authMiddleware := api.AuthMiddleware(tokenStore)
	registry := api.NewRegistry(state, authMiddleware)

	// Add REST + validation handlers so the generated client includes them.
	e2eapi.Register(registry)

	gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		OutputDir: "../api",
		Mode:      aprot.OutputVanilla,
	})

	files, err := gen.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate: %v\n", err)
		os.Exit(1)
	}

	for filename := range files {
		fmt.Printf("Generated e2e/api/%s\n", filename)
	}
}
