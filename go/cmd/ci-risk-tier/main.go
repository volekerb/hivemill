// Command ci-risk-tier is the Go port of its ci/ Python script; the logic lives in factory/go/internal/citools/risktier.
package main

import (
	"os"

	"factory/go/internal/citools/risktier"
)

func main() {
	os.Exit(risktier.Main(os.Args[1:], os.Stdout, os.Stderr))
}
