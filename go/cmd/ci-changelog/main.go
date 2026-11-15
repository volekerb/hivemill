// Command ci-changelog is the Go port of ci/changelog.py (G-048 P5c).
package main

import (
	"os"

	"factory/go/internal/citools/changelog"
)

func main() {
	os.Exit(changelog.Main(os.Args[1:], os.Stdout, os.Stderr))
}
