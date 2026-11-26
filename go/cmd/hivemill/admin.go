package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"factory/go/internal/hm"
)

// The standalone commands: `init` makes a factory home without the old CLI, `project`, `goal` and `task` file and
// change the rows the runner reads, `launchd daemon` renders the daemon's LaunchAgent.

var (
	repoRe    = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	projIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	goalRe    = regexp.MustCompile(`^(?i)G-\d+$`)
	taskState = map[string]bool{"draft": true, "todo": true, "in_progress": true, "in_review": true, "blocked": true, "done": true,
		"cancelled": true}
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, " && ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// text is the value of --X, or the content of --X-file (`-` = stdin).
func text(val, file string, stdin io.Reader) (string, error) {
	if file == "" {
		return val, nil
	}
	if val != "" {
		return "", fmt.Errorf("give the text or the file, not both")
	}
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(file)
	}
	return string(b), err
}

func absHome(h string) (string, error) {
	if h == "" {
		return hm.LoadEnv().Home, nil
	}
	if strings.HasPrefix(h, "~/") {
		uh, _ := os.UserHomeDir()
		h = filepath.Join(uh, h[2:])
	}
	return filepath.Abs(h)
}

// cmdInit is `hivemill init [--home DIR] [--noreply-email E] [--scrub-terms FILE]`. It needs no existing DB.
func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("init", stderr)
	home := fs.String("home", "", "factory home (default FACTORY_HOME, else ~/factory)")
	email := fs.String("noreply-email", "", "factory config noreply_email: the commit identity sessions use and the merge checks")
	terms := fs.String("scrub-terms", "", "factory config scrub_terms_file")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: hivemill init [--home DIR] [--noreply-email EMAIL] [--scrub-terms FILE]")
		return 2
	}
	h, err := absHome(*home)
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 1
	}
	notes, err := hm.InitHome(h, hm.InitOptions{NoreplyEmail: *email, ScrubTermsFile: *terms})
	for _, n := range notes {
		fmt.Fprintln(stdout, n)
	}
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 1
	}
	for _, n := range hm.TrustNotes(hm.LoadEnv().UserHome, h) { // read-only: the owner accepts the prompt once
		fmt.Fprintln(stdout, n)
	}
	fmt.Fprintf(stdout, "factory home %s ready; use FACTORY_HOME=%s\n", h, h)
	return 0
}

// cmdLaunchd is `hivemill launchd daemon|digest|health [--home DIR] [--path PATH] [--out FILE]`: the pass loop
// (KeepAlive), the morning digest, or the dead-man check (`hivemill health --alert` every 5 minutes).
func cmdLaunchd(args []string, stdout, stderr io.Writer) int {
	usage := "usage: hivemill launchd daemon|digest|health [--home DIR] [--path PATH] [--out FILE]"
	if len(args) == 0 || (args[0] != "daemon" && args[0] != "digest" && args[0] != "health") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := newFlags("launchd", stderr)
	home := fs.String("home", "", "factory home (default FACTORY_HOME, else ~/factory)")
	path := fs.String("path", "", "PATH of the daemon (default ~/.local/bin, Homebrew and the system dirs)")
	out := fs.String("out", "", "write the plist here instead of stdout")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	h, err := absHome(*home)
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 1
	}
	e := hm.LoadEnv()
	e.Home = h
	if *path == "" {
		*path = hm.DefaultLaunchdPath(e.UserHome)
	}
	p, err := hm.RenderPlist(e, args[0], hm.PlistInput{FactoryHome: h, Path: *path, LogDir: filepath.Join(h, "logs")})
	if err == nil && *out != "" {
		err = os.WriteFile(*out, []byte(p), 0o644)
	} else if err == nil {
		fmt.Fprint(stdout, p)
	}
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 1
	}
	return 0
}

func sqliteOf(r *hm.Runner, stderr io.Writer) *hm.SQLite {
	s, ok := r.Store.(*hm.SQLite)
	if !ok {
		fmt.Fprintln(stderr, "hivemill: this command needs the SQLite store")
	}
	return s
}

