package risktier

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
)

// Main is the ci-risk-tier command: `ci-risk-tier --base B [--head H] [--root DIR] [--rules FILE]`. Output: the tier,
// then one "reason: ..." line per T3 reason. Exit 0 classified, 2 usage, rules or git error.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci-risk-tier", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base ref (required)")
	head := fs.String("head", "HEAD", "head ref")
	root := fs.String("root", ".", "repository root")
	rules := fs.String("rules", "", "tier rules file (default: ci/tier_rules.json under --root)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *base == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "ci-risk-tier: usage: ci-risk-tier --base REF [--head REF] [--root DIR] [--rules FILE]")
		return 2
	}
	if *rules == "" {
		*rules = filepath.Join(*root, "ci", "tier_rules.json")
	}
	r, err := LoadRules(*rules)
	if err != nil {
		fmt.Fprintf(stderr, "ci-risk-tier: %v\n", err)
		return 2
	}
	t, why, err := ClassifyRange(*root, *base, *head, r)
	if err != nil {
		fmt.Fprintf(stderr, "ci-risk-tier: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout, t)
	for _, w := range why {
		fmt.Fprintf(stdout, "reason: %s\n", w)
	}
	return 0
}
