// Command ci-public-text-guard is the Go port of ci/public_text_guard.py; the logic lives in factory/go/internal/guard.
package main

import (
	"os"

	"factory/go/internal/guard"
)

func main() {
	os.Exit(guard.CIPublicTextGuardMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
