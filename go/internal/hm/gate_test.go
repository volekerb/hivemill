package hm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A proof that cannot pass is marked as the spec's fault: it exits 2 or 127, or fails the same way on the base commit.
// A task that fails on it says so in task.failed.
func TestGateSpecDefect(t *testing.T) {
	r, s := testRunner(t, "claude-task-t-0001")
	fake, real := r.Tools, RealTools(r.Env)
	wt := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		rc, out, errs := real.Git(wt, append([]string{"-c", "user.name=zz", "-c", "user.email=zz@users.noreply.github.com",
			"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		if rc != 0 {
			t.Fatalf("git %v: %s", args, errs)
		}
		return strings.TrimSpace(out)
	}
	write := func(name, text string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(filepath.Join(wt, name)), 0o755)
		if err := os.WriteFile(filepath.Join(wt, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write(".gitignore", ".hivemill/\n")
	write("state", "base\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	write("state", "head\n")
	git("commit", "-q", "-a", "-m", "head")
	write(".hivemill/findings.json", `{"findings": []}`)
	write(".hivemill/evidence.json", `{"criteria": [{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}, {"id": 5}]}`)
	r.Tools = fake
	r.Tools.Git = func(dir string, args ...string) (int, string, string) {
		if args[0] == "fetch" {
			return 0, "", ""
		}
		return real.Git(dir, args...)
	}
	r.Tools.Sh = real.Sh
	_, err := s.db.Exec(`UPDATE tasks SET acceptance=? WHERE id='T-0001'`, strings.Join([]string{
		"1. missing command - cmd:false-no-such-cmd => exit 0",
		"2. usage error - cmd:exit 2 => exit 0",
		"3. broken on the base too - cmd:echo broken in $PWD; exit 1 => exit 0",
		"4. the work broke it - cmd:cat state; grep -q base state => exit 0",
		"5. passes - cmd:true => exit 0",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: wt,
		Branch: "factory/t-0001", Agent: "claude-task-t-0001", State: Working, Round: 1}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	rec := r.Gate(run)
	problem := func(id string) string {
		for _, p := range rec.Problems {
			if strings.Contains(p, "criterion "+id+" ") {
				return p
			}
		}
		return ""
	}
	for _, id := range []string{"1", "2", "3"} {
		if p := problem(id); !strings.HasPrefix(p, "spec defect:") {
			t.Errorf("criterion %s: want a spec defect, got %q", id, p)
		}
	}
	if p := problem("4"); p == "" || strings.HasPrefix(p, "spec defect:") {
		t.Errorf("criterion 4 passes on the base, so it is the work's failure: %q", p)
	}
	if p := problem("5"); p != "" {
		t.Errorf("criterion 5 passed: %q", p)
	}
	if len(rec.Problems) == 0 || !strings.HasPrefix(rec.Problems[0], "spec defect:") {
		t.Errorf("spec defects lead the problems: %q", rec.Problems)
	}
	if _, err := os.Stat(filepath.Join(wt, ".hivemill", "base")); !os.IsNotExist(err) || strings.Count(git("worktree", "list"), "\n") != 0 {
		t.Errorf("the base checkout is removed: %v %s", err, git("worktree", "list"))
	}

	// a checkout an interrupted gate left is gone before the proofs run; over the time budget nothing runs on the base
	write(".hivemill/base/state", "left over\n")
	old := baseRunBudget
	baseRunBudget = 0
	rec = r.Gate(run)
	baseRunBudget = old
	if _, err := os.Stat(filepath.Join(wt, ".hivemill", "base")); !os.IsNotExist(err) {
		t.Errorf("a left over base checkout is removed: %v", err)
	}
	if p := problem("3"); p == "" || strings.HasPrefix(p, "spec defect:") {
		t.Errorf("over the budget, criterion 3 is not judged: %q", p)
	}

	failedKind := func() any {
		t.Helper()
		evs, err := r.Log.Read()
		if err != nil {
			t.Fatal(err)
		}
		last := evs[len(evs)-1]
		if last.Event != "task.failed" {
			t.Fatalf("last event %s", last.Event)
		}
		return last.Data["reason_kind"]
	}
	for i := 0; i < 3; i++ {
		var out bytes.Buffer
		if err := r.GateHook(strings.NewReader(`{}`), &out); err != nil {
			t.Fatal(err)
		}
	}
	if k := failedKind(); k != "spec_defect" {
		t.Errorf("reason_kind %v, want spec_defect", k)
	}
	for _, c := range []struct {
		block, reason string
		want          any
	}{
		{"spec defect: x", "stalled: idle", "spec_defect"},
		{"spec defect: x", "budget: over", nil},
		{"pr: no open PR\ncommitted: quotes spec defect: in passing", "gate blocked 3 times", nil},
	} {
		r.fail(&Run{Task: "T-0001", LastBlock: c.block}, c.reason)
		if k := failedKind(); k != c.want {
			t.Errorf("%q after %q: reason_kind %v, want %v", c.reason, c.block, k, c.want)
		}
	}
}
