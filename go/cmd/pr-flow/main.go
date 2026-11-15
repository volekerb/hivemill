// Command pr-flow reports how pull requests flow through the factory pipeline: merges per day, open-to-merge hours,
// open PRs over 4 h and 6 h, and stage hours per org. It is read-only (docs/pr-flow.md).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"factory/go/internal/prflow"
)

const usage = "usage: pr-flow --repo <owner/name> [--since 72h] [--events <path>] [--json]"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now())) }

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("pr-flow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "GitHub repository, owner/name")
	since := fs.String("since", "72h", "window length, such as 72h or 3d")
	events := fs.String("events", "", "events file (default $FACTORY_HOME/state/events.jsonl)")
	asJSON := fs.Bool("json", false, "print one JSON object")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *repo == "" {
		fmt.Fprintln(stderr, "pr-flow: --repo is required and no other arguments are taken;", usage)
		return 2
	}
	window, err := prflow.ParseSince(*since)
	if err != nil {
		fmt.Fprintln(stderr, "pr-flow:", err)
		return 2
	}
	f, err := prflow.OpenEvents(*events)
	if err != nil {
		fmt.Fprintln(stderr, "pr-flow:", err)
		return 2
	}
	rep, err := prflow.Run(prflow.Options{Repo: *repo, Since: window, Now: now, Events: f})
	if cerr := f.Close(); cerr != nil && err == nil {
		err = fmt.Errorf("close events file: %w", cerr)
	}
	if err != nil {
		fmt.Fprintln(stderr, "pr-flow:", err)
		return 2
	}
	out := rep.Text()
	if *asJSON {
		if out, err = rep.JSON(); err != nil {
			fmt.Fprintln(stderr, "pr-flow:", err)
			return 2
		}
	}
	fmt.Fprint(stdout, out)
	return 0
}
