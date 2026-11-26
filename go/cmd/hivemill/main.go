// Command hivemill is the Option B core (docs/option-b-runner.md): one Claude Code session per task, a Stop-hook
// gate, a fresh judge and a mechanical merge.
//
//	hivemill run [T-1234]          one pass (advance every run, revert check, start ready tasks), or start one task
//	hivemill run status|stop T-1234
//	hivemill gate hook             Stop hook of a task session (reads the hook JSON on stdin)
//	hivemill gate T-1234           run the gate checks of a run now and print them
//	hivemill judge hook            Stop hook of a judge session
//	hivemill judge packet T-1234   print the judge packet of a run
//	hivemill merge T-1234          one merge attempt of a run in state merging (or print its decision with --dry-run)
//	hivemill report [--json]       scoreboard
//	hivemill eval T-1234 [--judge-only|--review-only]  eval run on a prepared branch with a draft PR: measures gate, review and judge, never merges
//	hivemill task lint T-1234|--all  spec gate: every criterion names its proof, none runs a whole Go module (--all: todo and draft tasks)
//	hivemill task add|list|show|update|cancel   file and change tasks (add and update refuse criteria that fail the spec gate; --draft)
//	hivemill goal new|list|show|close           goals
//	hivemill project add ID --repo OWNER/NAME --path DIR [--gate CMD]... | project list | project profile ID [--sensitive GLOB]...
//	hivemill init [--home DIR]     make a standalone factory home (DB tables, state dirs, ci/scrub_allowlist.txt); idempotent
//	hivemill launchd daemon|health print the daemon's LaunchAgent, or the 5-minute `health --alert` one, for this home
//	hivemill config [KEY [VALUE]]  runner settings (table hm_config) and the factory keys noreply_email, scrub_terms_file, ...
//	hivemill pr create --title T --body - | pr ready   a task session opens the PR of its own task (repo and branch from the DB)
//	hivemill guard --home DIR --role ROLE   the worker guard hook of a standalone home's sessions (exit 2 blocks)
//	hivemill guard export          write run/guard-export.json now (the daemon writes it every minute)
//	hivemill check public-text --home DIR REMOTE URL   the pre-push hook of a task clone (ci/pre_push_public_text.py in Go)
//	hivemill affected --base REF [--run] [--quick]     ci/affected.py in Go
//	hivemill proofs touched --base REF [--root DIR]    test the JS packages changed since REF (a project's gate proof)
//	hivemill daemon                the pass loop in-process (systemd Type=notify, launchd KeepAlive)
//	hivemill health [--alert]      exit 0 when the last good pass is recent; --alert raises the rate-limited dead-man alert
//	hivemill drain [--status] | resume   no new dispatch while active runs finish / lift it
//	hivemill backup | restore <snapshot> [--dry-run]   VACUUM INTO snapshots with retention, checked restore
//	hivemill planner start|stop|status|wake [REASON]   the planner session (keeps ready queues full; woken by the daemon)
//	hivemill ask add|list|answer|withdraw              the owner queue
//	hivemill digest [--date D] [--json] [--send]        the morning digest of one day (default yesterday)
//	hivemill obs [--listen HOST] [--port N] [--json] | obs pull --from HOST:PATH | obs sources   read-only page, multi-machine roll-up
//	hivemill otel export [--since D] [--endpoint URL] [--pulled] [--dry-run]       tasks as OpenTelemetry traces
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/hm"
)

var taskRe = regexp.MustCompile(`^(?i)T-\d+$`)

