// Command verify runs a change and prints a step trace; see docs/verify.md. Exit 0 = PASS, 1 = FAIL.
package main

import (
	"io"
	"os"

	"factory/go/internal/verify"
)

func main() { os.Exit(run(os.Args[1:], os.Environ(), os.Stdout)) }

func run(args, environ []string, stdout io.Writer) int { return verify.Run(args, environ, stdout) }
