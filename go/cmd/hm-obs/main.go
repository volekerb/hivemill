// Command hm-obs keeps the factory's observability store (state/obs.db) and answers the web UI's Spend and Flow
// pages from it. See docs/obs.md.
package main

import (
	"fmt"
	"os"

	"factory/go/internal/kit/obs"
)

func main() { os.Exit(run()) }

func run() (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "hm-obs: internal error:", r)
			code = obs.ExitError
		}
	}()
	return obs.Main(os.Args[1:], os.Stdout, os.Stderr)
}
