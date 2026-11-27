package hm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ended moves the end of the fixture's task d days into the past.
func (f *cleanFix) ended(t *testing.T, d int) {
	t.Helper()
	f.exec(t, `UPDATE tasks SET updated=?`,
		time.Now().Add(-time.Duration(d)*24*time.Hour).Format("2006-01-02T15:04:05-07:00"))
}

func zzOld(t *testing.T, age time.Duration, root string) {
	t.Helper()
	when := time.Now().Add(-age)
	var paths []string
	if err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		paths = append(paths, p)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Chtimes(paths[i], when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func zzDarwin(t *testing.T) {
	old := GOOS
	GOOS = "darwin"
	t.Cleanup(func() { GOOS = old })
}

func removedPaths(rep *CleanReport, kind string) (out []string) {
	for _, x := range rep.Removed {
		if x.Kind == kind {
			out = append(out, x.Path)
		}
	}
	slices.Sort(out)
	return out
}

func TestCleanAtticSavesDirtyWorktree(t *testing.T) {
	f := newCleanFix(t)
	f.ended(t, 5)
	zzWrite(t, filepath.Join(f.wt, "c.txt"), "local commit\n")
	zzGit(t, f.wt, "add", "c.txt")
	zzGit(t, f.wt, "commit", "-q", "-m", "local only")
	zzWrite(t, filepath.Join(f.wt, "a.txt"), "changed  \n\n") // trailing blanks must survive
	zzWrite(t, filepath.Join(f.wt, "sub", "new.txt"), "untracked\n")
	head := zzGit(t, f.wt, "rev-parse", "HEAD")
	attic := filepath.Join(f.r.Env.StateDir(), "attic")
	expired := filepath.Join(attic, "T-0000.patch")
	zzWrite(t, expired, "old")
	zzOld(t, 61*24*time.Hour, expired)

	dry := f.clean(t, true)
	if !exists(f.wt) || !exists(expired) || exists(filepath.Join(attic, "T-0001.patch")) {
		t.Fatal("a dry run changed the disk")
	}
	if !slices.Contains(removedPaths(dry, cleanAttic), f.wt) {
		t.Fatalf("dry run: %+v", dry)
	}

	rep := f.clean(t, false)
	if exists(f.wt) || exists(expired) {
		t.Fatalf("worktree or expired entry still there: %+v", rep.Kept)
	}
	check := filepath.Join(t.TempDir(), "restore")
	zzGit(t, f.repo, "clone", "-q", f.repo, check)
	zzGit(t, check, "fetch", "-q", filepath.Join(attic, "T-0001.bundle"), "factory/t-0001")
	zzGit(t, check, "checkout", "-q", "--detach", head)
	zzGit(t, check, "apply", "--whitespace=nowarn", filepath.Join(attic, "T-0001.patch"))
	for name, want := range map[string]string{"a.txt": "changed  \n\n", "c.txt": "local commit\n", "sub/new.txt": "untracked\n"} {
		if got, err := os.ReadFile(filepath.Join(check, name)); err != nil || string(got) != want {
			t.Errorf("%s restored as %q (%v), want %q", name, got, err, want)
		}
	}
}

func TestCleanAtticSavesStashAndSideBranch(t *testing.T) {
	f := newCleanFix(t)
	f.ended(t, 5)
	zzGit(t, f.wt, "checkout", "-q", "-b", "side")
	zzGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "side")
	side := zzGit(t, f.wt, "rev-parse", "HEAD")
	zzGit(t, f.wt, "checkout", "-q", "factory/t-0001")
	zzWrite(t, filepath.Join(f.wt, "a.txt"), "stashed\n")
	zzGit(t, f.wt, "stash", "-q")
	stash := zzGit(t, f.wt, "rev-parse", "refs/stash")
	f.clean(t, false)
	if exists(f.wt) {
		t.Fatal("worktree still there")
	}
	heads := zzGit(t, f.repo, "bundle", "list-heads", filepath.Join(f.r.Env.StateDir(), "attic", "T-0001.bundle"))
	if !strings.Contains(heads, side) || !strings.Contains(heads, stash) {
		t.Fatalf("bundle heads lack %s or %s:\n%s", side, stash, heads)
	}
}

func TestCleanAtticFailedRun(t *testing.T) {
	f := newCleanFix(t)
	f.exec(t, `UPDATE tasks SET status='blocked'`)
	run := &Run{Task: "T-0001", Goal: "G-001", State: Failed, Worktree: f.wt, Base: "main",
		Ended: time.Now().Add(-48 * time.Hour).Format(time.RFC3339)}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	f.wantKept(t, "run failed less than")
	run.Ended = time.Now().Add(-96 * time.Hour).Format(time.RFC3339)
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	f.clean(t, false)
	if exists(f.wt) {
		t.Fatal("worktree of a long failed run stays")
	}
}

func TestCleanAtticKeepsLiveCwd(t *testing.T) {
	f := newCleanFix(t)
	f.ended(t, 5)
	zzWrite(t, filepath.Join(f.wt, "new.txt"), "work\n")
	cmd := exec.Command("sleep", "60")
	cmd.Dir = f.wt
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	f.wantKept(t, "has its cwd inside")
	if exists(filepath.Join(f.r.Env.StateDir(), "attic", "T-0001.patch")) {
		t.Error("a kept worktree was saved")
	}
}

// zzGoCache is a build cache of the factory with one sparse file of size bytes.
func zzGoCache(t *testing.T, r *Runner, size int64) string {
	t.Helper()
	dir := filepath.Join(r.Env.UserHome, ".cache", "hivemill", "go", "build")
	zzWrite(t, filepath.Join(dir, "trim.txt"), "0")
	zzWrite(t, filepath.Join(dir, "blob"), "")
	if err := os.Truncate(filepath.Join(dir, "blob"), size); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCleanGoCacheCapSkipsWhileGateRuns(t *testing.T) {
	f := newCleanFix(t)
	dir := zzGoCache(t, f.r, 13<<30)
	var cleaned []string
	sh := f.r.Tools.Sh
	f.r.Tools.Sh = func(d string, timeout time.Duration, env []string, argv ...string) (int, string) {
		if len(argv) == 3 && argv[1] == "clean" && argv[2] == "-cache" {
			cleaned = append(cleaned, env[len(env)-1])
			return 0, ""
		}
		return sh(d, timeout, env, argv...)
	}
	if err := f.s.PutRun(&Run{Task: "T-0009", Goal: "G-001", State: Gated, HookStarted: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	lock := f.r.lock("T-0009", false)
	if lock == nil {
		t.Fatal("no lock")
	}
	release := f.r.holdGate(&Run{Task: "T-0001"}) // a manual gate or a proof rerun holds no task lock
	if rep := f.clean(t, false); len(cleaned) != 0 || !slices.ContainsFunc(rep.Kept, func(x CleanItem) bool {
		return x.Path == dir && strings.Contains(x.Reason, "a gate or a proof rerun runs")
	}) {
		t.Fatalf("while a rerun runs: cleaned %v, kept %+v", cleaned, rep.Kept)
	}
	release()
	rep := f.clean(t, false)
	if len(cleaned) != 0 || !slices.ContainsFunc(rep.Kept, func(x CleanItem) bool {
		return x.Path == dir && strings.Contains(x.Reason, "the gate of T-0009 runs")
	}) {
		t.Fatalf("while the gate runs: cleaned %v, kept %+v", cleaned, rep.Kept)
	}
	lock.Close() // a killed hook leaves HookStarted, not the lock
	f.clean(t, false)
	if len(cleaned) != 1 || cleaned[0] != "GOCACHE="+dir {
		t.Fatalf("go clean ran for %v, want %s", cleaned, dir)
	}
	zzGoCache(t, f.r, 11<<30)
	f.clean(t, false)
	if len(cleaned) != 1 {
		t.Fatal("a cache below the cap was emptied")
	}
}

func TestCleanTmpLeftovers(t *testing.T) {
	f := newCleanFix(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.r.TmpRoots = []string{tmp}
	cache, err := filepath.EvalSymlinks(f.r.Env.UserHome)
	if err != nil {
		t.Fatal(err)
	}
	cache = filepath.Join(cache, ".cache")
	file := func(age time.Duration, path string) string {
		zzWrite(t, path, "x")
		zzOld(t, age, path)
		return path
	}
	dirOf := func(age time.Duration, dir string) string {
		zzWrite(t, filepath.Join(dir, "sub", "f"), "x")
		zzOld(t, age, dir)
		return dir
	}
	want := []string{
		dirOf(4*time.Hour, filepath.Join(tmp, "go-build123")),
		file(4*time.Hour, filepath.Join(tmp, "hm-leftover")),
		dirOf(8*24*time.Hour, filepath.Join(cache, "hivemill", "tmp", "zz-session")),
		dirOf(8*24*time.Hour, filepath.Join(cache, "factory-gate", "zz-old")),
	}
	busy := dirOf(4*time.Hour, filepath.Join(tmp, "lg-busy"))
	written := dirOf(4*time.Hour, filepath.Join(tmp, "wg-bin-written"))
	zzWrite(t, filepath.Join(written, "sub", "now"), "x")
	zzOld(t, 4*time.Hour, filepath.Join(written, "sub", "f"))
	open := dirOf(4*time.Hour, filepath.Join(tmp, "lg-open"))
	held, err := os.Open(filepath.Join(open, "sub", "f"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	stay := []string{busy, written, open,
		dirOf(time.Hour, filepath.Join(tmp, "hm-young")),
		dirOf(4*time.Hour, filepath.Join(tmp, "someone-elses")),
		dirOf(24*time.Hour, filepath.Join(cache, "factory-gate", "zz-recent")),
		dirOf(8*24*time.Hour, filepath.Join(cache, "factory-gate", "go-build")),
	}
	cmd := exec.Command("sleep", "60")
	cmd.Dir = busy
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	slices.Sort(want)
	if got := removedPaths(f.clean(t, true), cleanTmp); !slices.Equal(got, want) || !exists(want[0]) {
		t.Fatalf("dry run: %v, want %v", got, want)
	}
	if got := removedPaths(f.clean(t, false), cleanTmp); !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v", got, want)
	}
	for _, p := range want {
		if exists(p) {
			t.Errorf("%s stays", p)
		}
	}
	for _, p := range stay {
		if !exists(p) {
			t.Errorf("%s removed", p)
		}
	}
}

func TestCleanDerivedDataOfRemovedWorktree(t *testing.T) {
	f := newCleanFix(t)
	zzDarwin(t)
	home, err := filepath.EvalSymlinks(f.r.Env.UserHome)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")
	entry := func(name, workspace string) string {
		zzWrite(t, filepath.Join(data, name, "info.plist"),
			"<plist><dict>\n\t<key>WorkspacePath</key>\n\t<string>"+workspace+"</string>\n</dict></plist>\n")
		return filepath.Join(data, name)
	}
	worktrees := filepath.Join(f.r.Env.Home, "worktrees")
	gone := entry("App-gone", filepath.Join(worktrees, "G-001-T-0002", "ios", "App &amp; Co.xcodeproj"))
	f.exec(t, `UPDATE tasks SET status='doing'`) // its worktree stays
	present := entry("App-present", filepath.Join(f.wt, "App.xcodeproj"))
	other := entry("App-other", "/nonexistent/code/App.xcodeproj")
	unmounted := entry("App-unmounted", "/nonexistent/worktrees/G-001-T-0003/App.xcodeproj")
	rep := f.clean(t, false)
	if got := removedPaths(rep, cleanDerived); !slices.Equal(got, []string{gone}) || exists(gone) {
		t.Fatalf("removed %v, want %s", got, gone)
	}
	for _, p := range []string{present, other, unmounted} {
		if !exists(p) {
			t.Errorf("%s removed", p)
		}
	}
}

func TestCleanMaestroOutput(t *testing.T) {
	f := newCleanFix(t)
	zzDarwin(t)
	home, err := filepath.EvalSymlinks(f.r.Env.UserHome)
	if err != nil {
		t.Fatal(err)
	}
	var old, young []string
	for _, dir := range []string{"tests", "screenshots", "recordings"} {
		base := filepath.Join(home, ".maestro", dir)
		stale := filepath.Join(base, "old")
		zzWrite(t, filepath.Join(stale, "f"), "x")
		zzOld(t, 8*24*time.Hour, stale)
		fresh := filepath.Join(base, "new")
		zzWrite(t, fresh, "x")
		zzOld(t, 6*24*time.Hour, fresh)
		old, young = append(old, stale), append(young, fresh)
	}
	slices.Sort(old)
	dry := f.clean(t, true)
	if got := removedPaths(dry, cleanMaestro); !slices.Equal(got, old) || !exists(old[0]) {
		t.Fatalf("dry run: %v", got)
	}
	rep := f.clean(t, false)
	if got := removedPaths(rep, cleanMaestro); !slices.Equal(got, old) {
		t.Fatalf("removed %v, want %v", got, old)
	}
	for _, p := range old {
		if exists(p) {
			t.Errorf("%s stays", p)
		}
	}
	for _, p := range young {
		if !exists(p) {
			t.Errorf("%s removed", p)
		}
	}
}

func TestCleanSimCloneOfRemovedWorktree(t *testing.T) {
	f := newCleanFix(t)
	zzDarwin(t)
	if err := os.MkdirAll(filepath.Join(f.r.Env.UserHome, "Library", "Developer", "CoreSimulator"), 0o700); err != nil {
		t.Fatal(err)
	}
	list := `{"devices": {"iOS-18": [
		{"name": "Clone of iPhone T-0001", "udid": "U1", "state": "Shutdown"},
		{"name": "T-0001 booted", "udid": "U2", "state": "Booted"},
		{"name": "iPhone T-00012", "udid": "U3", "state": "Shutdown"},
		{"name": "iPhone 16", "udid": "U4", "state": "Shutdown"}]}}`
	var ran []string
	sh := f.r.Tools.Sh
	f.r.Tools.Sh = func(d string, timeout time.Duration, env []string, argv ...string) (int, string) {
		if argv[0] != "xcrun" {
			return sh(d, timeout, env, argv...)
		}
		if argv[2] == "list" {
			return 0, list
		}
		ran = append(ran, strings.Join(argv[2:], " "))
		return 0, ""
	}
	f.clean(t, true)
	if len(ran) != 0 {
		t.Fatalf("a dry run deleted: %v", ran)
	}
	rep := f.clean(t, false) // removes the worktree, then its clone
	if want := []string{"delete U1", "delete unavailable"}; !slices.Equal(ran, want) {
		t.Fatalf("simctl ran %v, want %v (%+v)", ran, want, rep)
	}
	if got := removedPaths(rep, cleanSim); len(got) != 1 {
		t.Errorf("report: %v", got)
	}
}

func TestHealthWarnsDiskFull(t *testing.T) {
	f := newCleanFix(t)
	f.exec(t, `UPDATE tasks SET status='doing'`)
	zzGoCache(t, f.r, 3<<30)
	disk := func() *OwnerAlert {
		checks := f.r.OwnerChecks(time.Now())
		for _, a := range checks {
			if a.Kind == "hivemill-disk" {
				return &a
			}
		}
		return nil
	}
	f.r.DiskFree = func(string) (uint64, uint64, error) { return 100, 10, nil }
	f.clean(t, false)
	a := disk()
	if a == nil {
		t.Fatal("no warning at 90% used")
	}
	for _, want := range []string{f.r.Machine() + ": 90% used", "gocache 3.0 GB", "worktrees 0.0 GB"} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("text lacks %q: %s", want, a.Text)
		}
	}
	f.r.DiskFree = func(string) (uint64, uint64, error) { return 100, 15, nil }
	f.clean(t, false)
	if a := disk(); a != nil {
		t.Errorf("warns at 85%%: %+v", a)
	}
}

// zzRemote answers `hc exec ... hivemill local clean <spec>` by running LocalClean here with its own user home.
func zzRemote(t *testing.T, f *cleanFix, userHome string, specs *[]MachineSpec) {
	t.Helper()
	sh := f.r.Tools.Sh
	f.r.Tools.Sh = func(d string, timeout time.Duration, env []string, argv ...string) (int, string) {
		if argv[0] != "hc" {
			return sh(d, timeout, env, argv...)
		}
		var spec MachineSpec
		if err := json.Unmarshal([]byte(argv[len(argv)-1]), &spec); err != nil {
			t.Fatalf("spec: %v: %v", err, argv)
		}
		*specs = append(*specs, spec)
		remote := &Runner{Env: Env{UserHome: userHome}, Tools: Tools{Sh: sh, Git: f.r.Tools.Git}, TmpRoots: []string{}, DiskFree: f.r.DiskFree}
		rep, err := remote.LocalClean(context.Background(), spec, time.Now())
		if err != nil {
			return 1, err.Error()
		}
		var out bytes.Buffer
		if err := rep.WriteLine(&out); err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	}
}

func TestCleanRunsOnSessionMachines(t *testing.T) {
	f := newCleanFix(t)
	if err := f.s.SetCfg("session_machines", "zz-worker,local"); err != nil {
		t.Fatal(err)
	}
	f.r.Tools.HC = func(...string) (int, string, string) { return 0, "done", "" }
	if err := f.s.PutRun(&Run{Task: "T-0001", Goal: "G-001", State: Merged, Machine: "zz-worker"}); err != nil {
		t.Fatal(err)
	}
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(userHome, ".cache", "factory-gate", "zz-old")
	zzWrite(t, filepath.Join(old, "f"), "x")
	zzOld(t, 8*24*time.Hour, old)
	var specs []MachineSpec
	zzRemote(t, f, userHome, &specs)
	rep := f.clean(t, false)
	if exists(old) || !slices.Contains(removedPaths(rep, cleanTmp), "zz-worker:"+old) {
		t.Fatalf("the machine was not cleaned: %+v", rep)
	}
	if len(specs) != 1 || specs[0].GoCacheMaxGB != 12 || !slices.Contains(specs[0].Ended, "T-0001") {
		t.Errorf("specs: %+v", specs)
	}

	f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { return 1, "no route" }
	rep, err = f.r.Clean(context.Background(), true, time.Now())
	if err != nil || len(rep.Skipped) != 1 || !strings.HasPrefix(rep.Skipped[0], "zz-worker: ") || len(rep.Errors) != 0 {
		t.Fatalf("unreachable machine: %v %+v", err, rep)
	}
}

func TestCleanAtticSavesStagedContent(t *testing.T) {
	f := newCleanFix(t)
	f.ended(t, 5)
	zzWrite(t, filepath.Join(f.wt, "a.txt"), "only in the index\n")
	zzGit(t, f.wt, "add", "a.txt")
	zzWrite(t, filepath.Join(f.wt, "a.txt"), "a\n")
	f.clean(t, false)
	got, err := os.ReadFile(filepath.Join(f.r.Env.StateDir(), "attic", "T-0001.staged.patch"))
	if exists(f.wt) || err != nil || !strings.Contains(string(got), "+only in the index") {
		t.Fatalf("staged content: %v %q", err, got)
	}
}

func TestCleanAtticKeepsChangedSubmodule(t *testing.T) {
	f := newCleanFix(t)
	f.ended(t, 5)
	zzGit(t, f.wt, "update-index", "--add", "--cacheinfo", "160000,"+f.head+",sub")
	if err := os.Mkdir(filepath.Join(f.wt, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.wantKept(t, "a changed submodule")
}

// A clone-mode worktree that was kept for its unpushed work, and one that is already gone.
func TestCleanAtticOnCloneMachine(t *testing.T) {
	f := newCleanFix(t)
	f.ended(t, 5)
	zzWrite(t, filepath.Join(f.wt, "new.txt"), "work\n")
	run := &Run{Task: "T-0001", Goal: "G-001", State: Failed, Machine: "zz-worker", Mirror: "/zz/mirror", Worktree: f.wt,
		Base: "main", RemoteDone: true, Ended: time.Now().Add(-96 * time.Hour).Format(time.RFC3339)}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	var specs []MachineSpec
	zzRemote(t, f, t.TempDir(), &specs)
	ask := func() *CleanReport {
		rep := &CleanReport{}
		f.r.atticMachine(context.Background(), "zz-worker", f.r.machineSpec(false, []string{"T-0001"}), []Run{*run}, rep, time.Now())
		if len(rep.Errors)+len(rep.Skipped)+len(rep.Kept) != 0 {
			t.Fatalf("%+v", rep)
		}
		got, err := f.s.Run("T-0001")
		if err != nil || !got.RemoteGone {
			t.Fatalf("the run must record that the worktree is gone: %v %+v", err, got)
		}
		return rep
	}
	if rep := ask(); exists(f.wt) || len(removedPaths(rep, cleanAttic)) != 1 {
		t.Fatalf("not removed: %+v", rep)
	}
	if rep := ask(); len(rep.Removed) != 0 { // the run as it was: the machine says the worktree is absent
		t.Fatalf("removed twice: %+v", rep)
	}
}

func TestCleanStoppedDaemonDoesNotWaitForAMachine(t *testing.T) {
	f := newCleanFix(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { <-block; return 1, "" }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.r.askMachine(ctx, "zz-worker", MachineSpec{Home: f.r.Env.Home}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
