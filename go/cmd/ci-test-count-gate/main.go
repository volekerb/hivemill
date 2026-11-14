// Command ci-test-count-gate is the Go port of its ci/ Python script; the logic lives in factory/go/internal/citools/testcount.
package main

import (
	"os"

	"factory/go/internal/citools/testcount"
)

func main() {
	os.Exit(testcount.Main(os.Args[1:], os.Stdout, os.Stderr))
}
