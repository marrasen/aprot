// Package gentestpkg exists only to host test fixture types in a Go package
// whose name is different from "aprot", so that generate_test.go can exercise
// code paths where shared types and shared enums live in separate per-package
// shared TypeScript files and reference each other across packages.
package gentestpkg

// Color is a string enum registered with aprot.RegisterEnum in tests.
// Its TypeScript counterpart lives in gentestpkg.ts.
type Color string

const (
	ColorRed   Color = "red"
	ColorGreen Color = "green"
	ColorBlue  Color = "blue"
)

// ColorValues is the canonical list used by RegisterEnum.
func ColorValues() []Color {
	return []Color{ColorRed, ColorGreen, ColorBlue}
}
