package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/hm"
)

// The planner commands: `planner start|stop|wake|status|hook`, the owner queue `ask add|list|answer|withdraw` and the
// morning `digest` (docs/hivemill-planner.md).

// plannerMay is what the planner session may run: goals and tasks, run status, the report, health and its side of the
// owner queue. Never a dispatch, a merge, the config or an answer to its own question.
func plannerMay(args []string) bool {
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	switch args[0] {
	case "goal", "task", "report", "health":
		return true
	case "run":
		return sub == "status"
	case "ask":
		return sub == "add" || sub == "list" || sub == "withdraw"
	}
	return false
}

// cmdPlannerHook is the planner's PreToolUse hook. It needs no DB and fails closed: exit 2 blocks the tool call.
func cmdPlannerHook(stdin io.Reader, stderr io.Writer) int {
	uh, _ := os.UserHomeDir()
	if why := hm.PlannerHook(stdin, uh); why != "" {
		fmt.Fprintln(stderr, "hivemill planner hook:", why)
		return 2
	}
	return 0
}

func cmdPlanner(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	usage := "usage: hivemill planner start|stop|status | planner wake [REASON]"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	now := time.Now()
	var err error
	switch args[0] {
	case "start":
		err = r.PlannerStart(now)
	case "stop":
		err = r.PlannerStop()
	case "status":
		err = r.PlannerStatus(now)
	case "wake":
		err = r.PlannerWake(now, strings.Join(args[1:], " "))
		if err == nil {
			fmt.Fprintln(stdout, "planner woken")
		}
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

func cmdAsk(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	usage := "usage: hivemill ask add [--task T-1234|--goal G-001|--project ID] TEXT | ask list [--all] | ask answer N TEXT | ask withdraw N [WHY]"
	s := sqliteOf(r, stderr)
	if s == nil {
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "add":
		fs := newFlags("ask add", stderr)
		task := fs.String("task", "", "the task the question is about")
		goal := fs.String("goal", "", "the goal the question is about")
		project := fs.String("project", "", "the project the question is about")
		if fs.Parse(args[1:]) != nil || fs.NArg() == 0 || (*task != "" && !taskRe.MatchString(*task)) || (*goal != "" && !goalRe.MatchString(*goal)) {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		id, err := r.AskAdd(hm.Ask{Task: strings.ToUpper(*task), Goal: strings.ToUpper(*goal), Project: *project, Question: strings.Join(fs.Args(), " ")})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "question #%d open\n", id)
	case "list":
		fs := newFlags("ask list", stderr)
		all := fs.Bool("all", false, "answered and withdrawn questions too")
		if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		st := "open"
		if *all {
			st = ""
		}
		asks, err := s.Asks(st)
		if err != nil {
			return fail(stderr, err)
		}
		for _, a := range asks {
			fmt.Fprintln(stdout, hm.AskLine(a, time.Now()))
		}
		if len(asks) == 0 {
			fmt.Fprintln(stdout, "no open owner questions")
		}
	case "answer", "withdraw":
		if len(args) < 2 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
		if err != nil || (args[0] == "answer" && len(args) < 3) {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		if err := r.AskClose(id, args[0] == "answer", strings.Join(args[2:], " ")); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "question #%d %s\n", id, map[bool]string{true: "answered", false: "withdrawn"}[args[0] == "answer"])
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return 0
}

func cmdDigest(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("digest", stderr)
	date := fs.String("date", "", "the day to summarise, YYYY-MM-DD (default yesterday)")
	asJSON := fs.Bool("json", false, "print JSON")
	send := fs.Bool("send", false, "also write state/hivemill/digests/<date>.md and open one owner alert feed row for it")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: hivemill digest [--date YYYY-MM-DD] [--json] [--send]")
		return 2
	}
	now := time.Now()
	day := now.AddDate(0, 0, -1)
	if *date != "" {
		d, err := time.ParseInLocation("2006-01-02", *date, time.Now().Location())
		if err != nil {
			fmt.Fprintln(stderr, "hivemill: --date:", err)
			return 2
		}
		day = d
	}
	d, err := r.BuildDigest(day)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(d, "", " ")
		fmt.Fprintf(stdout, "%s\n", b)
	} else {
		fmt.Fprint(stdout, d.Markdown(now))
	}
	if *send {
		p, err := r.SendDigest(d, now)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stderr, "digest written to %s\n", p)
	}
	return 0
}
