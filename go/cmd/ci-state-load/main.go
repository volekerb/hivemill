// Command ci-state-load is the Go port of ci/state_load.py (G-048 P5c).
package main

import (
	"os"

	"factory/go/internal/citools/stateload"
)

func main() {
	os.Exit(stateload.Main(os.Args[1:], os.Stdout, os.Stderr))
}
