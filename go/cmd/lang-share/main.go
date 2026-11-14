// Command lang-share prints the Go vs Python non-blank code-line counts of a
// git tree (the measured number behind the README "written in Go" badge).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"factory/go/internal/langshare"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lang-share", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "git repository directory")
	ref := fs.String("ref", "HEAD", "revision to measure")
	asJSON := fs.Bool("json", false, "print one JSON object")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "lang-share: unexpected arguments; usage: lang-share --repo <dir> [--ref <rev>] [--json]")
		return 2
	}
	c, err := langshare.Count(*repo, *ref)
	if err != nil {
		fmt.Fprintln(stderr, "lang-share:", err)
		return 2
	}
	if *asJSON {
		fmt.Fprint(stdout, c.JSON())
	} else {
		fmt.Fprint(stdout, c.Text())
	}
	return 0
}
