// Command ci-publish-repo is the Go port of ci/publish_repo.py (G-048 P5c).
package main

import (
	"os"

	"factory/go/internal/citools/publish"
)

func main() {
	os.Exit(publish.Main(os.Args[1:], os.Stdout, os.Stderr))
}
