package testcount

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Main is the ci-test-count-gate command (see the package doc).
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "count" && args[0] != "check") {
		fmt.Fprintln(stderr, "ci-test-count-gate: usage: count <tree> [--python-only] | check --base N --pr M --body-file F [--base-go N --pr-go M]")
		return 2
	}
	if args[0] == "count" {
		return count(args[1:], stdout, stderr)
	}
	return check(args[1:], stdout, stderr)
}

func count(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci-test-count-gate count", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pyOnly := fs.Bool("python-only", false, "print only the Python count, exactly like ci/test_count_gate.py count")
	// flags may follow the tree argument (argparse allows it): split them off
	var tree string
	var rest []string
	for _, a := range args {
		if len(a) > 0 && a[0] != '-' && tree == "" {
			tree = a
		} else {
			rest = append(rest, a)
		}
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if tree == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "ci-test-count-gate count: exactly one <tree> argument is required")
		return 2
	}
	py, err := CountPython(tree)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	var goN int
	if !*pyOnly {
		if goN, err = CountGo(tree); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
	}
	fmt.Fprintln(stdout, py)
	if !*pyOnly {
		fmt.Fprintf(stdout, "go-tests: %d\n", goN)
	}
	return 0
}

func check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci-test-count-gate check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.Int("base", -1, "Python test count of the base (required)")
	pr := fs.Int("pr", -1, "Python test count of the PR (required)")
	bodyFile := fs.String("body-file", "", "file holding the PR body (required)")
	baseGo := fs.Int("base-go", -1, "Go test count of the base (optional, with --pr-go)")
	prGo := fs.Int("pr-go", -1, "Go test count of the PR (optional, with --base-go)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if !seen["base"] || !seen["pr"] || !seen["body-file"] || fs.NArg() > 0 || seen["base-go"] != seen["pr-go"] {
		fmt.Fprintln(stderr, "ci-test-count-gate check: --base, --pr and --body-file are required; --base-go and --pr-go go together")
		return 2
	}
	body := ""
	if b, err := os.ReadFile(*bodyFile); err == nil {
		body = string(b)
	}
	ok, msg := Decide(*base, *pr, body)
	fmt.Fprintln(stdout, msg)
	if seen["base-go"] {
		gok, gmsg := DecideGo(*baseGo, *prGo, body)
		fmt.Fprintln(stdout, gmsg)
		ok = ok && gok
	}
	if ok {
		return 0
	}
	return 1
}