func main() {
	if code, done := preMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); done { // a worker forwards (forward.go)
		os.Exit(code)
	}
	os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// Main is the whole CLI; it returns the exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	usage := "usage: hivemill run [T-1234|status|stop T-1234] | gate hook|T-1234 | judge hook|packet T-1234 | merge T-1234 [--dry-run] | " +
		"report [--json] | eval T-1234 [--judge-only|--review-only] | task lint|add|list|show|update|cancel | goal new|list|show|close|status | " +
		"project add|list | config [KEY [VALUE]] | daemon | health [--alert] | drain [--status] | resume | " +
		"backup | restore <snapshot> [--dry-run] | init [--home DIR] | launchd daemon|digest|health | planner start|stop|status|wake | " +
		"ask add|list|answer|withdraw | digest [--date YYYY-MM-DD] [--json] [--send] | pr create|ready | " +
		"guard --home DIR --role ROLE | guard export | check public-text --home DIR REMOTE URL | affected --base REF | " +
		"proofs touched --base REF [--root DIR] | " +
		"obs [--listen HOST] [--port N] [--json] | obs pull --from HOST:PATH [--as NAME] | obs sources | " +
		"otel export [--since D] [--endpoint URL] [--pulled] [--dry-run]"
	if len(args) > 0 && args[0] == "guard" && !isGuardExport(args) { // a hook: its arguments and stdin are the guard's, never a help request
		return cmdGuard(args[1:], stdin, stdout, stderr)
	}
	help := len(args) == 0
	for _, a := range args {
		help = help || a == "-h" || a == "--help" // every subcommand: never read -h/--help as a task, key or path
	}
	if help {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if a := os.Getenv("FACTORY_AGENT"); (args[0] == "init" || args[0] == "launchd") && a != "" && a != "owner" {
		fmt.Fprintf(stderr, "hivemill: %s is for the owner, not %s\n", args[0], a)
		return 3
	}
	if args[0] == "planner" && len(args) > 1 && args[1] == "hook" {
		return cmdPlannerHook(stdin, stderr) // fails closed, needs no DB
	}
	switch args[0] {
	case "init": // before the DB exists
		return cmdInit(args[1:], stdout, stderr)
	case "launchd":
		return cmdLaunchd(args[1:], stdout, stderr)
	case "check": // the pre-push hook of a session's clone: no runner, any actor
		return cmdCheck(args[1:], stdin, stdout, stderr)
	case "affected": // the gate and the sessions: no runner, any actor
		return cmdAffected(args[1:], stdout, stderr)
	case "proofs": // a project's gate command: no runner, any actor
		return cmdProofs(args[1:], stdout, stderr)
	}
	env := hm.LoadEnv()
	hook := len(args) >= 2 && (args[1] == "hook" || args[1] == "hook-session")
	if hook {
		// a hook never loops the session: any failure is logged and the stop is allowed
		defer func() {
			if p := recover(); p != nil {
				hookLog(env, fmt.Sprintf("panic: %v", p))
				code = 0
			}
		}()
	}
	r, err := hm.NewRunner(env, stdout)
	if err != nil {
		if hook {
			hookLog(env, err.Error())
			return 0
		}
		fmt.Fprintln(stderr, "hivemill:", err)
		return 1
	}
	defer r.Store.Close()
	arg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	if !hook && env.Actor != "" && env.Actor != "owner" {
		role := r.Store.AgentRole(env.Actor)
		// a session may read tasks, never change one: the gate re-runs the proofs stored in the task row
		readTask := args[0] == "task" && (arg(1) == "lint" || arg(1) == "list" || arg(1) == "show")
		ownPR := args[0] == "pr" && (role == "task" || role == "coder") // the PR of the session's own task only
		if role == "planner" {
			if !plannerMay(args) {
				fmt.Fprintf(stderr, "hivemill: the planner may run goal, task, run status, report, health and ask add|list|withdraw, not %s\n", args[0])
				return 3
			}
		} else if role != "director" && role != "boss" && !(readTask || ownPR || args[0] == "report" || args[0] == "health") {
			fmt.Fprintf(stderr, "hivemill: %s is for the owner or the director, not %s\n", args[0], env.Actor)
			return 3
		}
	}
	switch args[0] {
	case "run":
		switch a := arg(1); {
		case a == "":
			err = r.Pass(true)
		case a == "hook-session":
			err = r.SessionHook(stdin)
		case a == "status":
			err = r.Status()
		case a == "stop" && taskRe.MatchString(arg(2)):
			err = r.Stop(strings.ToUpper(arg(2)))
		case taskRe.MatchString(a):
			id := strings.ToUpper(a)
			if run, _ := r.Store.Run(id); run != nil && hm.Active(run.State) {
				list, ok := hm.Terminals(r.Tools)
				var st string
				st, err = r.Advance(id, list, ok)
				fmt.Fprintf(stdout, "%s: %s\n", id, st)
			} else {
				err = r.Start(id)
			}
		default:
			fmt.Fprintln(stderr, usage)
			return 2
		}
	case "gate":
		switch a := arg(1); {
		case a == "hook":
			err = r.GateHook(stdin, stdout)
		case taskRe.MatchString(a):
			run, e := r.Store.Run(strings.ToUpper(a))
			if e != nil || run == nil {
				fmt.Fprintf(stderr, "hivemill: no run for %s\n", a)
				return 2
			}
			rec := r.Gate(run)
			b, _ := json.MarshalIndent(rec, "", " ")
			fmt.Fprintf(stdout, "%s\n", b)
			if len(rec.Problems) > 0 {
				return 1
			}
		default:
			fmt.Fprintln(stderr, usage)
			return 2
		}
	case "judge":
		switch arg(1) {
		case "hook":
			err = r.JudgeHook(stdin, stdout)
		case "packet":
			run, e := r.Store.Run(strings.ToUpper(arg(2)))
			if e != nil || run == nil {
				fmt.Fprintf(stderr, "hivemill: no run for %s\n", arg(2))
				return 2
			}
			var p string
			p, err = r.Packet(run)
			fmt.Fprint(stdout, p)
		default:
			fmt.Fprintln(stderr, usage)
			return 2
		}
	case "merge":
		run, e := r.Store.Run(strings.ToUpper(arg(1)))
		if e != nil || run == nil {
			fmt.Fprintf(stderr, "hivemill: no run for %s\n", arg(1))
			return 2
		}
		if arg(2) == "--dry-run" {
			in, d, e := r.MergeDecision(run)
			if e != nil {
				fmt.Fprintln(stderr, "hivemill:", e)
				return 1
			}
			b, _ := json.MarshalIndent(map[string]any{"input": in, "decision": d}, "", " ")
			fmt.Fprintf(stdout, "%s\n", b)
			return 0
		}
		if run.State != hm.Merging {
			fmt.Fprintf(stderr, "hivemill: %s is %s, not merging\n", run.Task, run.State)
			return 2
		}
		var merged *hm.Run
		if merged, err = r.MergeTask(run.Task); merged != nil {
			fmt.Fprintf(stdout, "%s: %s %s\n", merged.Task, merged.State, merged.MergeHold)
		}
	case "eval":
		if !taskRe.MatchString(arg(1)) || (arg(2) != "" && arg(2) != "--judge-only" && arg(2) != "--review-only") {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		mode := hm.EvalFull
		switch arg(2) {
		case "--judge-only":
			mode = hm.EvalJudgeOnly
		case "--review-only":
			mode = hm.EvalReview
		}
		err = r.EvalStart(strings.ToUpper(arg(1)), mode)
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		err = r.Daemon(ctx)
	case "health":
		ok, age := r.Health(time.Now())
		fmt.Fprintf(stdout, "last good pass %s ago; drain %v\n", age.Round(time.Second), r.Draining())
		// starvation and owner-queue checks: reported (and with --alert raised) whether or not the dead-man check passes
		checks := r.OwnerChecks(time.Now())
		for _, c := range checks {
			fmt.Fprintf(stdout, "%s: %s\n", c.Kind, c.Text)
		}
		if arg(1) == "--alert" && (env.Actor == "" || env.Actor == "owner") {
			if sent, e := r.RaiseOwnerAlerts(time.Now(), checks); e != nil {
				fmt.Fprintln(stderr, "hivemill: owner alert:", e)
			} else if len(sent) > 0 {
				fmt.Fprintf(stdout, "owner alert: %s\n", strings.Join(sent, ", "))
			}
		}
		if !ok {
			if arg(1) == "--alert" {
				if sent, e := r.HealthAlert(time.Now(), age); e != nil {
					fmt.Fprintln(stderr, "hivemill: alert:", e)
				} else if sent {
					fmt.Fprintln(stdout, "alert sent")
				}
			}
			return 1
		}
	case "drain":
		if arg(1) != "--status" {
			err = r.SetDrain(true)
		}
		if err == nil {
			fmt.Fprintf(stdout, "drain %v\n", r.Draining())
			err = r.Status()
		}
	case "resume":
		err = r.SetDrain(false)
		fmt.Fprintln(stdout, "drain off: new runs may start")
	case "backup":
		var p string
		p, err = r.Backup(time.Now())
		if err == nil {
			fmt.Fprintf(stdout, "snapshot %s\n%s\n", p, r.ReplicationPlan())
		}
	case "restore":
		if arg(1) == "" {
			fmt.Fprintln(stderr, "usage: hivemill restore <snapshot> [--dry-run]")
			return 2
		}
		l := r.DaemonRunning()
		var msg string
		msg, err = hm.Restore(env, arg(1), arg(2) == "--dry-run", l)
		if err == nil {
			fmt.Fprintln(stdout, msg)
		}
	case "report":
		err = r.Report(arg(1) == "--json")
	case "task":
		return cmdTask(r, args[1:], stdin, stdout, stderr)
	case "planner":
		return cmdPlanner(r, args[1:], stdout, stderr)
	case "ask":
		return cmdAsk(r, args[1:], stdout, stderr)
	case "digest":
		return cmdDigest(r, args[1:], stdout, stderr)
	case "obs": // read-only page, JSON portfolio, pull other machines (docs/hivemill-observability.md)
		return cmdObs(r, args[1:], stdout, stderr)
	case "otel":
		return cmdOtel(r, args[1:], stdout, stderr)
	case "goal":
		return cmdGoal(r, args[1:], stdin, stdout, stderr)
	case "project":
		return cmdProject(r, args[1:], stdout, stderr)
	case "config":
		return cmdConfig(r, args[1:], stdout, stderr)
	case "guard": // only `guard export` reaches here
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: hivemill guard export")
			return 2
		}
		err = r.WriteGuardExport(time.Now())
	case "pr":
		return cmdPR(r, args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		if hook {
			hookLog(env, err.Error())
			return 0
		}
		fmt.Fprintln(stderr, "hivemill:", err)
		if errors.Is(err, hm.ErrRefused) {
			return 3
		}
		return 1
	}
	return 0
}

// isGuardExport: `guard export` is a runner command that opens the DB; every other `guard ...` is the hook.
func isGuardExport(args []string) bool {
	return len(args) > 1 && args[0] == "guard" && args[1] == "export"
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func hookLog(env hm.Env, msg string) {
	_ = os.MkdirAll(env.StateDir(), 0o700)
	f, err := os.OpenFile(env.StateDir()+"/hook-errors.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: %s\n", env.Actor, strings.Join(os.Args[1:], " "), msg)
}
