// Command lane-check says whether two branches, or the open PR heads, really conflict (git merge-tree), so a
// file-name lane rule is not needed to find out. Read-only. See docs/lanes.md.
package main

import (
	"fmt"
	"os"

	"factory/go/internal/lanecheck"
)

func main() { os.Exit(run()) }

func run() (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "lane-check: internal error:", r)
			code = lanecheck.ExitError
		}
	}()
	return lanecheck.Main(os.Args[1:], os.Stdout, os.Stderr)
}
