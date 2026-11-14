package affected

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultRoot is the repository the tool works on when --root is not given: the git top level of the current
// directory (ci/affected.py uses the checkout the script lives in), else ".".
func DefaultRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "."
	}
	return strings.TrimSpace(string(out))
}

// runSnippet runs the selection through ci/affected.py of the same checkout: the slot gate (tests/_slot.py) first,
// then affected.run (in process, no cap) or run_quick (child process groups, hard budget). The argument is JSON.
const runSnippet = `import json, os, sys
a = json.loads(sys.argv[1])
sys.path.insert(0, a["ci"])
sys.path.insert(0, a["tests"])
import _slot
import affected
refused = _slot.local_run_gate()
if refused:
    print("affected: %s" % refused, file=sys.stderr)
    sys.exit(EXIT_LOAD)
sys.exit(affected.run(a["mods"], a["tests"], a["budget"]))
`

func runPython(root string, mods []string, budget *int, stdout, stderr io.Writer) int {
	arg, _ := json.Marshal(map[string]any{
		"ci": filepath.Join(root, "ci"), "tests": filepath.Join(root, "tests"), "mods": mods, "budget": budget,
	})
	cmd := exec.Command("python3", "-c", strings.Replace(runSnippet, "EXIT_LOAD", fmt.Sprint(ExitLoad), 1), string(arg))
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(stderr, "affected: cannot run python3 for the test run: %v; install python3 or rely on CI\n", err)
		return 2
	}
	return 0
}

// Main is the ci-affected command: flags, stdout and exit codes as ci/affected.py.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci-affected", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base ref (required)")
	head := fs.String("head", "HEAD", "head ref")
	list := fs.Bool("list", false, "print the selection (default)")
	run := fs.Bool("run", false, "run the selection")
	quick := fs.Bool("quick", false, "role mode: affected modules only, never FULL (rely on CI)")
	budget := fs.Int("budget", 0, "--quick with this time budget in seconds (default 120)")
	root := fs.String("root", "", "repository root (default: git top level of the current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	budgetSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "budget" {
			budgetSet = true
		}
	})
	if *base == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "ci-affected: usage: ci-affected --base REF [--head REF] [--list|--run] [--quick] [--budget SECONDS]")
		return 2
	}
	if *list && *run {
		fmt.Fprintln(stderr, "ci-affected: argument --run: not allowed with argument --list")
		return 2
	}
	if budgetSet && *budget < 1 {
		fmt.Fprintln(stderr, "ci-affected: --budget must be at least 1 second")
		return 2
	}
	if *root == "" {
		*root = DefaultRoot()
	}
	isQuick := *quick || budgetSet
	var b *int
	if isQuick {
		v := QuickBudgetS
		if budgetSet {
			v = *budget
		}
		b = &v
	}
	sel, err := Select(*root, *base, *head, "")
	if err != nil {
		fmt.Fprintf(stderr, "affected: %v\n", err)
		return 2
	}
	testsDir := filepath.Join(*root, "tests")
	if sel.Full {
		if isQuick {
			fmt.Fprintf(stdout, "FULL needed: rely on CI (%s)\n", sel.Note)
			return 0
		}
		fmt.Fprintf(stdout, "FULL (%s)\n", sel.Note)
		if *run {
			all, err := AllModules(testsDir)
			if err != nil {
				fmt.Fprintf(stderr, "affected: %v\n", err)
				return 2
			}
			return runPython(*root, all, nil, stdout, stderr)
		}
		return 0
	}
	fmt.Fprintln(stdout, strings.Join(sel.Modules, "\n"))
	if *run {
		return runPython(*root, sel.Modules, b, stdout, stderr)
	}
	return 0
}