// cmdProject is `hivemill project add ID --repo OWNER/NAME --path DIR [--gate CMD]... [--base BRANCH] | project list |
// project profile ID ...`. The gate command is what the gate's tests check runs in the task clone (and what a test:
// proof can name); the profile holds the project's own tier settings.
func cmdProject(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	usage := "usage: hivemill project add ID --repo OWNER/NAME --path DIR [--gate CMD]... [--base BRANCH] [--name NAME] | project list | " +
		"project profile ID [--sensitive GLOB]... [--tier-rules FILE] [--clear] " +
		"[--post-merge-cmd CMD] [--post-merge-timeout-s N]"
	s := sqliteOf(r, stderr)
	if s == nil {
		return 1
	}
	switch {
	case len(args) == 1 && args[0] == "list":
		ps, err := s.Projects()
		if err != nil {
			fmt.Fprintln(stderr, "hivemill:", err)
			return 1
		}
		for _, p := range ps {
			fmt.Fprintf(stdout, "%s %s %s base %s gate %q\n", p.ID, p.GHRepo, p.RepoPath, p.Base, p.GateCmd)
		}
		return 0
	case len(args) >= 1 && args[0] == "profile":
		return cmdProfile(r, args[1:], stdout, stderr)
	case len(args) >= 2 && args[0] == "add":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	id := args[1]
	fs := newFlags("project add", stderr)
	repo := fs.String("repo", "", "GitHub repo OWNER/NAME")
	path := fs.String("path", "", "the project's checkout (its origin is the GitHub remote)")
	base := fs.String("base", "main", "base branch")
	name := fs.String("name", "", "display name (default the id)")
	var gates multi
	fs.Var(&gates, "gate", "local gate command (repeat for more; they run in order, joined with &&)")
	if fs.Parse(args[2:]) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch {
	case !projIDRe.MatchString(id):
		fmt.Fprintf(stderr, "hivemill: project id %q: use lower-case letters, digits, '.', '_' or '-'\n", id)
		return 2
	case !repoRe.MatchString(*repo):
		fmt.Fprintf(stderr, "hivemill: --repo %q: want OWNER/NAME\n", *repo)
		return 2
	case *path == "":
		fmt.Fprintln(stderr, "hivemill: --path is required")
		return 2
	}
	p, err := filepath.Abs(*path)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(p); err == nil && !fi.IsDir() {
			err = fmt.Errorf("%s is not a directory", p)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "hivemill: --path:", err)
		return 2
	}
	if err := s.AddProject(hm.Project{ID: id, Name: *name, RepoPath: p, GHRepo: *repo, Base: *base, GateCmd: gates.String()}); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "project %s: %s at %s (not in `hivemill config projects` until you add it there)\n", id, *repo, p)
	return 0
}

