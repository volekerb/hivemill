package hm

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cleanFix struct {
	r                           *Runner
	s                           *SQLite
	wt, repo, origin, head      string
	transcript, otherTranscript string
}

func zzGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=zz-test", "-c", "user.email=zz@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func zzWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newCleanFix is a done task (2 days ago) with a task clone as the runner makes it, its branch pushed to origin. Git
// and the process scan are real; unreadable processes of the test host are dropped from the scan.
func newCleanFix(t *testing.T) *cleanFix {
	t.Helper()
	r, s := testRunner(t, "")
	f := &cleanFix{r: r, s: s}
	base := t.TempDir()
	f.origin, f.repo = filepath.Join(base, "origin.git"), filepath.Join(base, "repo")
	zzGit(t, base, "init", "-q", "--bare", f.origin)
	zzGit(t, base, "clone", "-q", f.origin, f.repo)
	zzGit(t, f.repo, "checkout", "-q", "-B", "main")
	zzWrite(t, filepath.Join(f.repo, "a.txt"), "a\n")
	zzGit(t, f.repo, "add", "a.txt")
	zzGit(t, f.repo, "commit", "-q", "-m", "a")
	zzGit(t, f.repo, "push", "-q", "origin", "main")
	f.wt = filepath.Join(r.Env.Home, "worktrees", "G-001-T-0001")
	zzGit(t, base, "clone", "-q", "--shared", f.repo, f.wt)
	zzGit(t, f.wt, "remote", "set-url", "origin", f.origin)
	zzGit(t, f.wt, "fetch", "-q", "origin")
	zzGit(t, f.wt, "checkout", "-q", "-B", "factory/t-0001")
	zzWrite(t, filepath.Join(f.wt, "b.txt"), strings.Repeat("b", 2<<20))
	zzGit(t, f.wt, "add", "b.txt")
	zzGit(t, f.wt, "commit", "-q", "-m", "b")
	zzGit(t, f.wt, "push", "-q", "origin", "factory/t-0001")
	f.head = zzGit(t, f.wt, "rev-parse", "HEAD")
	f.exec(t, `UPDATE projects SET repo_path=?`, f.repo)
	f.exec(t, `UPDATE tasks SET status='done', branch='factory/t-0001', updated=?`,
		time.Now().Add(-48*time.Hour).Format("2006-01-02T15:04:05-07:00"))

	r.Tools.Git = func(repo string, args ...string) (int, string, string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		var out, errs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errs
		if err := cmd.Run(); err != nil {
			return 1, out.String(), errs.String()
		}
		return 0, out.String(), ""
	}
	sh := RealTools(r.Env).Sh
	r.Tools.Sh = func(dir string, timeout time.Duration, env []string, argv ...string) (int, string) {
		rc, out := sh(dir, timeout, env, argv...)
		var keep []string
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "unread ") {
				keep = append(keep, line)
			}
		}
		return rc, strings.Join(keep, "\n")
	}
	r.DiskFree = func(string) (uint64, uint64, error) { return 100, 90, nil } // the disk is 10% used
	return f
}

func (f *cleanFix) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.s.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *cleanFix) clean(t *testing.T, dry bool) *CleanReport {
	t.Helper()
	rep, err := f.r.Clean(context.Background(), dry, time.Now())
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("clean: %v %v", err, rep)
	}
	return rep
}

func (f *cleanFix) events(t *testing.T, kind string) (out []Event) {
	t.Helper()
	all, err := f.r.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if e.Event == kind {
			out = append(out, e)
		}
	}
	return out
}

