package hm

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// termHC is a fake hc that keeps the live terminals and what was sent to them.
type termHC struct {
	live  map[string]bool
	sent  []string
	spawn int
}

func (f *termHC) hc(args ...string) (int, string, string) {
	switch args[0] {
	case "run":
		f.spawn++
		for i, a := range args {
			if a == "--name" {
				f.live[args[i+1]] = true
			}
		}
		return 0, "{}", ""
	case "ls":
		var ts []string
		for n := range f.live {
			ts = append(ts, `{"name": "`+n+`", "state": "live", "agent": "claude-code", "status": "idle"}`)
		}
		return 0, `{"terminals": [` + strings.Join(ts, ",") + `]}`, ""
	case "send":
		f.sent = append(f.sent, strings.Join(args[1:], " "))
	}
	return 0, "", ""
}

func resumeRunner(t *testing.T) (*Runner, *SQLite, *termHC) {
	t.Helper()
	r, s := testRunner(t, "")
	f := &termHC{live: map[string]bool{}}
	r.Tools.HC = f.hc
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
			return 0, `{"number":1,"state":"OPEN","headRefOid":"a"}`, ""
		}
		return 0, "", ""
	}
	r.Poll, r.ReadyWait = time.Millisecond, time.Second
	sessionFiles(r)
	return r, s, f
}

// sessionFiles are the files a task session start reads.
func sessionFiles(r *Runner) {
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	for _, name := range []string{"task.json.in", "task.md"} {
		_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", name), []byte(`{}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
}

func TestAnsweredHeldAskResumes(t *testing.T) {
	const agent = "claude-coder-t-0001-a7"
	for _, c := range []struct {
		name, verdict, answer, ref string
		live                       bool
		state                      string
		sent, spawns               int
	}{
		{"warm session", "HOLD", "split the change", "held:T-0001:1", true, Working, 1, 0},
		{"closed session", "HOLD", "split the change", "held:T-0001", false, Working, 1, 1},
		{"merge a judged MERGE", VerdictMerge, " Merge it", "held:T-0001:1", false, Merging, 0, 0},
		{"merge without a MERGE verdict", "HOLD", "merge it", "held:T-0001:1", true, Working, 1, 0},
		{"answer of an older round", "HOLD", "split the change", "held:T-0001:7", true, Held, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, s, f := resumeRunner(t)
			wt := t.TempDir()
			_ = os.MkdirAll(filepath.Join(wt, ".git"), 0o755)
			f.live[agent] = c.live
			if !c.live {
				delete(f.live, agent)
			}
			run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Agent: agent, Worktree: wt,
				Model: "claude-sonnet-5-5", Effort: "medium", State: Held, Round: 1, MergeHold: "the owner merges",
				PRURL: "https://github.com/zz-test/www/pull/1", Verdicts: []Verdict{{Verdict: c.verdict}}}
			_ = s.PutRun(run)
			_ = s.AddSession(Session{SID: "sid-1", Task: "T-0001", Role: "task", Agent: agent, Started: nowISO()})
			// an older answered question about the same hold: used up with the latest, never acted on later
			older, _, _ := s.AddAsk(Ask{Ref: "held:T-0001", Task: "T-0001", Question: "T-0001 waits", AskedBy: "runner"})
			id, _, err := s.AddAsk(Ask{Ref: c.ref, Task: "T-0001", Question: "T-0001 is held", AskedBy: "runner"})
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Pass(false)
			if got, _ := s.Run("T-0001"); got.State != Held || len(f.sent) != 0 {
				t.Fatalf("an open question moves nothing: %s, sent %q", got.State, f.sent)
			}
			if c.ref == "held:T-0001:1" {
				_ = r.AskClose(older, true, "an older answer")
			}
			if err := r.AskClose(id, true, c.answer); err != nil {
				t.Fatal(err)
			}
			if err := r.Pass(false); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Run("T-0001")
			if got.State != c.state || len(f.sent) != c.sent || f.spawn != c.spawns {
				t.Fatalf("state %s %q, sent %q, spawns %d; want %s, %d sent, %d spawns", got.State, got.FailReason, f.sent, f.spawn,
					c.state, c.sent, c.spawns)
			}
			if c.state == Working {
				if got.Round != 2 || got.MergeHold != "" || !strings.Contains(f.sent[0], strings.TrimSpace(c.answer)) {
					t.Errorf("round %d hold %q prompt %q", got.Round, got.MergeHold, f.sent[0])
				}
			}
			script, _ := os.ReadFile(filepath.Join(r.Env.StateDir(), "spawn", agent+".sh"))
			if c.spawns > 0 && !strings.Contains(string(script), "--resume sid-1") {
				t.Errorf("the new session is not a resumed one: %q", script)
			}
			if c.state == Held {
				return
			}
			answered, _ := s.Asks("answered")
			for _, a := range answered {
				if strings.HasPrefix(a.Ref, "held:") {
					t.Errorf("an answer is left to act on: %+v", a)
				}
			}
			// the answer is used once: the same question about a later hold is filed anew
			if _, isNew, _ := s.AddAsk(Ask{Ref: c.ref, Task: "T-0001", Question: "T-0001 is held again", AskedBy: "runner"}); !isNew {
				t.Error("the used question still blocks a new one")
			}
		})
	}
}

