// Command ci-release-check is the Go port of ci/release_check.py (G-048 P5c).
package main

import (
	"os"

	"factory/go/internal/citools/releasecheck"
)

func main() {
	os.Exit(releasecheck.Main(os.Args[1:], os.Stdout, os.Stderr))
}
