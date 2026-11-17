package spend

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Exit codes, as in ADR 0007 section F.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitRefused = 3 // report --check found a high finding; limits --check found missing limits
)

const usage = `hm-spend: where Claude tokens go on this machine, and context limits for its agents (docs/token-discipline.md).

  hm-spend report [--hours N] [--root DIR] [--top N] [--merged-prs N] [--json] [--check]
      Sum the Claude Code transcripts of the last N hours (default 24): cost by model, top sessions, and findings
      (Fable use, big contexts, chatter, cache writes), and cost by role split into spawn, polling and work.
      --merged-prs N adds $ per merged PR (total and per role). --check exits 3 when a high finding exists.

  hm-spend limits [--settings FILE] [--boss-settings FILE] [--apply] [--check] [--json]
      Show (default), apply, or check the worker limits in FILE (default ~/.claude/settings.json) and the boss
      exception in --boss-settings (default ~/.claude/boss-settings.json; start the boss with
      claude --settings <that file>). --check exits 3 when a limit is missing.
`

// Main runs hm-spend with args (without the program name) and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	home, _ := os.UserHomeDir()
	switch args[0] {
	case "report":
		return report(args[1:], home, stdout, stderr)
	case "limits":
		return limits(args[1:], home, stdout, stderr)
	}
	fmt.Fprintf(stderr, "hm-spend: unknown command %q\n%s", args[0], usage)
	return ExitUsage
}

func report(args []string, home string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("report", flag.ContinueOnError)
	fl.SetOutput(stderr)
	hours := fl.Float64("hours", 24, "")
	root := fl.String("root", filepath.Join(home, ".claude", "projects"), "")
	top := fl.Int("top", 10, "")
	merged := fl.Int("merged-prs", 0, "")
	asJSON := fl.Bool("json", false, "")
	check := fl.Bool("check", false, "")
	if fl.Parse(args) != nil || fl.NArg() > 0 || *hours <= 0 || *hours > 24*90 || *top < 1 || *top > 1000 || *merged < 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	now := time.Now()
	rep, err := Scan(*root, now.Add(-time.Duration(*hours*float64(time.Hour))), now, *top)
	if err != nil {
		fmt.Fprintln(stderr, "hm-spend:", err)
		return ExitError
	}
	rep.SetMergedPRs(*merged)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printReport(stdout, rep, *hours)
	}
	if *check {
		for _, f := range rep.Findings {
			if f.Level == "high" {
				return ExitRefused
			}
		}
	}
	return ExitOK
}

func printReport(w io.Writer, r *Report, hours float64) {
	t := r.Total
	fmt.Fprintf(w, "Last %gh: $%.2f, %d requests, %d sessions (API prices; on a subscription this is the share of your limits)\n",
		hours, t.Cost, t.Requests, r.Sessions)
	if t.Requests == 0 {
		fmt.Fprintf(w, "No requests found under %s.\n", r.Root)
		return
	}
	fmt.Fprintf(w, "  average context %dk, cache writes $%.2f (%.0f%%), tiny turns %.0f%%\n\n", t.AvgContext/1000,
		t.CacheWriteCost, pct(t.CacheWriteCost, t.Cost), pct(float64(t.Tiny), float64(t.Requests)))
	fmt.Fprintln(w, "By model:")
	for _, m := range r.ByModel {
		fmt.Fprintf(w, "  %-22s $%9.2f  %6d req  avg ctx %4dk\n", m.Key, m.Cost, m.Requests, m.AvgContext/1000)
	}
	fmt.Fprintln(w, "\nBy role:")
	fmt.Fprintf(w, "  %-15s %8s %8s %10s %10s %10s %10s %10s\n", "role", "sessions", "requests", "$", "spawn $", "polling $", "work $", "start ctx")
	for _, b := range r.ByRole {
		fmt.Fprintf(w, "  %-15s %8d %8d %10.2f %10.2f %10.2f %10.2f %9dk\n", b.Role, b.Sessions, b.Requests, b.Cost, b.SpawnCost,
			b.PollingCost, b.WorkCost, b.StartContext/1000)
	}
	fmt.Fprintf(w, "  buckets: spawn $%.2f, polling $%.2f, work $%.2f\n", t.SpawnCost, t.PollingCost, t.WorkCost)
	if r.PerMergedPR == nil {
		fmt.Fprintln(w, "merged PRs: not given")
	} else {
		fmt.Fprintf(w, "merged PRs: %d, $%.2f per merged PR\n", r.MergedPRs, *r.PerMergedPR)
		for _, b := range r.ByRole {
			fmt.Fprintf(w, "    %-15s $%.2f per merged PR\n", b.Role, *b.PerMergedPR)
		}
	}
	fmt.Fprintln(w, "\nTop sessions:")
	for _, s := range r.Top {
		fmt.Fprintf(w, "  %-8.8s $%8.2f  %5d req  avg ctx %4dk  max %4dk  start %3dk  tiny %3.0f%%  %s  %s\n", s.Key, s.Cost, s.Requests,
			s.AvgContext/1000, s.ContextMax/1000, s.StartContext/1000, pct(float64(s.Tiny), float64(s.Requests)), s.Model, s.Project)
	}
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "\nNo findings.")
		return
	}
	fmt.Fprintln(w, "\nFindings:")
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  [%s] %s: %s\n      fix: %s\n", f.Level, f.Code, f.Text, f.Advice)
	}
}

func limits(args []string, home string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("limits", flag.ContinueOnError)
	fl.SetOutput(stderr)
	settings := fl.String("settings", filepath.Join(home, ".claude", "settings.json"), "")
	boss := fl.String("boss-settings", filepath.Join(home, ".claude", "boss-settings.json"), "")
	apply := fl.Bool("apply", false, "")
	check := fl.Bool("check", false, "")
	asJSON := fl.Bool("json", false, "")
	if fl.Parse(args) != nil || fl.NArg() > 0 || (*apply && *check) || *settings == "" || *boss == "" {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	if filepath.Clean(*settings) == filepath.Clean(*boss) {
		fmt.Fprintln(stderr, "hm-spend: --settings and --boss-settings must be different files")
		return ExitUsage
	}
	run := Plan
	if *apply {
		run = Apply
	}
	var all []Change
	for _, f := range []struct {
		path string
		env  map[string]string
	}{{*settings, WorkerEnv}, {*boss, BossEnv}} {
		ch, err := run(f.path, f.env)
		if err != nil {
			if errors.Is(err, ErrNotObject) {
				fmt.Fprintln(stderr, "hm-spend: refusing to touch", err)
			} else {
				fmt.Fprintln(stderr, "hm-spend:", err)
			}
			return ExitError
		}
		all = append(all, ch...)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"applied": *apply, "changes": all})
	} else {
		verb := "would set"
		if *apply {
			verb = "set"
		}
		for _, c := range all {
			fmt.Fprintf(stdout, "%s %s %s=%s (was %q)\n", verb, c.File, c.Key, c.New, c.Old)
		}
		if len(all) == 0 {
			fmt.Fprintln(stdout, "limits already in place")
		} else if !*apply {
			fmt.Fprintln(stdout, "run again with --apply to write; new sessions pick it up, running ones at their next start")
		} else {
			fmt.Fprintf(stdout, "start the boss with: claude --settings %s\n", *boss)
		}
	}
	if *check && len(all) > 0 {
		return ExitRefused
	}
	return ExitOK
}
