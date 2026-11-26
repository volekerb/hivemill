package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type ciFake struct {
	r      *Runner
	s      *SQLite
	run    *Run
	checks string // the statusCheckRollup of `gh pr view`
	reads  int    // `gh pr view` calls
	judges int    // `hc run` calls
}

// ciRunner is a gated run with an open PR, a fake gh and a fake hc.
func ciRunner(t *testing.T) *ciFake {
	t.Helper()
	r, s := testRunner(t, "")
	r.Poll = time.Millisecond
	f := &ciFake{r: r, s: s, run: &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main",
		Branch: "factory/t-0001", State: Gated, Round: 1, Head: "abc", PRURL: "u1", Worktree: t.TempDir()}}
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		if args[0] == "pr" && args[1] == "view" {
			f.reads++
			return 0, `{"number":1,"state":"OPEN","headRefOid":"abc","mergeable":"MERGEABLE","url":"u1","statusCheckRollup":` +
				f.checks + `}`, ""
		}
		return 0, "", ""
	}
	r.Tools.HC = func(args ...string) (int, string, string) {
		switch args[0] {
		case "run":
			f.judges++
		case "ls":
			return 0, `{"terminals":[]}`, ""
		}
		return 0, "", ""
	}
	dir := filepath.Join(r.Env.Home, "roles", "hivemill")
	for _, d := range []string{dir, filepath.Join(f.run.Worktree, ".hivemill")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"task-judge.json.in", "task-judge.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// poll moves the wait one ci_poll_s back and reads the PR again.
func (f *ciFake) poll() {
	f.run.CI.Polled -= 60
	if f.run.CI.Green != 0 {
		f.run.CI.Green -= 60
	}
	f.r.gated(f.run)
}

// alerts counts the owner messages sent from now on.
func (f *ciFake) alerts(t *testing.T) *int {
	t.Helper()
	n := new(int)
	f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { *n++; return 0, "" }
	if err := f.s.SetCfg("alert_cmd", "true"); err != nil {
		t.Fatal(err)
	}
	return n
}

const (
	unitOK  = `{"name":"unit","status":"COMPLETED","conclusion":"SUCCESS"}`
	e2eOK   = `{"name":"e2e","status":"COMPLETED","conclusion":"SUCCESS"}`
	e2eBusy = `{"name":"e2e","status":"IN_PROGRESS"}`
)

func TestCIWaitGreenToJudge(t *testing.T) {
	f := ciRunner(t)
	if _, err := f.s.db.Exec(`CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetFactoryCfg("required_checks", "unit,e2e"); err != nil {
		t.Fatal(err)
	}
	// A required check that is not registered yet keeps the run waiting, though every listed check passed.
	f.checks = `[` + unitOK + `]`
	f.r.gated(f.run)
	if f.run.State != Gated || f.run.CI == nil {
		t.Fatalf("a missing required check is not green: %s %+v", f.run.State, f.run.CI)
	}
	f.r.gated(f.run)
	if f.reads != 1 {
		t.Errorf("a second read inside ci_poll_s: %d reads", f.reads)
	}
	f.checks = `[` + unitOK + `,` + e2eBusy + `]`
	f.poll()
	if f.run.State != Gated || f.reads != 2 {
		t.Fatalf("pending stays gated: %s reads=%d", f.run.State, f.reads)
	}
	f.checks = `[` + unitOK + `,` + e2eOK + `]`
	f.poll()
	if f.run.State != Judging || f.judges != 1 {
		t.Fatalf("green starts the judge: %s (%s) judges=%d", f.run.State, f.run.FailReason, f.judges)
	}

	// Without a required list, the first green read is read again, also after earlier pending reads.
	f = ciRunner(t)
	f.checks = `[` + e2eBusy + `]`
	f.r.gated(f.run)
	f.checks = `[` + unitOK + `]`
	f.poll()
	if f.run.State != Gated {
		t.Fatalf("first green read: %s", f.run.State)
	}
	f.poll()
	if f.run.State != Judging || f.judges != 1 {
		t.Errorf("settled green: %s (%s) judges=%d", f.run.State, f.run.FailReason, f.judges)
	}
}

func TestCIWaitTimeoutAlerts(t *testing.T) {
	f := ciRunner(t)
	if _, err := f.s.db.Exec(`CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL,
		kind TEXT NOT NULL, ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL,
		closed TEXT, sent TEXT)`); err != nil {
		t.Fatal(err)
	}
	msgs := f.alerts(t)
	open := func() (n int) {
		if err := f.s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed
			WHERE kind='hivemill-ci' AND ref='T-0001' AND closed IS NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	f.checks = `[` + e2eBusy + `]`
	f.r.gated(f.run)
	if *msgs != 0 || f.run.CI == nil {
		t.Fatalf("no alert before ci_timeout_s: %d messages", *msgs)
	}
	f.run.CI.Since -= 2701
	f.poll()
	f.poll()
	if *msgs != 1 || open() != 1 {
		t.Errorf("one alert: %d messages, %d feed rows", *msgs, open())
	}
	if f.run.State != Gated || f.run.CIRounds != 0 || f.judges != 0 {
		t.Errorf("the run stays: %s ci=%d judges=%d", f.run.State, f.run.CIRounds, f.judges)
	}

	// A pass that takes the run out of the wait closes the alert; the wait survives the store.
	f.checks = `[` + unitOK + `]`
	f.poll()
	f.run.CI.Polled -= 60
	f.run.CI.Green -= 60
	if err := f.s.PutRun(f.run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Advance("T-0001", nil, false); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Run("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Judging || got.CI != nil || open() != 0 {
		t.Errorf("green after the alert: %s %+v, %d open rows", got.State, got.CI, open())
	}

	// A PR that cannot be read times out the same way.
	f = ciRunner(t)
	msgs = f.alerts(t)
	f.r.Tools.GH = func(string, ...string) (int, string, string) { return 1, "", "HTTP 502" }
	f.r.gated(f.run)
	f.run.CI.Since -= 2701
	f.poll()
	if *msgs != 1 || f.run.State != Gated || f.run.MergeHold == "" {
		t.Errorf("unreadable PR: %d messages, %s hold=%q", *msgs, f.run.State, f.run.MergeHold)
	}
}

func TestCIWaitRedResumes(t *testing.T) {
	var log strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&log, "unit\tTest\t\x1b[31mlog line %03d\x1b[0m\n", i)
	}
	const red = `[{"name":"unit","status":"COMPLETED","conclusion":"FAILURE",` +
		`"detailsUrl":"https://github.com/zz-test/www/actions/runs/11/job/22"},` +
		`{"name":"lint","conclusion":"STARTUP_FAILURE","detailsUrl":"https://github.com/zz-test/www/actions/runs/33"},` +
		e2eOK + `]`
	for _, pane := range []string{"live", "closed"} {
		f := ciRunner(t)
		f.checks = red
		f.run.Agent, f.run.Model, f.run.Effort = "claude-task-t-0001", "claude-sonnet-5-5", "high"
		var logReads []string
		view := f.r.Tools.GH
		f.r.Tools.GH = func(in string, args ...string) (int, string, string) {
			if args[0] != "run" {
				return view(in, args...)
			}
			logReads = append(logReads, strings.Join(args, " "))
			if slices.Contains(args, "22") {
				return 0, log.String(), ""
			}
			return 0, "the workflow file is invalid", ""
		}
		var sent []string
		launches := 0
		f.r.Tools.HC = func(args ...string) (int, string, string) {
			switch args[0] {
			case "send":
				sent = append(sent, args[2])
			case "run":
				launches++
			case "ls":
				if pane == "live" {
					return 0, `{"terminals":[{"name":"claude-task-t-0001","state":"live"}]}`, ""
				}
				return 0, `{"terminals":[]}`, ""
			}
			return 0, "", ""
		}
		for _, name := range []string{"task.json.in", "task.md"} {
			if err := os.WriteFile(filepath.Join(f.r.Env.Home, "roles", "hivemill", name), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.s.AddSession(Session{SID: "sid-1", Task: "T-0001", Role: "task", Agent: f.run.Agent}); err != nil {
			t.Fatal(err)
		}

		f.r.gated(f.run)

		want := map[string]int{"live": 0, "closed": 1}[pane]
		if f.run.State != Working || f.run.CIRounds != 1 || f.run.Round != 2 || launches != want || len(sent) != 1 {
			t.Fatalf("%s: %s (%s) ci=%d round=%d launches=%d messages=%d", pane, f.run.State, f.run.FailReason,
				f.run.CIRounds, f.run.Round, launches, len(sent))
		}
		for _, s := range []string{"CI is red on your PR: unit, lint.", "=== unit: last lines of the failed log ===",
			"log line 021\n", "log line 100", "=== lint:", "the workflow file is invalid"} {
			if !strings.Contains(sent[0], s) {
				t.Errorf("%s: the resume text lacks %q", pane, s)
			}
		}
		if strings.Contains(sent[0], "log line 020") || strings.Contains(sent[0], "\x1b") {
			t.Errorf("%s: more than the last 80 lines, or escape codes", pane)
		}
		if got := strings.Join(logReads, "; "); got != "run view --job 22 --log-failed --repo zz-test/www; "+
			"run view 33 --log-failed --repo zz-test/www" {
			t.Errorf("%s: log reads %q", pane, got)
		}
		script, err := os.ReadFile(filepath.Join(f.r.Env.StateDir(), "spawn", f.run.Agent+".sh"))
		if resumed := err == nil && strings.Contains(string(script), "sid-1"); resumed != (pane == "closed") {
			t.Errorf("%s: resumed by session id = %v", pane, resumed)
		}
		// one send, one record: a relaunch records its own as resume
		kind := map[string]string{"live": "ci", "closed": "resume"}[pane]
		if ev := eventsOf(t, f.r, "prompt.sent"); len(ev) != 1 || ev[0].Data["kind"] != kind {
			t.Errorf("%s: prompt.sent %+v", pane, ev)
		}
	}

	// Past ci_rounds_max the run fails.
	f := ciRunner(t)
	f.checks, f.run.CIRounds = red, 3
	f.r.gated(f.run)
	if f.run.State != Failed || !strings.Contains(f.run.FailReason, "CI red 4 times") {
		t.Errorf("over the limit: %s %q", f.run.State, f.run.FailReason)
	}
	// A red PR whose machine is disconnected waits and counts no round.
	f = ciRunner(t)
	f.checks, f.run.Machine = red, "m1"
	f.r.Tools.HC = func(...string) (int, string, string) {
		return 0, `{"terminals":[],"machines":[{"name":"m1","state":"disconnected"}]}`, ""
	}
	f.r.gated(f.run)
	if f.run.State != Gated || f.run.CIRounds != 0 || f.run.MergeHold == "" {
		t.Errorf("machine down: %s ci=%d hold=%q", f.run.State, f.run.CIRounds, f.run.MergeHold)
	}
}