// cmdGoal is `hivemill goal new|list|show|close`.
func cmdGoal(r *hm.Runner, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := "usage: hivemill goal new --project ID --title TEXT [--intent TEXT] [--criteria TEXT|--criteria-file F] [--priority N] | " +
		"goal list [--status S] | goal show G-001 | goal close G-001 [--cancel] [--force] | goal status G-001 active|blocked|done|cancelled"
	s := sqliteOf(r, stderr)
	if s == nil {
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "new":
		fs := newFlags("goal new", stderr)
		project := fs.String("project", "", "project id")
		title := fs.String("title", "", "goal title")
		intent := fs.String("intent", "", "why")
		crit := fs.String("criteria", "", "success criteria (cmd: proofs run on main after its last task merges)")
		critFile := fs.String("criteria-file", "", "read the success criteria from a file (- = stdin)")
		prio := fs.Int("priority", 2, "goal priority (lower dispatches first)")
		if fs.Parse(args[1:]) != nil || fs.NArg() > 0 || *project == "" || strings.TrimSpace(*title) == "" {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		c, err := text(*crit, *critFile, stdin)
		if err != nil {
			return fail(stderr, err)
		}
		id, err := s.NewGoal(*project, strings.TrimSpace(*title), *intent, c, *prio)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s active in %s\n", id, *project)
	case "list":
		fs := newFlags("goal list", stderr)
		status := fs.String("status", "", "only goals in this state")
		if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		gs, err := s.Goals(*status)
		if err != nil {
			return fail(stderr, err)
		}
		for _, g := range gs {
			fmt.Fprintf(stdout, "%s %-9s p%d %s %s\n", g.ID, g.Status, g.Priority, g.Project, g.Title)
		}
	case "show":
		if len(args) != 2 || !goalRe.MatchString(args[1]) {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		g, err := s.Goal(strings.ToUpper(args[1]))
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s %s (project %s, priority %d)\n%s\n", g.ID, g.Status, g.Project, g.Priority, g.Title)
		if g.Intent != "" {
			fmt.Fprintf(stdout, "\nIntent:\n%s\n", g.Intent)
		}
		if g.Criteria != "" {
			fmt.Fprintf(stdout, "\nSuccess criteria:\n%s\n", g.Criteria)
		}
		ts, err := s.Tasks(g.ID, "")
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "\nTasks (%d):\n", len(ts))
		for _, t := range ts {
			fmt.Fprintf(stdout, "  %s %-11s %s\n", t.ID, t.Status, t.Title)
		}
	case "close":
		fs := newFlags("goal close", stderr)
		cancel := fs.Bool("cancel", false, "mark it cancelled instead of done")
		force := fs.Bool("force", false, "close it even with open tasks")
		if len(args) < 2 || !goalRe.MatchString(args[1]) || fs.Parse(args[2:]) != nil || fs.NArg() > 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		id := strings.ToUpper(args[1])
		n, _, err := s.GoalOpenTasks(id)
		if err != nil {
			return fail(stderr, err)
		}
		if _, err := s.Goal(id); err != nil {
			return fail(stderr, err)
		}
		if n > 0 && !*force {
			return fail(stderr, fmt.Errorf("%w: %s has %d open tasks (cancel them, or --force)", hm.ErrRefused, id, n))
		}
		st := "done"
		if *cancel {
			st = "cancelled"
		}
		if err := s.SetGoalStatus(id, st); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s %s\n", id, st)
	case "status":
		if len(args) != 3 || !goalRe.MatchString(args[1]) {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		id, st := strings.ToUpper(args[1]), strings.ToLower(args[2])
		switch st {
		case "active", "blocked", "done", "cancelled":
		default:
			fmt.Fprintf(stderr, "hivemill: unknown goal status %q (active, blocked, done, cancelled)\n", st)
			return 2
		}
		if _, err := s.Goal(id); err != nil {
			if strings.HasPrefix(err.Error(), "no goal ") {
				fmt.Fprintf(stderr, "hivemill: unknown goal %s\n", id)
				return 2
			}
			return fail(stderr, err)
		}
		if st == "done" || st == "cancelled" {
			n, _, err := s.GoalOpenTasks(id)
			if err != nil {
				return fail(stderr, err)
			}
			if n > 0 {
				return fail(stderr, fmt.Errorf("%w: %s has %d open tasks (cancel them, or use goal close --force)", hm.ErrRefused, id, n))
			}
		}
		if err := s.SetGoalStatus(id, st); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s %s\n", id, st)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return 0
}

const taskUsage = "usage: hivemill task lint|show|cancel T-1234 | task lint --all | task list [--goal G] [--status S] | task list --startable|--blocked | " +
	"task add --goal G --title TEXT (--spec TEXT|--spec-file F) (--acceptance TEXT|--acceptance-file F) [--priority N] [--depends T-1,T-2] [--refile-of T-1234] [--draft] | " +
	"task update T-1234 [--title] [--spec|--spec-file] [--acceptance|--acceptance-file] [--status S] [--priority N] [--depends T-1,T-2]"

// lintReport prints the parsed criteria and the spec gate result; it returns the problems.
func lintReport(w io.Writer, id, acceptance string) []string {
	for _, c := range hm.Criteria(acceptance) {
		p := c.Proof
		if p == "" {
			p = c.Text
		}
		if c.Expect != "" {
			p += " => " + c.Expect
		}
		fmt.Fprintf(w, "  %d. %s: %s\n", c.ID, or(c.Kind, "-"), trunc(p, 100))
	}
	probs := hm.FilingProblems(acceptance)
	if len(probs) > 0 {
		fmt.Fprintf(w, "%s: spec gate FAIL\n  - %s\n", id, strings.Join(probs, "\n  - "))
	} else {
		fmt.Fprintf(w, "%s: spec gate PASS (%d criteria)\n", id, len(hm.Criteria(acceptance)))
	}
	return probs
}

// lintAll lists the todo and draft tasks that fail the spec gate, one line per problem; exit 1 when there is one.
func lintAll(r *hm.Runner, stdout, stderr io.Writer) int {
	s := sqliteOf(r, stderr)
	if s == nil {
		return 1
	}
	checked, failed := 0, 0
	for _, status := range []string{"todo", "draft"} {
		ts, err := s.Tasks("", status)
		if err != nil {
			return fail(stderr, err)
		}
		for _, t := range ts {
			checked++
			probs := hm.FilingProblems(t.Acceptance)
			if len(probs) > 0 {
				failed++
			}
			for _, p := range probs {
				fmt.Fprintf(stdout, "%s %s: %s\n", t.ID, t.Status, p)
			}
		}
	}
	fmt.Fprintf(stdout, "%d of %d todo and draft tasks fail the spec gate\n", failed, checked)
	if failed > 0 {
		return 1
	}
	return 0
}

func depsJSON(v string) (string, error) {
	deps := []string{}
	for _, d := range strings.Split(v, ",") {
		if d = strings.ToUpper(strings.TrimSpace(d)); d == "" {
			continue
		}
		if !taskRe.MatchString(d) {
			return "", fmt.Errorf("--depends: %q is not a task id", d)
		}
		deps = append(deps, d)
	}
	b, _ := json.Marshal(deps)
	return string(b), nil
}

// cmdTask is `hivemill task lint|add|list|show|update|cancel`.
func cmdTask(r *hm.Runner, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, taskUsage)
		return 2
	}
	idArg := func() (string, bool) {
		if len(args) < 2 || !taskRe.MatchString(args[1]) {
			return "", false
		}
		return strings.ToUpper(args[1]), true
	}
	switch args[0] {
	case "lint":
		if len(args) == 2 && args[1] == "--all" {
			return lintAll(r, stdout, stderr)
		}
		id, ok := idArg()
		if !ok || len(args) != 2 {
			fmt.Fprintln(stderr, "usage: hivemill task lint T-1234|--all")
			return 2
		}
		t, e := r.Store.Task(id)
		if e != nil {
			fmt.Fprintln(stderr, "hivemill:", e)
			return 2
		}
		if len(lintReport(stdout, t.ID, t.Acceptance)) > 0 {
			return 1
		}
		return 0
	}
	s := sqliteOf(r, stderr)
	if s == nil {
		return 1
	}
	switch args[0] {
	case "add":
		fs := newFlags("task add", stderr)
		goal := fs.String("goal", "", "goal id")
		title := fs.String("title", "", "task title")
		spec := fs.String("spec", "", "what to build")
		specFile := fs.String("spec-file", "", "read the spec from a file (- = stdin)")
		acc := fs.String("acceptance", "", "numbered acceptance criteria, each naming its proof")
		accFile := fs.String("acceptance-file", "", "read the acceptance criteria from a file (- = stdin)")
		prio := fs.Int("priority", 1, "task priority (lower dispatches first)")
		depends := fs.String("depends", "", "comma-separated task ids that must be done first")
		refileOf := fs.String("refile-of", "", "the cancelled or failed task this one files again: its dependents move to the new id")
		draft := fs.Bool("draft", false, "store it as status draft: never dispatched, criteria may still fail the spec gate")
		if fs.Parse(args[1:]) != nil || fs.NArg() > 0 || !goalRe.MatchString(*goal) || strings.TrimSpace(*title) == "" {
			fmt.Fprintln(stderr, taskUsage)
			return 2
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
		deps, err := depsJSON(*depends)
		if err != nil {
			return fail(stderr, err)
		}
		status := "todo"
		if *draft {
			status = "draft"
		} else if strings.TrimSpace(sp) == "" {
			return fail(stderr, fmt.Errorf("%w: a task needs a spec (or --draft)", hm.ErrRefused))
		}
		var lint strings.Builder
		if probs := lintReport(&lint, "new task", a); len(probs) > 0 && !*draft {
			fmt.Fprint(stderr, lint.String())
			return fail(stderr, fmt.Errorf("%w: the criteria fail the spec gate; fix them, or file it with --draft", hm.ErrRefused))
		}
		of := strings.ToUpper(strings.TrimSpace(*refileOf))
		if of != "" {
			if !taskRe.MatchString(of) {
				return fail(stderr, fmt.Errorf("--refile-of: %q is not a task id", of))
			}
			old, err := s.Task(of)
			if err != nil {
				return fail(stderr, fmt.Errorf("--refile-of: %w", err))
			}
			if g, err := s.Goal(strings.ToUpper(*goal)); err == nil && g.Project != old.Project {
				return fail(stderr, fmt.Errorf("%w: --refile-of %s is a task of another project", hm.ErrRefused, of))
			}
			if old.Status == "done" {
				return fail(stderr, fmt.Errorf("%w: --refile-of %s is done; a re-file replaces a cancelled or failed task", hm.ErrRefused, of))
			}
			if waitsOn(s, deps, of) {
				return fail(stderr, fmt.Errorf("%w: --depends leads to %s, which this task files again: it could never start", hm.ErrRefused, of))
			}
		}
		id, err := s.AddTask(strings.ToUpper(*goal), strings.TrimSpace(*title), sp, a, status, deps, *prio, of)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprint(stdout, strings.Replace(lint.String(), "new task", id, 1))
		fmt.Fprintf(stdout, "%s %s in %s\n", id, status, strings.ToUpper(*goal))
		repoint(r, stderr)
	case "list":
		fs := newFlags("task list", stderr)
		goal := fs.String("goal", "", "only tasks of this goal")
		status := fs.String("status", "", "only tasks in this state")
		startable := fs.Bool("startable", false, "the tasks a pass would start")
		blocked := fs.Bool("blocked", false, "the todo tasks a pass skips, with the reason")
		if fs.Parse(args[1:]) != nil || fs.NArg() > 0 || (*startable && *blocked) ||
			((*startable || *blocked) && (*goal != "" || *status != "")) {
			fmt.Fprintln(stderr, taskUsage)
			return 2
		}
		if *startable || *blocked {
			if err := r.ListQueue(stdout, *blocked); err != nil {
				return fail(stderr, err)
			}
			return 0
		}
		ts, err := s.Tasks(strings.ToUpper(*goal), *status)
		if err != nil {
			return fail(stderr, err)
		}
		for _, t := range ts {
			fmt.Fprintf(stdout, "%s %-11s %s %s %s\n", t.ID, t.Status, t.Goal, or(t.PRURL, "-"), t.Title)
		}
	case "show":
		id, ok := idArg()
		if !ok || len(args) != 2 {
			fmt.Fprintln(stderr, taskUsage)
			return 2
		}
		t, err := s.Task(id)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s %s (goal %s, project %s, priority %s, depends %s)\n%s\n", t.ID, t.Status, t.Goal, or(t.Project, "-"),
			prio(t), t.DependsOn, t.Title)
		if t.PRURL != "" {
			fmt.Fprintf(stdout, "PR %s tier %s\n", t.PRURL, or(t.Tier, "-"))
		}
		fmt.Fprintf(stdout, "\nSpec:\n%s\n\nAcceptance:\n%s\n\n", strings.TrimSpace(t.Spec), strings.TrimSpace(t.Acceptance))
		lintReport(stdout, t.ID, t.Acceptance)
		if run, _ := s.Run(t.ID); run != nil {
			fmt.Fprintf(stdout, "run: %s, round %d, %s\n", run.State, run.Round, or(run.PRURL, "no PR"))
		}
	case "update":
		id, ok := idArg()
		if !ok {
			fmt.Fprintln(stderr, taskUsage)
			return 2
		}
		fs := newFlags("task update", stderr)
		title := fs.String("title", "", "new title")
		spec := fs.String("spec", "", "new spec")
		specFile := fs.String("spec-file", "", "read the new spec from a file (- = stdin)")
		acc := fs.String("acceptance", "", "new acceptance criteria")
		accFile := fs.String("acceptance-file", "", "read the new acceptance criteria from a file (- = stdin)")
		status := fs.String("status", "", "new status (draft, todo, blocked, done, cancelled, ...)")
		pr := fs.Int("priority", -1, "new priority")
		depends := fs.String("depends", "\x00", "new comma-separated dependencies (empty = none)")
		if fs.Parse(args[2:]) != nil || fs.NArg() > 0 {
			fmt.Fprintln(stderr, taskUsage)
			return 2
		}
		t, err := s.Task(id)
		if err != nil {
			return fail(stderr, err)
		}
		if isPlanner(r) {
			// the gate re-runs the criteria stored in the row: the planner never changes a task while it runs
			if run, _ := s.Run(id); run != nil && hm.Active(run.State) {
				return fail(stderr, fmt.Errorf("%w: %s has an active run (%s); the planner changes no running task", hm.ErrRefused, id, run.State))
			}
			if *status == "done" || *status == "in_progress" || *status == "in_review" {
				return fail(stderr, fmt.Errorf("%w: only the runner sets a task %s", hm.ErrRefused, *status))
			}
		}
		set := map[string]any{}
		if *title != "" {
			set["title"] = strings.TrimSpace(*title)
		}
		if *spec != "" || *specFile != "" {
			v, err := text(*spec, *specFile, stdin)
			if err != nil {
				return fail(stderr, err)
			}
			set["spec"] = v
		}
		if *acc != "" || *accFile != "" {
			v, err := text(*acc, *accFile, stdin)
			if err != nil {
				return fail(stderr, err)
			}
			set["acceptance"] = v
		}
		if *status != "" {
			if !taskState[*status] {
				return fail(stderr, fmt.Errorf("--status %q: want one of draft, todo, in_progress, in_review, blocked, done, cancelled", *status))
			}
			if run, _ := s.Run(id); run != nil && hm.Active(run.State) {
				return fail(stderr, fmt.Errorf("%w: %s has an active run (%s); `hivemill run stop %s` first", hm.ErrRefused, id, run.State, id))
			}
			set["status"] = *status
		}
		if *pr >= 0 {
			set["priority"] = *pr
		}
		if *depends != "\x00" {
			d, err := depsJSON(*depends)
			if err != nil {
				return fail(stderr, err)
			}
			set["depends_on"] = d
		}
		if len(set) == 0 {
			fmt.Fprintln(stderr, taskUsage)
			return 2
		}
		// The spec gate holds for every task that can be dispatched: changing its criteria, or promoting a draft, re-lints.
		newStatus, newAcc := t.Status, t.Acceptance
		if v, ok := set["status"]; ok {
			newStatus = v.(string)
		}
		if v, ok := set["acceptance"]; ok {
			newAcc = v.(string)
		}
		_, accChanged := set["acceptance"]
		_, stChanged := set["status"]
		if (accChanged || stChanged) && newStatus != "draft" && newStatus != "done" && newStatus != "cancelled" {
			var lint strings.Builder
			probs := lintReport(&lint, id, newAcc)
			filed := t.Status == "todo" || t.Status == "blocked" || t.Status == "in_progress" || t.Status == "in_review"
			if !accChanged && filed { // a status change of a filed task: the dispatch gate
				probs = hm.LintProblems(newAcc)
			}
			if len(probs) > 0 {
				fmt.Fprint(stderr, lint.String())
				return fail(stderr, fmt.Errorf("%w: %s would be %s with criteria that fail the spec gate", hm.ErrRefused, id, newStatus))
			}
		}
		if err := s.UpdateTask(id, set); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s updated (%s), status %s\n", id, strings.Join(sortedKeys(set), ", "), newStatus)
		repoint(r, stderr)
	case "cancel":
		id, ok := idArg()
		if !ok || len(args) != 2 {
			fmt.Fprintln(stderr, taskUsage)
			return 2
		}
		if _, err := s.Task(id); err != nil {
			return fail(stderr, err)
		}
		if run, _ := s.Run(id); run != nil && hm.Active(run.State) {
			return fail(stderr, fmt.Errorf("%w: %s has an active run (%s); `hivemill run stop %s` first", hm.ErrRefused, id, run.State, id))
		}
		if err := s.UpdateTask(id, map[string]any{"status": "cancelled"}); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s cancelled\n", id)
		repoint(r, stderr)
	default:
		fmt.Fprintln(stderr, taskUsage)
		return 2
	}
	return 0
}

// waitsOn reports whether the depends list leads to the task `on`, directly or through other tasks.
func waitsOn(s *hm.SQLite, depends, on string) bool {
	var queue []string
	_ = json.Unmarshal([]byte(depends), &queue)
	seen := map[string]bool{}
	for ; len(queue) > 0; queue = queue[1:] {
		id := queue[0]
		if id == on {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if t, err := s.Task(id); err == nil {
			var next []string
			_ = json.Unmarshal([]byte(t.DependsOn), &next)
			queue = append(queue, next...)
		}
	}
	return false
}

// repoint moves dependents to re-filed tasks after a task change; the change stands when it fails (a pass retries).
func repoint(r *hm.Runner, stderr io.Writer) {
	if err := r.RepointRefiles(); err != nil {
		fmt.Fprintln(stderr, "hivemill: re-point re-filed tasks:", err)
	}
}

func prio(t *hm.Task) string {
	if !t.Priority.Valid {
		return "-"
	}
	return fmt.Sprint(t.Priority.Int64)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for _, k := range []string{"title", "spec", "acceptance", "status", "priority", "depends_on"} {
		if _, ok := m[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "hivemill:", err)
	if errors.Is(err, hm.ErrRefused) {
		return 3
	}
	return 1
}

// isPlanner reports whether the caller is the planner session (agents row role planner).
func isPlanner(r *hm.Runner) bool {
	return r.Env.Actor != "" && r.Env.Actor != "owner" && r.Store.AgentRole(r.Env.Actor) == "planner"
}
