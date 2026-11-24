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

  hm-spend report [--hours N] [--since T] [--until T] [--root DIR] [--top N] [--merged-prs N] [--json] [--check]
      Sum the Claude Code transcripts of the last N hours (default 24): cost by model, top sessions, and findings
      (Fable use, big contexts, chatter, cache writes), and cost by role split into spawn, polling and work.
      --since/--until T (RFC3339 or YYYY-MM-DD, a date is 00:00 UTC) set the window; --hours N alone is the last N hours,
      with --until it is the N hours before it, with --since the N hours after it. Default: the last 24 hours.
      "Before/after metrics": median start-up context (first request of each session) and tool-output tokens per
      session (median, p90; tool_result bytes / 4 as the token estimate) per role, and requests per merged PR.
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
	sinceS := fl.String("since", "", "")
	untilS := fl.String("until", "", "")
	asJSON := fl.Bool("json", false, "")
	check := fl.Bool("check", false, "")
	if fl.Parse(args) != nil || fl.NArg() > 0 || *hours <= 0 || *hours > 1_000_000 || *top < 1 || *top > 1000 || *merged < 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	hoursSet := false
	fl.Visit(func(f *flag.Flag) { hoursSet = hoursSet || f.Name == "hours" })
	since, until, err := Window(time.Now(), *sinceS, *untilS, *hours, hoursSet)
	if err != nil {
		fmt.Fprintln(stderr, "hm-spend:", err)
		return ExitUsage
	}
	rep, err := Scan(*root, since, until, *top)
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
		printReport(stdout, rep, until.Sub(since).Hours())
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
	printMetrics(w, r)
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

// Window resolves the report window. until defaults to now; since to until - hours (or, with only --since and an
// explicit hours, until = since + hours).
func Window(now time.Time, sinceS, untilS string, hours float64, hoursSet bool) (since, until time.Time, err error) {
	d := time.Duration(hours * float64(time.Hour))
	until = now
	if untilS != "" {
		if until, err = ParseTime(untilS); err != nil {
			return since, until, fmt.Errorf("--until: want RFC3339 or YYYY-MM-DD, got %q", untilS)
		}
	}
	switch {
	case sinceS != "":
		if since, err = ParseTime(sinceS); err != nil {
			return since, until, fmt.Errorf("--since: want RFC3339 or YYYY-MM-DD, got %q", sinceS)
		}
		if untilS == "" && hoursSet {
			until = since.Add(d)
		}
	default:
		since = until.Add(-d)
	}
	if !since.Before(until) {
		return since, until, errors.New("--since must be before --until")
	}
	return since, until, nil
}

func printMetrics(w io.Writer, r *Report) {
	m := r.Metrics
	if m == nil {
		return
	}
	fmt.Fprintln(w, "\nBefore/after metrics (tool-output tokens estimated as tool_result bytes / 4):")
	fmt.Fprintf(w, "  %-15s %8s %8s %11s %11s %11s\n", "role", "sessions", "requests", "start ctx", "tool med", "tool p90")
	row := func(name string, x RoleMetrics) {
		fmt.Fprintf(w, "  %-15s %8d %8d %10dk %10dk %10dk\n", name, x.Sessions, x.Requests, x.MedianStartContext/1000,
			x.ToolOutputMedian/1000, x.ToolOutputP90/1000)
	}
	for _, b := range m.ByRole {
		row(b.Role, b)
	}
	row("all", m.Total)
	if m.Total.RequestsPerMergedPR == nil {
		fmt.Fprintln(w, "  requests per merged PR: n/a")
	} else {
		fmt.Fprintf(w, "  requests per merged PR: %.1f (%d requests / %d merged PRs)\n", *m.Total.RequestsPerMergedPR, m.Total.Requests, r.MergedPRs)
	}
}
