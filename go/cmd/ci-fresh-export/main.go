// Command ci-fresh-export is the Go port of ci/fresh_export.py (G-048 P5c).
package main

import (
	"os"

	"factory/go/internal/citools/freshexport"
)

func main() {
	os.Exit(freshexport.Main(os.Args[1:], os.Stdout, os.Stderr))
}
