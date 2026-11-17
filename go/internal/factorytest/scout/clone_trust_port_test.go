package scout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	sb "factory/go/internal/sandbox"
)

// Port of tests/test_clone_trust.py (11 cases: CloneTrustSpawnTests 4, CloneTrustBranchTests 5, CloneTrustCleanTests 2;
// python3 -m unittest -v reports exactly these 11, CloneCleanBase has no cases of its own; see ported_scout.txt).
// The ~/.claude.json rules are in Go (sandbox.SetCloneTrust), so the file cases call it on a temp HOME. The two
// `factory clean` cases drive the CLI as a subprocess and call e.NeedPython() (clean is still Python).
// Deviation: the clean cases run `factory clean` as a director row (not cleaner) and set terminal_backend to the TUI
//   backend: here a worker cannot call the stub (not under tests/stubs/) and hc is on PATH.
// not ported: test_foreign_uid_owner_... - patches os.fstat inside workerbox to fake another uid; Go cannot fake a
//   foreign owner of a file it just made (needs root chown); sandbox/workerbox_test.go has no such case either.
// not ported: test_a_rewrite_between_read_and_rename_is_retried_once_then_merged and
//   test_a_second_rewrite_gives_up_... - patch os.lstat so another writer rewrites the file between the read and the
//   rename; Go has no seam there (a goroutine race would be non-deterministic).
// Partly ported: the spawn glue of the Spawn cases (Python prepare_worktree / make_task_clone, the clone-trust-refused
//   event row, "the spawn goes on") is Python internals; those cases assert the file outcome and the refusal reason
//   SetCloneTrust returns, which is what the event carries. test_unusable "toolarge" writes a real file over the
//   16 MiB cap (the Python test lowers CLAUDE_JSON_MAX to 10).

var ctSeed = map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}

const ctOtherJSON = `{"theme": "dark", "numStartups": 7, "projects": {"/elsewhere/proj": {"allowedTools": ["x"]}}}`

type ctFx struct {
	t     *testing.T
	home  string // the temp HOME holding .claude.json
	wts   string // <factory home>/worktrees
	clone string
}

func ctNew(t *testing.T) *ctFx {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &ctFx{t: t, home: filepath.Join(root, "userhome"), wts: filepath.Join(root, "factory", "worktrees")}
	f.clone = filepath.Join(f.wts, "G-001-T-0001")
	for _, d := range []string{f.home, f.clone} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *ctFx) cj() string { return filepath.Join(f.home, ".claude.json") }

