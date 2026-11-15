// Command ci-scrub-scan is the Go port of ci/scrub_scan.py; the logic lives in factory/go/internal/guard.
package main

import (
	"os"

	"factory/go/internal/guard"
)

func main() {
	os.Exit(guard.CIScrubScanMain(os.Args[1:], os.Stdout, os.Stderr))
}