func (f *cleanFix) wantKept(t *testing.T, reason string) {
	t.Helper()
	rep := f.clean(t, false)
	if _, err := os.Stat(filepath.Join(f.wt, "a.txt")); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}
	if len(rep.Kept) != 1 || !strings.Contains(rep.Kept[0].Reason, reason) {
		t.Fatalf("kept = %+v, want reason %q", rep.Kept, reason)
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestCleanRemovesFinishedWorktree(t *testing.T) {
	f := newCleanFix(t)
	// squash-merged: the branch is gone on origin, the merged run records its head; the checkout has the local branch
	zzGit(t, f.repo, "fetch", "-q", f.wt, "factory/t-0001:factory/t-0001")
	zzGit(t, f.wt, "push", "-q", "origin", "--delete", "factory/t-0001")
	if err := f.s.PutRun(&Run{Task: "T-0001", Goal: "G-001", State: Merged, Head: f.head, Worktree: f.wt}); err != nil {
		t.Fatal(err)
	}
	rep := f.clean(t, false)
	if exists(f.wt) {
		t.Fatalf("worktree still there: %+v", rep.Kept)
	}
	if rep.MB(cleanWorktrees) < 2 {
		t.Errorf("MB freed = %d", rep.MB(cleanWorktrees))
	}
	if got := zzGit(t, f.repo, "branch", "--list", "factory/t-0001"); got != "" {
		t.Errorf("local branch of a merged PR stays: %q", got)
	}
	done := f.events(t, "clean.done")
	if len(done) != 1 || done[0].Data["worktrees"] != float64(1) || done[0].Data["worktrees_mb"].(float64) < 2 {
		t.Errorf("clean.done = %+v", done)
	}
}

func TestCleanRemovesLinkedWorktree(t *testing.T) {
	f := newCleanFix(t)
	if err := os.RemoveAll(f.wt); err != nil {
		t.Fatal(err)
	}
	zzGit(t, f.repo, "worktree", "add", "-q", "--detach", f.wt, "main")
	f.clean(t, false)
	if exists(f.wt) || strings.Contains(zzGit(t, f.repo, "worktree", "list"), "G-001-T-0001") {
		t.Fatal("linked worktree not removed and pruned")
	}
}

func TestCleanKeepsDirtyWorktree(t *testing.T) {
	f := newCleanFix(t)
	zzWrite(t, filepath.Join(f.wt, "new.txt"), "work\n")
	f.wantKept(t, "uncommitted changes")
	f.wantKept(t, "uncommitted changes")
	if got := f.events(t, "clean.kept"); len(got) != 1 || got[0].Data["reason"] != "uncommitted changes" {
		t.Errorf("clean.kept once per reason, got %+v", got)
	}
}

func TestCleanKeepsUnsavedCommits(t *testing.T) {
	f := newCleanFix(t)
	zzGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "local only")
	f.wantKept(t, "not on origin")
	zzGit(t, f.wt, "reset", "-q", "--hard", f.head)
	zzGit(t, f.wt, "checkout", "-q", "-b", "side")
	zzGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "side")
	zzGit(t, f.wt, "checkout", "-q", "factory/t-0001")
	f.wantKept(t, "branch side")
	zzGit(t, f.wt, "branch", "-q", "-D", "side")
	zzWrite(t, filepath.Join(f.wt, "a.txt"), "changed\n")
	zzGit(t, f.wt, "stash", "-q")
	f.wantKept(t, "stash")
}

func TestCleanClosedPRRemoves(t *testing.T) {
	f := newCleanFix(t)
	zzGit(t, f.wt, "push", "-q", "origin", "--delete", "factory/t-0001")
	f.exec(t, `UPDATE tasks SET status='cancelled', pr_url='https://github.com/zz-test/www/pull/1'`)
	f.wantKept(t, "not on origin") // the PR state cannot be read: keep
	f.r.Tools.GH = func(string, ...string) (int, string, string) {
		return 0, `{"state": "CLOSED", "headRefOid": "` + f.head + `"}`, ""
	}
	zzGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "after the PR was closed")
	f.wantKept(t, "not on origin")
	zzGit(t, f.wt, "reset", "-q", "--hard", f.head)
	f.clean(t, false)
	if exists(f.wt) {
		t.Fatal("worktree of a closed PR stays")
	}
}

func TestCleanKeepsLiveCwd(t *testing.T) {
	f := newCleanFix(t)
	sub := filepath.Join(f.wt, ".hivemill")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	zzWrite(t, filepath.Join(f.wt, ".git", "info", "exclude"), "/.hivemill/\n")
	cmd := exec.Command("sleep", "60")
	cmd.Dir = sub
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	f.wantKept(t, "has its cwd inside")
}

func TestCleanKeepsWhenAProcessCannotBeChecked(t *testing.T) {
	f := newCleanFix(t)
	for out, want := range map[string]string{
		"unread zz-editor\ndone": "cannot read the cwd of process zz-editor",
		"cwd /\n":                "cannot check the processes", // cut short
	} {
		f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { return 0, out }
		f.wantKept(t, want)
	}
	// a session machine sharing the home is scanned too
	f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { return 0, "unread sshd-session\ndone" }
	if err := f.s.SetCfg("session_machines", "zz-worker,local"); err != nil {
		t.Fatal(err)
	}
	f.r.Tools.HC = func(...string) (int, string, string) { return 1, "", "down" }
	f.wantKept(t, "cannot check the processes on zz-worker")
}

