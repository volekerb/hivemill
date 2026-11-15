package tier

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"factory/go/internal/citools/affected"
)

// Main is the ci-tier command: `ci-tier classify|docs-check --base B [--head H] [--root DIR]`; classify also takes
// --only-file F and --github-output.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "classify" && args[0] != "docs-check") {
		fmt.Fprintln(stderr, "ci-tier: usage: ci-tier classify|docs-check --base BASE [--head HEAD] [--root DIR]")
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("ci-tier "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base ref (required)")
	head := fs.String("head", "HEAD", "head ref")
	root := fs.String("root", ".", "repository root")
	onlyFile := fs.String("only-file", "", "classify: write the affected modules here, one per line")
	ghOut := fs.Bool("github-output", false, "classify: also append the result to the file named by GITHUB_OUTPUT")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *base == "" || fs.NArg() > 0 {
		fmt.Fprintf(stderr, "ci-tier %s: --base is required\n", cmd)
		return 2
	}
	if cmd == "docs-check" {
		if *onlyFile != "" || *ghOut {
			fmt.Fprintln(stderr, "ci-tier docs-check: --only-file and --github-output belong to classify")
			return 2
		}
		probs, err := DocsProblems(*root, *base, *head)
		if err != nil {
			fmt.Fprintf(stderr, "ci_tier: %v\n", err)
			if _, ok := err.(*affected.Error); ok {
				return 2
			}
			return 1
		}
		for _, p := range probs {
			fmt.Fprintf(stderr, "docs-check: %s\n", p)
		}
		if len(probs) > 0 {
			fmt.Fprintln(stdout, "docs-check: FAILED")
			return 1
		}
		fmt.Fprintln(stdout, "docs-check: OK")
		return 0
	}
	res, err := Plan(*root, *base, *head)
	if err != nil {
		fmt.Fprintf(stderr, "::warning::ci_tier could not classify (%v); running the full suite\n", err)
		res = Fallback(fmt.Sprintf("classifier error: %v", err))
	}
	text := fmt.Sprintf("tier=%s\nmode=%s\ngo=%s\nreason=%s\nmodules=%s", res.Tier, res.Mode, res.Go, res.Reason, strings.Join(res.Modules, " "))
	fmt.Fprintln(stdout, text)
	if *onlyFile != "" {
		body := strings.Join(res.Modules, "\n")
		if len(res.Modules) > 0 {
			body += "\n"
		}
		if err := os.WriteFile(*onlyFile, []byte(body), 0o644); err != nil {
			fmt.Fprintf(stderr, "ci_tier: cannot write %s: %v\n", *onlyFile, err)
			return 1
		}
	}
	if *ghOut {
		p := os.Getenv("GITHUB_OUTPUT")
		if p == "" {
			fmt.Fprintln(stderr, "ci_tier: --github-output needs GITHUB_OUTPUT set; set it or drop the flag")
			return 1
		}
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "ci_tier: cannot open the GITHUB_OUTPUT file: %v\n", err)
			return 1
		}
		defer f.Close()
		fmt.Fprintln(f, text)
	}
	return 0
}