func (f *ctFx) write(data string) string {
	f.t.Helper()
	if err := os.WriteFile(f.cj(), []byte(data), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(f.cj(), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return f.cj()
}

func (f *ctFx) load() map[string]any {
	f.t.Helper()
	b, err := os.ReadFile(f.cj())
	if err != nil {
		f.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		f.t.Fatalf("%s: %v", f.cj(), err)
	}
	return m
}

func (f *ctFx) trust(path string, add bool) (string, string) {
	return sb.SetCloneTrust(f.home, f.wts, path, add)
}

func ctJSON(t testing.TB, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ctWant(t testing.TB, res, why, wantRes, wantWhy string) {
	t.Helper()
	// wantWhy "" = no reason (not refused); "*" = any reason (the Python test counts one refusal event)
	if res != wantRes || (wantWhy == "") != (why == "") && wantWhy != "*" || wantWhy != "*" && !strings.Contains(why, wantWhy) {
		t.Fatalf("SetCloneTrust = (%q, %q), want (%q, reason containing %q)", res, why, wantRes, wantWhy)
	}
}

func TestCloneTrustCloneAddsExactlyOneEntryAndNothingElse(t *testing.T) {
	f := ctNew(t)
	f.write(ctOtherJSON)
	res, why := f.trust(f.clone, true)
	ctWant(t, res, why, "added", "")
	want := ctJSON(t, ctOtherJSON)
	want["projects"].(map[string]any)[f.clone] = ctSeed
	if got := f.load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("~/.claude.json = %v, want %v", got, want)
	}
	if fi, _ := os.Stat(f.cj()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestCloneTrustExistingEntryLeavesTheFileUntouched(t *testing.T) {
	f := ctNew(t)
	p := f.write(fmt.Sprintf(`{"projects": {%q: {"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true, "extra": 1}}}`, f.clone))
	ts := time.Unix(1, 0)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
	snap := func() (string, int64) {
		b, _ := os.ReadFile(p)
		fi, _ := os.Stat(p)
		return string(b), fi.ModTime().UnixNano()
	}
	b0, m0 := snap()
	res, why := f.trust(f.clone, true)
	ctWant(t, res, why, "unchanged", "")
	if b1, m1 := snap(); b1 != b0 || m1 != m0 {
		t.Fatal("the file was rewritten")
	}
}

func TestCloneTrustUnusableClaudeJSONWritesNothingAndRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *ctFx)
		why   string
		check func(f *ctFx)
	}{
		{"missing", func(f *ctFx) {}, "is missing", func(f *ctFx) {
			if _, err := os.Lstat(f.cj()); err == nil {
				t.Fatal("a missing ~/.claude.json was created")
			}
		}},
		{"invalid", func(f *ctFx) { f.write("{not json") }, "not valid JSON", func(f *ctFx) {
			if b, _ := os.ReadFile(f.cj()); string(b) != "{not json" {
				t.Fatalf("invalid file rewritten: %q", b)
			}
		}},
		{"symlink", func(f *ctFx) {
			real := filepath.Join(filepath.Dir(f.home), "real.json")
			if err := os.WriteFile(real, []byte(ctOtherJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, f.cj()); err != nil {
				t.Fatal(err)
			}
		}, "symlink", func(f *ctFx) {
			b, _ := os.ReadFile(filepath.Join(filepath.Dir(f.home), "real.json"))
			if !reflect.DeepEqual(ctJSON(t, string(b)), ctJSON(t, ctOtherJSON)) {
				t.Fatalf("the symlink target was rewritten: %s", b)
			}
		}},
		{"toolarge", func(f *ctFx) {
			pad := strings.Repeat("x", sb.ClaudeJSONMax)
			f.write(`{"projects": {}, "pad": "` + pad + `"}`)
		}, "larger than", func(f *ctFx) {
			if fi, _ := os.Stat(f.cj()); fi.Size() <= sb.ClaudeJSONMax {
				t.Fatal("the oversize file was replaced")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ctNew(t)
			c.setup(f)
			res, why := f.trust(f.clone, true)
			ctWant(t, res, why, "refused", c.why)
			if _, err := os.Stat(filepath.Join(f.clone)); err != nil {
				t.Fatalf("the clone dir vanished: %v", err)
			}
			c.check(f)
		})
	}
}

func TestCloneTrustScopeRefusalsWriteNothing(t *testing.T) {
	f := ctNew(t)
	p := f.write(ctOtherJSON)
	before, _ := os.ReadFile(p)
	outside := filepath.Join(filepath.Dir(f.wts), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.wts, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, f.wts, filepath.Join(f.wts, "link", "sub"), filepath.Join(f.wts, "..", "outside")} {
		t.Run(path, func(t *testing.T) {
			res, why := f.trust(path, true)
			ctWant(t, res, why, "refused", "*")
			if after, _ := os.ReadFile(p); !bytes.Equal(after, before) {
				t.Fatal("~/.claude.json changed on a refusal")
			}
		})
	}
}

func TestCloneTrustNonRegularFileWritesNothingAndRefuses(t *testing.T) {
	for _, kind := range []string{"directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := ctNew(t)
			if kind == "directory" {
				if err := os.MkdirAll(f.cj(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.cj(), "keep"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := syscall.Mkfifo(f.cj(), 0o600); err != nil {
				t.Fatal(err)
			}
			res, why := f.trust(f.clone, true)
			ctWant(t, res, why, "refused", "not a regular file")
			fi, err := os.Lstat(f.cj())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "directory" {
				if es, _ := os.ReadDir(f.cj()); len(es) != 1 || es[0].Name() != "keep" {
					t.Fatalf("directory contents changed: %v", es)
				}
			} else if fi.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("the fifo was replaced: %v", fi.Mode())
			}
		})
	}
}

func TestCloneTrustCleanSideRefusesOutsideAndSymlinkedPathsAndRemovesNoEntry(t *testing.T) {
	f := ctNew(t)
	outside := filepath.Join(filepath.Dir(f.wts), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.wts, "link")); err != nil {
		t.Fatal(err)
	}
	linkClone := filepath.Join(f.wts, "linkclone")
	if err := os.Symlink(f.clone, linkClone); err != nil {
		t.Fatal(err)
	}
	cases := []string{outside, filepath.Join(outside, "sub"), f.wts, filepath.Join(f.wts, "link", "sub"), linkClone}
	entries := map[string]any{f.clone: ctSeed}
	for _, c := range cases {
		entries[c] = ctSeed
	}
	b, err := json.Marshal(map[string]any{"theme": "dark", "numStartups": 7, "projects": entries})
	if err != nil {
		t.Fatal(err)
	}
	p := f.write(string(b))
	before, _ := os.ReadFile(p)
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			res, why := f.trust(path, false)
			ctWant(t, res, why, "refused", "*")
			if after, _ := os.ReadFile(p); !bytes.Equal(after, before) {
				t.Fatal("an entry was removed on a refusal")
			}
		})
	}
}

// ---- CloneTrustCleanTests: `factory clean` (CLI, Python) over a real temp repo --------------------------------------

func ctGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=zz", "GIT_AUTHOR_EMAIL=zz@example.invalid", "GIT_COMMITTER_NAME=zz", "GIT_COMMITTER_EMAIL=zz@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// ctClean is CloneCleanBase setUp + clone_task: a merged task T-0001 with a pushed standalone clone.
func ctClean(t *testing.T) (e *ft.Env, wt string) {
	e = ft.New(t)
	e.NeedPython()
	e.Script("herdr", ft.StubRule{Match: []string{"pane", "list"}, Out: `{"result": {"panes": []}}` + "\n"}, // clean lists live panes and agents
		ft.StubRule{Match: []string{"agent", "list"}, Out: `{"result": {"agents": []}}` + "\n"})
	origin, repo := filepath.Join(e.Home, "origin.git"), filepath.Join(e.Home, "repo")
	ctGit(t, e.Home, "init", "-q", "--bare", "-b", "main", origin)
	ctGit(t, e.Home, "clone", "-q", origin, repo)
	ctGit(t, repo, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctGit(t, repo, "add", "README")
	ctGit(t, repo, "commit", "-q", "-m", "init")
	ctGit(t, repo, "push", "-q", "-u", "origin", "main")
	wts := filepath.Join(e.Home, "worktrees")
	if err := os.Mkdir(wts, 0o755); err != nil {
		t.Fatal(err)
	}
	adExec(t, e, "INSERT INTO projects(id,name,auto_merge,created) VALUES('zz-test-proj','zz-test-proj',1,'t')")
	adExec(t, e, "UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", repo)
	adExec(t, e, "UPDATE config SET value=? WHERE key='terminal_backend'", "her"+"dr") // the Python harness has no hc on PATH
	e.SeedGoal("G-001", "zz-test-proj", "zz-test goal")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-cleaner", Role: "director", Goal: "G-001"}) // a worker role cannot call the stub herdr here: it is not under tests/stubs/
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Status: "exited"})

	wt, branch := filepath.Join(wts, "G-001-T-0001"), "factory/t-0001"
	ctGit(t, e.Home, "-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", repo, wt)
	ctGit(t, wt, "remote", "set-url", "origin", origin)
	ctGit(t, wt, "fetch", "-q", "origin")
	ctGit(t, wt, "checkout", "-q", "-b", branch, "origin/main")
	if err := os.WriteFile(filepath.Join(wt, "f1"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctGit(t, wt, "add", "f1")
	ctGit(t, wt, "commit", "-q", "-m", "work")
	ctGit(t, wt, "push", "-q", "origin", branch)
	sha := ctGit(t, wt, "rev-parse", "HEAD")
	e.SeedTask(ft.Task{ID: "T-0001", Status: "done"})
	adExec(t, e, "UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", wt, branch)
	pr, err := json.Marshal(map[string]any{"number": 1, "state": "MERGED", "headRefOid": sha, "headRefName": branch, "baseRefName": "main",
		"mergeable": "MERGEABLE", "isDraft": false, "url": ft.PRURL, "statusCheckRollup": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	e.Script("gh", ft.StubRule{Match: []string{"pr", "view", ft.PRURL}, Out: string(pr) + "\n"})
	return e, wt
}

func ctCleanRun(t *testing.T, e *ft.Env) {
	t.Helper()
	e.OKWith(ft.Opts{Agent: "zz-test-cleaner"}, "clean")
}

func TestCloneTrustCleanRemovesOnlyTheCleanedClonesEntry(t *testing.T) {
	e, wt := ctClean(t)
	key, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	cj := filepath.Join(e.UserHome, ".claude.json")
	b, _ := json.Marshal(map[string]any{"theme": "dark", "projects": map[string]any{key: ctSeed, "/elsewhere/proj": ctSeed}})
	if err := os.WriteFile(cj, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctCleanRun(t, e)
	if _, err := os.Lstat(wt); err == nil {
		t.Fatal("the clone was not removed")
	}
	raw, _ := os.ReadFile(cj)
	want := map[string]any{"theme": "dark", "projects": map[string]any{"/elsewhere/proj": ctSeed}}
	if got := ctJSON(t, string(raw)); !reflect.DeepEqual(got, want) {
		t.Fatalf("~/.claude.json = %v, want %v", got, want)
	}
	if fi, _ := os.Stat(cj); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestCloneTrustCleanWithoutAnEntryDoesNotRewriteTheFile(t *testing.T) {
	e, wt := ctClean(t)
	cj := filepath.Join(e.UserHome, ".claude.json")
	if err := os.WriteFile(cj, []byte(ctOtherJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1, 0)
	if err := os.Chtimes(cj, ts, ts); err != nil {
		t.Fatal(err)
	}
	ctCleanRun(t, e)
	if _, err := os.Lstat(wt); err == nil {
		t.Fatal("the clone was not removed")
	}
	fi, _ := os.Stat(cj)
	if fi.ModTime().UnixNano() != 1_000_000_000 {
		t.Fatalf("mtime = %d, the file was rewritten", fi.ModTime().UnixNano())
	}
	raw, _ := os.ReadFile(cj)
	if !reflect.DeepEqual(ctJSON(t, string(raw)), ctJSON(t, ctOtherJSON)) {
		t.Fatalf("~/.claude.json = %s", raw)
	}
}
