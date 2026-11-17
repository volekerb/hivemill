// Package guard2 ports the second batch of tests/test_worker_guard_*.py (persist, push_pin, repocfg, repoprot,
// researcher, retro, retro_mirror, scratch_init, uv_wrapper) to Go. Every case drives the real entry point: the
// cmd/worker-guard binary as a subprocess, with the hook payload on stdin, and asserts exit code and stderr as the
// Python test does (the Python guard's stderr is the oracle; the Go guard differs only in the prefix of its own
// internal-error lines, "worker-guard(go):" for "worker-guard:").
package guard2

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"

	_ "modernc.org/sqlite"
)

var (
	binDir  string
	binOnce sync.Once
	binPath string
	binErr  error
)

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "guard2-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "guard2: mkdir temp:", err)
		os.Exit(1)
	}
	binDir = d
	code := m.Run()
	if err := os.RemoveAll(d); err != nil {
		fmt.Fprintln(os.Stderr, "guard2: cleanup:", err)
	}
	os.Exit(code)
}

// guardBin builds ./cmd/worker-guard once per test binary.
func guardBin(t testing.TB) string {
	t.Helper()
	binOnce.Do(func() {
		mod, err := ft.ModuleRoot()
		if err != nil {
			binErr = err
			return
		}
		goBin, err := exec.LookPath("go")
		if err != nil {
			binErr = fmt.Errorf("go not on PATH: %w", err)
			return
		}
		binPath = filepath.Join(binDir, "worker-guard")
		cmd := exec.Command(goBin, "build", "-o", binPath, "./cmd/worker-guard")
		cmd.Dir = mod
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build ./cmd/worker-guard: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("build worker-guard: %v", binErr)
	}
	return binPath
}

func repoRoot(t testing.TB) string {
	t.Helper()
	r, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// res is the outcome of one hook run.
type res struct {
	code   int
	stderr string
}

// hookOpts configures one hook run. Env starts from the process env minus Drop, then Set is applied.
type hookOpts struct {
	args []string // e.g. {"--role", "coder"}
	cwd  string
	drop []string
	set  map[string]string
}

func environ(drop []string, set map[string]string) []string {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	for k := range set {
		skip[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !skip[k] {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// hookRaw runs the Go guard with data on stdin.
func hookRaw(t testing.TB, data string, o hookOpts) res {
	t.Helper()
	bin := guardBin(t)
	py := filepath.Join(repoRoot(t), "roles", "hooks", "worker-guard.py")
	cmd := exec.Command(bin, append(append([]string{}, o.args...), "--py-hook", py)...)
	cmd.Stdin = strings.NewReader(data)
	cmd.Env = environ(o.drop, o.set)
	cmd.Dir = o.cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	timer := time.AfterFunc(30*time.Second, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run %s %v: %v", bin, o.args, err)
		}
		code = ee.ExitCode()
	}
	return res{code: code, stderr: strings.ReplaceAll(stderr.String(), "worker-guard(go):", "worker-guard:")}
}

// bash runs a Bash tool payload.
func bash(t testing.TB, cmd string, o hookOpts) res {
	t.Helper()
	return hookJSON(t, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}, o)
}

func hookJSON(t testing.TB, payload map[string]any, o hookOpts) res {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return hookRaw(t, string(b), o)
}

func roleArgs(role string) []string { return []string{"--role", role} }

// guardDB creates <home>/db/factory.db with tasks(id, worktree) rows and writes the run/guard-export.json the Go guard
// needs (tests/guard_impl.py seed_guard_export). rows maps task id to worktree.
func guardDB(t testing.TB, home string, rows map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := sql.Open("sqlite", filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)"); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	wts := map[string]string{}
	for id, wt := range rows {
		if _, err := d.Exec("INSERT INTO tasks VALUES (?, ?)", id, wt); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		if wt != "" {
			wts[id] = wt
		}
	}
	doc := map[string]any{
		"version":      1,
		"generated_at": time.Now().UTC().Format("2006-01-02T15:04:05") + "+00:00", // the guard refuses an export older than 15 min
		"config": map[string]string{"public_text_repos": "", "frozen_repos": "", "scrub_terms_file": "",
			"test_sandbox": "0", "factory_remote_url": ""},
		"agent_goals":    map[string]string{},
		"task_worktrees": wts,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "guard-export.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// git runs git with a hermetic identity in dir and fails the test on error.
func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false",
		"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = environ([]string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"},
		map[string]string{"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func mkdirs(t testing.TB, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t testing.TB, path, content string) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func realTemp(t testing.TB) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wantExit(t testing.TB, what string, r res, code int) {
	t.Helper()
	if r.code != code {
		t.Errorf("%s: exit %d, want %d (stderr %q)", what, r.code, code, r.stderr)
	}
}
