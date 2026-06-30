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

	// Vanilla client — used by the WS / SSE / REST tests.
	vanilla := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		OutputDir: "../api",
		Mode:      aprot.OutputVanilla,
	})
	if _, err := vanilla.Generate(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate vanilla client: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Generated e2e/api/ (vanilla)")

	// React client — used by the hook-execution test (react-hooks.test.tsx).
	react := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
		OutputDir: "../react-api",
		Mode:      aprot.OutputReact,
	})
	if _, err := react.Generate(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate react client: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Generated e2e/react-api/ (react)")
}
