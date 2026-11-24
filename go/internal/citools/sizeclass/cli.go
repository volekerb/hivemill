package sizeclass

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Main is the ci-size-class command: `ci-size-class --base REF [--head REF] [--root DIR] [--github-output]`.
// It exits 0 whenever it prints a result (errors give the fail-safe) and 2 on a usage error.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci-size-class", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base ref (required)")
	head := fs.String("head", "HEAD", "head ref")
	root := fs.String("root", ".", "repository root")
	ghOut := fs.Bool("github-output", false, "also append the result to the file named by GITHUB_OUTPUT")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *base == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "ci-size-class: usage: ci-size-class --base REF [--head REF] [--root DIR] [--github-output]")
		return 2
	}
	res := Select(*root, *base, *head)
	reason := strings.NewReplacer("\n", " ", "\r", " ").Replace(res.Reason)
	text := fmt.Sprintf("class=%s\nbudget_min=%d\nchecks=%s\nlines=%d\nreason=%s", res.Class, res.BudgetMin, res.Checks, res.Lines, reason)
	fmt.Fprintln(stdout, text)
	if *ghOut {
		p := os.Getenv("GITHUB_OUTPUT")
		if p == "" {
			fmt.Fprintln(stderr, "ci-size-class: --github-output needs GITHUB_OUTPUT set; set it or drop the flag")
			return 2
		}
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "ci-size-class: cannot open the GITHUB_OUTPUT file: %v\n", err)
			return 2
		}
		defer f.Close()
		fmt.Fprintln(f, text)
	}
	return 0
}
