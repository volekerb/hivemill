// Command hm-spend reports where Claude tokens go on this machine and applies context limits to its agents, without
// a factory: it reads Claude Code transcripts and settings files only. See docs/token-discipline.md.
package main

import (
	"fmt"
	"os"

	"factory/go/internal/kit/spend"
)

func main() { os.Exit(run()) }

func run() (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "hm-spend: internal error:", r)
			code = spend.ExitError
		}
	}()
	return spend.Main(os.Args[1:], os.Stdout, os.Stderr)
}
