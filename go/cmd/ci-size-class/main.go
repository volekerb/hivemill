// Command ci-size-class picks the size class of a PR diff; the logic lives in factory/go/internal/citools/sizeclass.
package main

import (
	"os"

	"factory/go/internal/citools/sizeclass"
)

func main() {
	os.Exit(sizeclass.Main(os.Args[1:], os.Stdout, os.Stderr))
}
