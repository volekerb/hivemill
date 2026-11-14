// Command ci-shard is the Go port of its ci/ Python script; the logic lives in factory/go/internal/citools/shard.
package main

import (
	"os"

	"factory/go/internal/citools/shard"
)

func main() {
	os.Exit(shard.Main(os.Args[1:], os.Stdout, os.Stderr))
}