func TestCleanKeepsRecent(t *testing.T) {
	f := newCleanFix(t)
	f.exec(t, `UPDATE tasks SET updated=?`, time.Now().Add(-time.Hour).Format("2006-01-02T15:04:05-07:00"))
	f.wantKept(t, "less than 24 hours ago")
	f.exec(t, `UPDATE tasks SET status='in_progress', updated='2020-01-01T00:00:00+00:00'`)
	f.wantKept(t, "task is in_progress")
	f.exec(t, `UPDATE tasks SET status='done'`)
	if err := f.s.PutRun(&Run{Task: "T-0001", Goal: "G-001", State: Working}); err != nil {
		t.Fatal(err)
	}
	f.wantKept(t, "a run of the task is working")
}

// project is the Claude project directory of the fixture worktree, plus suffix.
func (f *cleanFix) project(suffix string) string {
	return filepath.Join(f.r.Env.UserHome, ".claude", "projects", projectChar.ReplaceAllString(f.wt, "-")+suffix)
}

// oldTranscripts adds the transcripts of a run that ended 40 days ago.
func (f *cleanFix) oldTranscripts(t *testing.T) {
	t.Helper()
	old := time.Now().Add(-40 * 24 * time.Hour)
	f.transcript = filepath.Join(f.project(""), "s1", "subagents", "agent-a.jsonl")
	f.otherTranscript = filepath.Join(f.project("1"), "s2.jsonl") // another task
	// the same goal and task of another factory home
	otherHome := filepath.Join(f.r.Env.UserHome, ".claude", "projects", "-zz-factory-worktrees-G-001-T-0001", "s3.jsonl")
	zzWrite(t, otherHome, "{}\n")
	if err := os.Chtimes(otherHome, old, old); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !exists(otherHome) {
			t.Error("a transcript of another factory home was gzipped")
		}
	})
	for _, p := range []string{f.transcript, f.otherTranscript} {
		zzWrite(t, p, strings.Repeat(`{"type":"user"}`+"\n", 1000))
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.PutRun(&Run{Task: "T-0001", Goal: "G-001", State: Failed, Ended: old.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanDryRunListsOnly(t *testing.T) {
	f := newCleanFix(t)
	f.oldTranscripts(t)
	cache := filepath.Join(f.r.Env.UserHome, ".cache", "go-build")
	zzWrite(t, filepath.Join(cache, "trim.txt"), "0")
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, p := range []string{filepath.Join(cache, "trim.txt"), cache} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	f.r.DiskFree = func(string) (uint64, uint64, error) { return 100, 5, nil }
	rep := f.clean(t, true)
	var out bytes.Buffer
	rep.Print(&out)
	for _, want := range []string{"would remove worktrees " + f.wt, "would remove gocache " + cache,
		"would gzip transcripts " + f.transcript, "MB to free: worktrees "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if !exists(f.wt) || !exists(f.transcript) || exists(f.transcript+".gz") || !exists(filepath.Join(cache, "trim.txt")) {
		t.Error("a dry run changed files")
	}
	if all, _ := f.r.Log.Read(); len(all) != 0 || f.s.Meta("cleaned") != "" {
		t.Errorf("a dry run recorded something: %+v", all)
	}
}

func TestCleanTrimsIdleGoCachesOnAFullDisk(t *testing.T) {
	f := newCleanFix(t)
	home := f.r.Env.UserHome
	idle, busy := filepath.Join(home, ".cache", "go", "build"), filepath.Join(home, ".cache", "go", "worker-build")
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, d := range []string{idle, busy} {
		zzWrite(t, filepath.Join(d, "trim.txt"), "0")
	}
	for _, p := range []string{filepath.Join(idle, "trim.txt"), idle} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	var cleaned []string
	f.r.Tools.Sh = func(_ string, _ time.Duration, env []string, argv ...string) (int, string) {
		if len(argv) == 3 && argv[1] == "clean" && argv[2] == "-cache" {
			cleaned = append(cleaned, env[len(env)-1])
		}
		return 0, "done"
	}
	f.clean(t, false)
	if len(cleaned) != 0 {
		t.Fatalf("trimmed a disk below the threshold: %v", cleaned)
	}
	f.r.DiskFree = func(string) (uint64, uint64, error) { return 100, 15, nil }
	f.clean(t, false)
	if len(cleaned) != 1 || cleaned[0] != "GOCACHE="+idle {
		t.Fatalf("go clean ran for %v, want only %s", cleaned, idle)
	}
}

func TestTranscriptGzipReadable(t *testing.T) {
	f := newCleanFix(t)
	f.oldTranscripts(t)
	want, err := os.ReadFile(f.transcript)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(f.transcript)
	rep := f.clean(t, false)
	if exists(f.transcript) || !exists(f.otherTranscript) {
		t.Fatalf("wrong files gzipped: %+v", rep.Removed)
	}
	after, err := os.Stat(f.transcript + ".gz")
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("archive: %v, mtime must be kept", err)
	}
	in, err := os.Open(f.transcript + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(zr); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("content differs: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(f.transcript)); len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func TestCleanGzippedTranscriptStillPriced(t *testing.T) {
	f := newCleanFix(t)
	f.oldTranscripts(t)
	main := filepath.Join(f.project(""), "s1.jsonl")
	zzWrite(t, main, `{"type":"assistant","timestamp":"2026-01-02T03:04:05Z","requestId":"r1","message":{"id":"m1",`+
		`"model":"claude-sonnet-5-5","usage":{"input_tokens":1000,"output_tokens":1000}}}`+"\n")
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(main, old, old); err != nil {
		t.Fatal(err)
	}
	want := SessionCosts(f.r.Env.UserHome, []string{"s1"})["s1"]
	if want <= 0 {
		t.Fatalf("the plain transcript has no cost: %v", want)
	}
	f.clean(t, false)
	if exists(main) || !exists(main+".gz") {
		t.Fatal("the main transcript was not gzipped")
	}
	if got := SessionCosts(f.r.Env.UserHome, []string{"s1"})["s1"]; got != want {
		t.Errorf("cost after gzip %v, want %v", got, want)
	}
}

func TestCleanDue(t *testing.T) {
	r, s := testRunner(t, "")
	now := time.Now()
	if !r.CleanDue(now) {
		t.Error("never cleaned: due")
	}
	_ = s.SetMeta("cleaned", "100")
	if r.CleanDue(time.Unix(100+3599, 0)) || !r.CleanDue(time.Unix(100+3600, 0)) {
		t.Error("default is every 60 minutes")
	}
	_ = s.SetCfg("clean_every_min", "off")
	if r.CleanDue(now) {
		t.Error("off")
	}
}

func TestCleanKeepsABranchDeletedOnOrigin(t *testing.T) {
	f := newCleanFix(t)
	zzGit(t, f.wt, "checkout", "-q", "-b", "side")
	zzGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "side")
	zzGit(t, f.wt, "push", "-q", "origin", "side")
	zzGit(t, f.wt, "checkout", "-q", "factory/t-0001")
	zzGit(t, f.origin, "branch", "-q", "-D", "side") // the remote-tracking ref stays in the clone
	f.wantKept(t, "branch side")
}

func TestCleanRemovesReadOnlyDirectories(t *testing.T) {
	f := newCleanFix(t)
	zzWrite(t, filepath.Join(f.wt, ".git", "info", "exclude"), "/cache/\n")
	dir := filepath.Join(f.wt, "cache", "mod")
	zzWrite(t, filepath.Join(dir, "x.go"), "x\n")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	f.clean(t, false)
	if exists(f.wt) {
		t.Fatal("worktree with a read-only directory stays")
	}
}

func TestCleanFinishesAnInterruptedGzip(t *testing.T) {
	f := newCleanFix(t)
	f.oldTranscripts(t)
	plain, err := os.ReadFile(f.transcript)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(f.transcript)
	if err != nil {
		t.Fatal(err)
	}
	f.clean(t, false)
	zzWrite(t, f.transcript, string(plain)) // cut short before the plain file was removed
	if err := os.Chtimes(f.transcript, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	f.clean(t, false)
	if exists(f.transcript) || !exists(f.transcript+".gz") {
		t.Fatal("the plain copy of an archive stays")
	}
	zzWrite(t, f.transcript, "other\n")
	if err := os.Chtimes(f.transcript, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	f.clean(t, false)
	if !exists(f.transcript) {
		t.Fatal("a transcript that differs from the archive was removed")
	}
}
