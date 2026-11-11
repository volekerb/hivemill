// Command no-new-python fails when a diff adds Python files (G-041 SC4).
//
//	no-new-python --repo <dir> --base <sha> --head <sha>
//
// Exit 0: no new Python. Exit 1: new Python found. Exit 2: bad arguments or a git error (fails closed).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"factory/go/internal/nonewpy"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("no-new-python", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "git repository directory")
	base := fs.String("base", "", "base commit (required)")
	head := fs.String("head", "HEAD", "head commit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *base == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "no-new-python: usage: no-new-python --repo <dir> --base <sha> --head <sha>")
		return 2
	}
	bad, n, err := nonewpy.Run(*repo, *base, *head)
	if err != nil {
		fmt.Fprintf(stderr, "no-new-python: %v\n", err)
		return 2
	}
	if len(bad) > 0 {
		for _, v := range bad {
			fmt.Fprintf(stdout, "no-new-python: new Python file %s (status %s); all new code is Go (G-041 SC4)\n", v.Path, v.Status)
		}
		return 1
	}
	fmt.Fprintf(stdout, "no-new-python: ok (%d changed files, 0 new Python)\n", n)
	return 0
}
