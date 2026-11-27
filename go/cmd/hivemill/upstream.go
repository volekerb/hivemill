package main

import (
	"fmt"
	"io"
	"strings"

	"factory/go/internal/hm"
)

const upstreamUsage = "usage: hivemill upstream add --title TEXT (--spec TEXT|--spec-file F) (--acceptance TEXT|--acceptance-file F) " +
	"[--priority N] | upstream status T-1234"

// cmdUpstream is `hivemill upstream add|status`: factory tasks of a standalone runner, filed on the factory runner.
func cmdUpstream(r *hm.Runner, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if r.Env.Actor != "" && r.Env.Actor != "owner" && !isPlanner(r) {
		return fail(stderr, fmt.Errorf("%w: only the planner or the owner may use upstream", hm.ErrRefused))
	}
	switch {
	case len(args) == 2 && args[0] == "status" && taskRe.MatchString(args[1]):
		out, err := r.UpstreamStatus(strings.ToUpper(args[1]))
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprint(stdout, out)
		return 0
	case len(args) >= 1 && args[0] == "add":
	default:
		fmt.Fprintln(stderr, upstreamUsage)
		return 2
	}
	fs := newFlags("upstream add", stderr)
	title := fs.String("title", "", "task title")
	spec := fs.String("spec", "", "what to build")
	specFile := fs.String("spec-file", "", "read the spec from a file (- = stdin)")
	acc := fs.String("acceptance", "", "numbered acceptance criteria, each naming its proof")
	accFile := fs.String("acceptance-file", "", "read the acceptance criteria from a file (- = stdin)")
	prio := fs.Int("priority", -1, "task priority on the factory runner (lower dispatches first)")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 || strings.TrimSpace(*title) == "" {
		fmt.Fprintln(stderr, upstreamUsage)
		return 2
	}
	// the text leaves this machine: the planner, whose Read is limited, must not send a file by naming it
	if isPlanner(r) && (*specFile != "" && *specFile != "-" || *accFile != "" && *accFile != "-") {
		return fail(stderr, fmt.Errorf("%w: the planner gives the text itself, or - for stdin", hm.ErrRefused))
	}
	if *specFile == "-" && *accFile == "-" {
		return fail(stderr, fmt.Errorf("only one of --spec-file and --acceptance-file can be stdin"))
	}
	sp, err := text(*spec, *specFile, stdin)
	if err != nil {
		return fail(stderr, err)
	}
	a, err := text(*acc, *accFile, stdin)
	if err != nil {
		return fail(stderr, err)
	}
	id, err := r.UpstreamAdd(hm.UpstreamTask{Title: *title, Spec: sp, Acceptance: a, Priority: *prio})
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, id)
	return 0
}

// logUpstreamFiled is the factory runner's side of `upstream add`. The task stands when the log fails.
func logUpstreamFiled(r *hm.Runner, s *hm.SQLite, id, goal, from string, stderr io.Writer) {
	e := hm.Event{TaskID: id, GoalID: goal, Data: map[string]any{"from": from}}
	if g, err := s.Goal(goal); err == nil {
		e.Project = g.Project
	}
	if err := r.Log.Emit("upstream.filed", nil, e); err != nil {
		fmt.Fprintln(stderr, "hivemill: log upstream.filed:", err)
	}
}
