package shard

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func defaultRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "."
	}
	return strings.TrimSpace(string(out))
}

// Main is the ci-shard command (see the package doc). Exit 0 ok, 1 failure (ci/shard.py: ShardError and failed
// tests), 2 usage.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "ci-shard: usage: ci-shard list|run|verify|timings [flags]")
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("ci-shard "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "repository root (default: git top level of the current directory)")
	shardN := fs.Int("shard", 0, "1-based shard index")
	total := fs.Int("total", 0, "number of shards")
	out := fs.String("out", "", "report file (run) or timing file (timings)")
	only := fs.String("only", "", "file of module names (the affected selection)")
	dir := fs.String("dir", "", "directory of shard reports")
	switch cmd {
	case "list", "run", "verify", "timings":
	default:
		fmt.Fprintf(stderr, "ci-shard: unknown command %q; use list, run, verify or timings\n", cmd)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	need := map[string][]string{"list": nil, "run": {"shard", "total", "out"}, "verify": {"total", "dir"}, "timings": {"dir"}}
	for _, k := range need[cmd] {
		if !seen[k] {
			fmt.Fprintf(stderr, "ci-shard %s: --%s is required\n", cmd, k)
			return 2
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "ci-shard %s: unexpected argument %q\n", cmd, fs.Arg(0))
		return 2
	}
	if *root == "" {
		*root = defaultRoot()
	}
	timingsFile := filepath.Join(*root, "ci", "shard_timings.json")
	fail := func(err error) int {
		var e *Error
		if errors.As(err, &e) {
			fmt.Fprintf(stderr, "shard: %s\n", e.Msg)
		} else {
			fmt.Fprintf(stderr, "shard: %v\n", err)
		}
		return 1
	}
	switch cmd {
	case "list":
		mods, err := AllModules(filepath.Join(*root, "tests"))
		if err != nil {
			return fail(err)
		}
		if seen["shard"] {
			t := *total
			if !seen["total"] || t == 0 {
				t = 1
			}
			timings, err := LoadTimings(timingsFile)
			if err != nil {
				return fail(err)
			}
			if mods, err = Select(mods, *shardN, t, timings); err != nil {
				return fail(err)
			}
		}
		fmt.Fprintln(stdout, strings.Join(mods, "\n"))
		return 0
	case "run":
		onlySet, err := ReadOnly(*only)
		if err != nil {
			return fail(err)
		}
		code, err := Run(*root, *shardN, *total, *out, onlySet, os.Stderr)
		if err != nil {
			return fail(err)
		}
		return code
	case "timings":
		data, err := MergeTimings(*dir)
		if err != nil {
			return fail(err)
		}
		target := *out
		if target == "" {
			target = timingsFile
		}
		if err := os.WriteFile(target, []byte(FormatTimings(data)), 0o644); err != nil {
			return fail(fmt.Errorf("cannot write %s: %v", target, err))
		}
		fmt.Fprintf(stdout, "wrote %d module timings to %s\n", len(data), target)
		return 0
	}
	onlySet, err := ReadOnly(*only)
	if err != nil {
		return fail(err)
	}
	summary, _, err := Verify(*root, *dir, *total, onlySet)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, summary)
	return 0
}
