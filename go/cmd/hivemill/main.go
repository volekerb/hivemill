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
//	hivemill eval T-1234 [--judge-only]  eval run on a prepared branch with a draft PR: measures gate, review and judge, never merges
//	hivemill task lint T-1234      spec gate: every acceptance criterion names its proof
//	hivemill config [KEY [VALUE]]  runner settings (table hm_config)
//	hivemill daemon                the pass loop in-process (systemd Type=notify, launchd KeepAlive)
//	hivemill health [--alert]      exit 0 when the last good pass is recent; --alert raises the rate-limited dead-man alert
//	hivemill drain [--status] | resume   no new dispatch while active runs finish / lift it
//	hivemill backup | restore <snapshot> [--dry-run]   VACUUM INTO snapshots with retention, checked restore
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

func main() { os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// Main is the whole CLI; it returns the exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	usage := "usage: hivemill run [T-1234|status|stop T-1234] | gate hook|T-1234 | judge hook|packet T-1234 | merge T-1234 [--dry-run] | " +
		"report [--json] | eval T-1234 [--judge-only] | task lint T-1234 | config [KEY [VALUE]] | daemon | health [--alert] | drain [--status] | resume | " +
		"backup | restore <snapshot> [--dry-run]"
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, usage)
		return 0
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
	if !hook && env.Actor != "" && env.Actor != "owner" {
		role := r.Store.AgentRole(env.Actor)
		if role != "director" && role != "boss" && !(args[0] == "task" || args[0] == "report" || args[0] == "health") {
			fmt.Fprintf(stderr, "hivemill: %s is for the owner or the director, not %s\n", args[0], env.Actor)
			return 3
		}
	}
	arg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
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
		r.Merge(run)
		err = r.Store.PutRun(run)
		fmt.Fprintf(stdout, "%s: %s %s\n", run.Task, run.State, run.MergeHold)
	case "eval":
		if !taskRe.MatchString(arg(1)) || (arg(2) != "" && arg(2) != "--judge-only") {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		mode := hm.EvalFull
		if arg(2) == "--judge-only" {
			mode = hm.EvalJudgeOnly
		}
		err = r.EvalStart(strings.ToUpper(arg(1)), mode)
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		err = r.Daemon(ctx)
	case "health":
		ok, age := r.Health(time.Now())
		fmt.Fprintf(stdout, "last good pass %s ago; drain %v\n", age.Round(time.Second), r.Draining())
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
		if arg(1) != "lint" || !taskRe.MatchString(arg(2)) {
			fmt.Fprintln(stderr, "usage: hivemill task lint T-1234")
			return 2
		}
		t, e := r.Store.Task(strings.ToUpper(arg(2)))
		if e != nil {
			fmt.Fprintln(stderr, "hivemill:", e)
			return 2
		}
		for _, c := range hm.Criteria(t.Acceptance) {
			p := c.Proof
			if p == "" {
				p = c.Text
			}
			if c.Expect != "" {
				p += " => " + c.Expect
			}
			fmt.Fprintf(stdout, "  %d. %s: %s\n", c.ID, or(c.Kind, "-"), trunc(p, 100))
		}
		if probs := hm.LintProblems(t.Acceptance); len(probs) > 0 {
			fmt.Fprintf(stdout, "%s: spec gate FAIL\n  - %s\n", t.ID, strings.Join(probs, "\n  - "))
			return 1
		}
		fmt.Fprintf(stdout, "%s: spec gate PASS (%d criteria)\n", t.ID, len(hm.Criteria(t.Acceptance)))
	case "config":
		switch len(args) {
		case 1:
			for _, ln := range hm.ConfigLines(r.Store.Cfg) {
				fmt.Fprintln(stdout, ln)
			}
		case 2:
			fmt.Fprintln(stdout, r.Store.Cfg(args[1]))
		default:
			err = r.Store.SetCfg(args[1], args[2])
		}
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
