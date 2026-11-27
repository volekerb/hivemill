package guard

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Fixtures of the guard2 port (T-1920): the cases of go/internal/factorytest/guard2 ran the built worker-guard binary
// with the hook payload on stdin; here the same payload goes to HookMain in-process, with the environment and the
// working directory the subprocess had, and both are restored after the call.

// g2Res is the outcome of one hook call.
type g2Res struct {
	code   int
	stderr string
}

// g2Opts configures one hook call. The environment is the test process's minus drop, then set is applied; cwd "" keeps
// the test's own working directory.
type g2Opts struct {
	args []string // e.g. {"--role", "coder"}
	cwd  string
	drop []string
	set  map[string]string
	pw   string // "" = the account's passwd home; else the guard runs with a host built for this home (g2InstallHost)
}

// g2Root is the checkout root (this package is go/internal/guard). It is read once, before any call changes the cwd.
var g2Root string

func g2RepoRoot(t testing.TB) string {
	t.Helper()
	if g2Root == "" {
		g2Root = rpRepoRoot(t)
	}
	return g2Root
}

// g2HookRaw calls HookMain with data on stdin, as the shim does (argv plus --py-hook), and restores the environment,
// the cwd and the hook state afterwards.
func g2HookRaw(t testing.TB, data string, o g2Opts) g2Res {
	t.Helper()
	root := g2RepoRoot(t)
	saved := os.Environ()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		os.Clearenv()
		for _, kv := range saved {
			k, v, _ := strings.Cut(kv, "=")
			os.Setenv(k, v)
		}
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd %s: %v", wd, err)
		}
		resetHookState()
	}()
	for _, k := range o.drop {
		os.Unsetenv(k)
	}
	for k, v := range o.set {
		os.Setenv(k, v)
	}
	if o.cwd != "" {
		if err := os.Chdir(o.cwd); err != nil {
			t.Fatalf("chdir %s: %v", o.cwd, err)
		}
	}
	resetHookState()
	argv := append(append([]string{}, o.args...), "--py-hook", filepath.Join(root, "roles", "hooks", "worker-guard.py"))
	var so, se bytes.Buffer
	var code int
	if o.pw == "" {
		code = HookMain(argv, strings.NewReader(data), &so, &se)
	} else {
		code = g2HookMainIn(t, o.pw, argv, data, &so, &se)
	}
	return g2Res{code: code, stderr: strings.ReplaceAll(se.String(), "worker-guard(go):", "worker-guard:")}
}

func g2HookJSON(t testing.TB, payload map[string]any, o g2Opts) g2Res {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return g2HookRaw(t, string(b), o)
}

// g2Bash runs a Bash tool payload.
func g2Bash(t testing.TB, cmd string, o g2Opts) g2Res {
	t.Helper()
	return g2HookJSON(t, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}, o)
}

func g2RoleArgs(role string) []string { return []string{"--role", role} }

// g2GuardDB creates <home>/db/factory.db with tasks(id, worktree) rows and writes the run/guard-export.json the guard
// reads. rows maps task id to worktree.
func g2GuardDB(t testing.TB, home string, rows map[string]string) {
	t.Helper()
	g2Mkdirs(t, filepath.Join(home, "db"), filepath.Join(home, "run"))
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
	if err := os.WriteFile(filepath.Join(home, "run", "guard-export.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// g2Git runs git with a hermetic identity and no user or system config in dir (runGit of pthelpers_test.go); the
// variables are set only for the git call, so the hook calls see the environment the subprocess hook saw.
func g2Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	keys := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"}
	prev := map[string]*string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			prev[k] = &v
		} else {
			prev[k] = nil
		}
		os.Unsetenv(k)
	}
	defer func() {
		for k, v := range prev {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}()
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false",
		"-c", "protocol.file.allow=always"}, args...)
	return runGit(t, dir, full...)
}

func g2Mkdirs(t testing.TB, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// g2RealTemp is t.TempDir with symlinks resolved (the guard compares real paths).
func g2RealTemp(t testing.TB) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func g2WantExit(t testing.TB, what string, r g2Res, code int) {
	t.Helper()
	if r.code != code {
		t.Errorf("%s: exit %d, want %d (stderr %q)", what, r.code, code, r.stderr)
	}
}

// g2InstallHost makes the guard resolve ~ to pw instead of the account's passwd home: it caches a host built for pw
// (getHost returns the cache), and t.Cleanup drops it. HookMain resets the cache (SetHookPath), so a call that needs this
// host goes through g2HookMainIn (g2Opts.pw).
func g2InstallHost(t testing.TB, pw string) {
	t.Helper()
	hostMu.Lock()
	hostCache, hostErr = buildHost(pw, os.Getuid(), HookPath), nil
	hostMu.Unlock()
	t.Cleanup(resetHost)
}

// g2HookMainIn is HookMain for a payload g2HookRaw has already checked to be a JSON object, with the host of pw
// installed after the --py-hook argument is taken (SetHookPath drops the cache) and before the tool is dispatched.
func g2HookMainIn(t testing.TB, pw string, argv []string, data string, stdout, stderr io.Writer) (code int) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "worker-guard(go): internal error: %v\n", r)
			code = 2
		}
	}()
	hookPath, argv := splitPyHook(argv)
	SetHookPath(hookPath)
	role, hq := ParseHookArgs(argv)
	g2InstallHost(t, pw)
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil || payload == nil {
		t.Fatalf("g2HookMainIn: payload is not a JSON object: %q", data)
	}
	tool, _ := payload["tool_name"].(string)
	return dispatch(tool, payload, role, hq, stderr)
}

// g2TempPw is a temp passwd home (symlinks resolved) holding .local/bin/<name> = body for each entry of bins, mode 0755.
func g2TempPw(t testing.TB, bins map[string]string) string {
	t.Helper()
	pw := g2RealTemp(t)
	bin := filepath.Join(pw, ".local", "bin")
	g2Mkdirs(t, bin)
	for name, body := range bins {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return pw
}
