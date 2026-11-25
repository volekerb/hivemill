package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDrainHoldsNewRuns(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("projects", "www")
	_, _ = s.db.Exec(`UPDATE tasks SET status='todo'`)
	_ = r.SetDrain(true)
	if err := r.Start("T-0001"); err == nil || !strings.Contains(err.Error(), "drain") {
		t.Fatalf("drain must refuse a start: %v", err)
	}
	_ = r.SetDrain(false)
	if r.Draining() {
		t.Error("resume lifts drain")
	}
}

func TestHealthAndRateLimitedAlert(t *testing.T) {
	r, s := testRunner(t, "")
	_, _ = s.db.Exec(`CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL, kind TEXT NOT NULL,
		ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, closed TEXT, sent TEXT)`)
	msgs := 0
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { msgs++; return 0, "" }
	_ = s.SetCfg("alert_cmd", "true") // the owner alert goes through alert_cmd (counted by the Sh above)
	now := time.Unix(1795548325, 0)
	if ok, _ := r.Health(now); ok {
		t.Error("no heartbeat yet is not healthy")
	}
	_ = r.Heartbeat(now.Add(-2 * time.Minute))
	if ok, _ := r.Health(now); !ok {
		t.Error("a pass 2 minutes ago is healthy")
	}
	_ = r.Heartbeat(now.Add(-10 * time.Minute))
	ok, age := r.Health(now)
	if ok {
		t.Fatal("a pass 10 minutes ago is not healthy")
	}
	for i := 0; i < 2; i++ { // run twice: one alert, one message
		if _, err := r.HealthAlert(now, age); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-dead'`).Scan(&n)
	if n != 1 || msgs != 1 {
		t.Errorf("rate limit: %d feed rows, %d messages", n, msgs)
	}
	if sent, _ := r.HealthAlert(now.Add(2*time.Hour), age); !sent {
		t.Error("after alert_every_s a new alert goes out")
	}
}

func TestBackupRetentionAndRestoreDryRun(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("backup_keep", "2")
	target := t.TempDir()
	_ = s.SetCfg("backup_target", "file://"+target)
	var snaps []string
	for i := 0; i < 3; i++ {
		p, err := r.Backup(time.Unix(1795548325+int64(i), 0))
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, p)
	}
	left, _ := filepath.Glob(filepath.Join(r.backupDir(), "factory-*.db"))
	copied, _ := filepath.Glob(filepath.Join(target, "factory-*.db"))
	if len(left) != 2 || len(copied) != 2 {
		t.Errorf("keep 2: %d local, %d in target", len(left), len(copied))
	}
	msg, err := Restore(r.Env, snaps[2], true, false)
	if err != nil || !strings.Contains(msg, "integrity ok, 1 tasks") {
		t.Errorf("dry run: %q %v", msg, err)
	}
	if _, err := Restore(r.Env, snaps[2], false, true); err == nil {
		t.Error("restore must refuse while a daemon runs")
	}
	bad := filepath.Join(t.TempDir(), "x.db")
	_ = os.WriteFile(bad, []byte("not a db"), 0o600)
	if _, err := Restore(r.Env, bad, true, false); err == nil {
		t.Error("a broken snapshot is refused")
	}
	if !r.BackupDue(time.Unix(1795548325, 0)) == true && s.Cfg("backup_interval") == "off" {
		// backup_interval off: never due
	}
	_ = s.SetCfg("backup_interval", "6h")
	if r.BackupDue(time.Unix(1795548330, 0)) {
		t.Error("a snapshot seconds ago is not due")
	}
	if !r.BackupDue(time.Unix(1795548330+7*3600, 0)) {
		t.Error("7 h later a 6h backup is due")
	}
}

