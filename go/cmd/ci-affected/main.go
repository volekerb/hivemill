// Command ci-affected is the Go port of its ci/ Python script; the logic lives in factory/go/internal/citools/affected.
package main

import (
	"os"

	"factory/go/internal/citools/affected"
)

func main() {
	os.Exit(affected.Main(os.Args[1:], os.Stdout, os.Stderr))
}
