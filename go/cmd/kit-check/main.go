// Command kit-check validates the Hivemill Kit registry, every registered plugin and the skill template (ADR 0007).
// Run it from the repo root, or pass --root.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"factory/go/internal/kit"
	"factory/go/internal/kit/check"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if kit.HandleVersion(stdout, "kit-check", args) {
		return kit.OK
	}
	fs := flag.NewFlagSet("kit-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repo root")
	if err := fs.Parse(args); err != nil {
		return kit.Usage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "kit-check: unexpected arguments; usage: kit-check [--root DIR]")
		return kit.Usage
	}
	res := check.Run(*root)
	if len(res.Findings) > 0 {
		for _, f := range res.Findings {
			fmt.Fprintln(stdout, f)
		}
		return kit.Err
	}
	fmt.Fprintf(stdout, "kit-check: ok (%d skills)\n", res.Skills)
	return kit.OK
}