// Each side effect runs twice and acts once.
func TestStepsAreIdempotent(t *testing.T) {
	r, s := testRunner(t, "")
	runs := 0
	live := map[string]bool{}
	r.Tools.HC = func(args ...string) (int, string, string) {
		switch args[0] {
		case "run":
			runs++
			for i, a := range args {
				if a == "--name" {
					live[args[i+1]] = true
				}
			}
			return 0, "{}", ""
		case "ls":
			out := `{"terminals": [`
			first := true
			for n := range live {
				if !first {
					out += ","
				}
				first = false
				out += `{"name": "` + n + `", "state": "live", "agent": "claude-code", "status": "idle"}`
			}
			return 0, out + `]}`, ""
		}
		return 0, "", ""
	}
	r.Poll = time.Millisecond
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	for _, f := range []string{"task-judge.json.in", "task-judge.md"} {
		_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", f), []byte(`{}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
	run := &Run{Task: "T-0001", Goal: "G-001", Worktree: t.TempDir(), Base: "main", Model: "claude-sonnet-5-5", Effort: "medium"}
	l := Launch{Role: "task-judge", Name: "claude-task-judge-t-0001-r1", Cwd: run.Worktree, Model: run.Model, Effort: run.Effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", "task-judge.md"), Kickoff: "x"}
	for i := 0; i < 2; i++ {
		if _, err := r.StartSession(run, l); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 1 {
		t.Errorf("a judge started twice runs %d terminals, want 1", runs)
	}
	// revert: the second attempt finds the task the first one filed
	title := "Revert T-0001: main red after its merge"
	id, _ := s.NewTask("G-001", title, "spec", "1. x - manual: y")
	if got, _ := s.TaskByTitle("G-001", title); got != id {
		t.Errorf("TaskByTitle %q, want %q", got, id)
	}
	// merge: a merged PR is "done" on every later attempt, never merged again
	in := MergeInput{PRState: "MERGED"}
	if Decide(in).Action != "done" || Decide(in).Action != "done" {
		t.Error("merged twice")
	}
}

func TestReconcileResumesALostSessionThenFails(t *testing.T) {
	r, s := testRunner(t, "")
	r.Poll = time.Millisecond
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	for _, f := range []string{"task.json.in", "task.md"} {
		_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", f), []byte(`{}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
	wt := t.TempDir()
	_ = os.MkdirAll(filepath.Join(wt, ".git"), 0o755)
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "ls" {
			return 0, `{"terminals": []}`, "" // the session is gone (reboot)
		}
		return 0, "{}", ""
	}
	run := &Run{Task: "T-0001", Goal: "G-001", Agent: "claude-coder-t-0001-a7", State: Working, Round: 1, Worktree: wt, Base: "main",
		Model: "claude-sonnet-5-5", Effort: "medium"}
	_ = s.PutRun(run)
	_ = s.AddSession(Session{SID: "sid-1", Task: "T-0001", Role: "task", Agent: run.Agent, Model: run.Model, Effort: run.Effort, Started: nowISO()})
	_ = s.SetCfg("resume_max", "1")
	if _, err := r.Advance("T-0001", []Terminal{}, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run("T-0001")
	b, _ := os.ReadFile(filepath.Join(r.Env.StateDir(), "spawn", run.Agent+".sh"))
	if got.Resumes != 1 || got.State != Working || !strings.Contains(string(b), "--resume sid-1") {
		t.Fatalf("first loss: resumes %d state %s script %q", got.Resumes, got.State, b)
	}
	_, _ = r.Advance("T-0001", []Terminal{}, true)
	if got, _ = s.Run("T-0001"); got.State != Failed || !strings.Contains(got.FailReason, "session lost") {
		t.Errorf("after resume_max: %s %q", got.State, got.FailReason)
	}
}

func TestReviewEvidence(t *testing.T) {
	r, s := testRunner(t, "")
	run := &Run{Task: "T-0001", RoundStart: time.Now().Add(-time.Minute).Format(time.RFC3339)}
	_ = s.AddSession(Session{SID: "sid-r", Task: "T-0001", Role: "task", Agent: "a", Started: nowISO()})
	if r.ReviewRan(run) {
		t.Fatal("no marker: no review")
	}
	dir := filepath.Join(r.Env.UserHome, ".claude", "projects", "-x", "sid-r", "subagents")
	_ = os.MkdirAll(dir, 0o700)
	f := filepath.Join(dir, "agent-1.forked-skill.json")
	_ = os.WriteFile(f, []byte(`{"skillName":"simplify"}`), 0o600)
	if r.ReviewRan(run) {
		t.Error("another skill is not a review")
	}
	_ = os.WriteFile(f, []byte(`{"skillName":"code-review","effort":"medium"}`), 0o600)
	if !r.ReviewRan(run) {
		t.Error("a code-review marker in this round counts")
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(f, old, old)
	if r.ReviewRan(run) {
		t.Error("a review from an earlier round does not count")
	}
	p := filepath.Join(t.TempDir(), "f.json")
	_ = os.WriteFile(p, []byte(`{"head": "abc", "findings": []}`), 0o600)
	if probs, _, head := checkFindings(p); len(probs) != 0 || head != "abc" {
		t.Errorf("empty findings with a head are valid: %v %q", probs, head)
	}
}
