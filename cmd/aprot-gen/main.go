package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	output := flag.String("o", "", "Output file (default: stdout)")
	flag.Parse()

	if *output == "" {
		fmt.Fprintln(os.Stderr, "aprot-gen: TypeScript client generator for aprot")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "This tool generates TypeScript client code from Go handler types.")
		fmt.Fprintln(os.Stderr, "It should be used programmatically by importing the aprot package")
		fmt.Fprintln(os.Stderr, "and calling Generator.Generate() with your registered handlers.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Example usage in Go code:")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "    registry := aprot.NewRegistry()")
		fmt.Fprintln(os.Stderr, "    registry.Register(&MyHandlers{})")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "    gen := aprot.NewGenerator(registry)")
		fmt.Fprintln(os.Stderr, "    gen.RegisterPushEvent(\"UserUpdated\", UserUpdatedEvent{})")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "    f, _ := os.Create(\"client.ts\")")
		fmt.Fprintln(os.Stderr, "    gen.Generate(f)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Error: Direct CLI generation not supported.\n")
	fmt.Fprintf(os.Stderr, "Use the aprot.Generator programmatically with your handlers.\n")
	os.Exit(1)
}
