// Command ci-tier is the Go port of its ci/ Python script; the logic lives in factory/go/internal/citools/tier.
package main

import (
	"os"

	"factory/go/internal/citools/tier"
)

func main() {
	os.Exit(tier.Main(os.Args[1:], os.Stdout, os.Stderr))
}