func TestRequeueFailedTask(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	addReady(t, s, 1)
	_ = s.PutRun(&Run{Task: "T-0001", State: Failed, FailReason: "zz-test failure"})
	_ = s.TaskStatus("T-0001", "blocked", "owner")
	_ = s.TaskPR("T-0001", "https://github.com/zz-test/www/pull/1", "T1")
	_, _, _ = s.AddAsk(Ask{Ref: "held:T-0001:1", Task: "T-0001", Question: "T-0001 is held", AskedBy: "runner"})
	if got, _ := r.ReadyIn([]string{"www"}); len(got) != 0 {
		t.Fatalf("a failed task is ready: %v", got)
	}
	if err := r.Requeue("T-0001"); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Task("T-0001")
	got, err := r.ReadyIn([]string{"www"})
	if err != nil || task.Status != "todo" || len(got) != 1 {
		t.Fatalf("status %s, ready %v, err %v", task.Status, got, err)
	}
	if task.PRURL != "" {
		t.Errorf("the old run's PR is kept: %s", task.PRURL)
	}
	var old string
	if err := s.db.QueryRow(`SELECT data FROM hm_runs WHERE task='T-0001#1'`).Scan(&old); err != nil || !strings.Contains(old, "zz-test failure") {
		t.Fatalf("the old run row is gone: %q %v", old, err)
	}
	if runs, _ := s.Runs(); len(runs) != 0 {
		t.Fatalf("a retired run is still listed: %v", runs)
	}
	if _, isNew, _ := s.AddAsk(Ask{Ref: "held:T-0001:1", Task: "T-0001", Question: "held again", AskedBy: "runner"}); !isNew {
		t.Error("the old question still blocks one about the new run")
	}
	// dispatch: the next pass makes a new run (the fake clone and session are enough to see it start)
	_ = s.SetCfg("projects", "www")
	f := &termHC{live: map[string]bool{}}
	r.Tools.HC, r.Tools.Git = f.hc, func(string, ...string) (int, string, string) { return 0, "", "" }
	r.ReadyWait = time.Second
	sessionFiles(r)
	_ = os.MkdirAll(filepath.Join(r.Env.UserHome, ".claude"), 0o700)
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude", "usage-limits.json"),
		[]byte(`{"five_hour_pct": "10", "seven_day_pct": "12", "updated_at": `+strconv.FormatInt(time.Now().Unix(), 10)+`}`), 0o600)
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "worktrees", "G-001-T-0001", ".git"), 0o755)
	if err := r.Pass(true); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.Run("T-0001"); run == nil || run.State != Working || run.Round != 1 || f.spawn != 1 {
		t.Fatalf("no new run: %+v, %d spawns", run, f.spawn)
	}
	if err := r.Requeue("T-0001"); err == nil {
		t.Error("an active run was requeued")
	}
	// a cancelled run goes the same way, and the second retired row gets its own key
	run, _ := s.Run("T-0001")
	run.State = Cancelled
	_ = s.PutRun(run)
	if err := r.Requeue("T-0001"); err != nil {
		t.Fatal(err)
	}
	var rows int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM hm_runs WHERE task LIKE 'T-0001#%'`).Scan(&rows)
	if rows != 2 {
		t.Errorf("retired rows %d, want 2", rows)
	}
}
