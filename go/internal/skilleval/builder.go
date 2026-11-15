package skilleval

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
)

// PreflightTimeout is PREFLIGHT_TIMEOUT_S.
const PreflightTimeout = 30 * time.Second

// gitTimeout is the timeout=60 of scout/sandbox.py _git and of the `factory init` seed.
const gitTimeout = 60 * time.Second

// SandboxBuilder is the Builder of scout/sandbox.py: it creates <root>/trials/<tr id>/ with work/ (the task repo, a
// fresh git repo), fh/ (a temp FACTORY_HOME with its own DB), home/, cfg/, role/ and bin/, and the evaluator shim.
// The evaluator only ever runs through that shim. Nothing here reads the eval token (check_token only stats it);
// third-party files are copied as data and never executed.
type SandboxBuilder struct {
	TasksDir string
	// FactoryInit is the argv that runs `factory init` (own interpreter, own DB) with FACTORY_HOME set by Build.
	FactoryInit []string
	// WriteShim writes <trial>/bin/claude and the read-only factory CLI snapshot and returns the shim path. The
	// shim writer is not part of this package; until it is wired (P4-L) Build refuses instead of starting an
	// evaluator without it.
	WriteShim func(trialDir, realClaude, tokenFile, home, trialsRoot string, ns *sandbox.Cfg, envSet map[string]string) (string, error)
	// Seams for tests (zero values mean the live defaults).
	Seams sandbox.Opts
}

func refusef(format string, a ...any) error {
	return fmt.Errorf("%w: %s", sandbox.ErrSandboxRefused, fmt.Sprintf(format, a...))
}

func baseEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	return []string{"PATH=" + path}
}

func runCmd(argv, env []string, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = env, dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(errb.String()), fmt.Errorf("%s: %w", argv[0], err)
	}
	return out.String(), nil
}

func git(cwd string, args ...string) error {
	env := append(baseEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C.UTF-8")
	argv := append([]string{"git", "-C", cwd, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}, args...)
	if stderr, err := runCmd(argv, env, ""); err != nil {
		return refusef("git %s failed in %s: %s", args[0], cwd, stderr)
	}
	return nil
}

// copyTree is shutil.copytree(src, dst, dirs_exist_ok=True) for a repo-owned task tree: symlinks are followed.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return fmt.Errorf("relative path of %s: %w", p, err)
		}
		target := filepath.Join(dst, rel)
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		if st.IsDir() {
			if err := os.MkdirAll(target, 0o777); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if err := os.WriteFile(target, data, st.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}

type taskMeta struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Role  string `json:"role"`
}

func (b SandboxBuilder) seedFH(fh, taskDir string, meta taskMeta, agent string) error {
	if len(b.FactoryInit) == 0 {
		return refusef("factory init is not configured; the trial home cannot be seeded")
	}
	env := append(baseEnv(), "FACTORY_HOME="+fh, "HOME="+fh, "LC_ALL=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1")
	if stderr, err := runCmd(b.FactoryInit, env, ""); err != nil {
		return refusef("factory init in %s failed: %s", fh, stderr)
	}
	spec, err := os.ReadFile(filepath.Join(taskDir, "task.md"))
	if err != nil {
		return refusef("read task.md: %v", err)
	}
	const ts = "1970-01-01T00:00:00+00:00"
	db, err := sql.Open("sqlite", "file:"+FhDB(fh)+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return fmt.Errorf("open trial DB: %w", err)
	}
	defer db.Close() // seed writes are committed by their own Exec calls
	title := meta.Title
	if title == "" {
		title = meta.ID
	}
	steps := []struct {
		q string
		a []any
	}{
		{"INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", []any{"herdr_socket", sandbox.DeadSocket}},
		{"INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", []any{"git_push", "0"}},
		{"INSERT INTO goals(id,title,status,created,updated) VALUES(?,?,'active',?,?)", []any{sandbox.SeedGoal, "eval", ts, ts}},
		{"INSERT INTO tasks(id,goal_id,title,role,status,spec,acceptance,created,updated) VALUES(?,?,?,?, 'todo',?,?,?,?)",
			[]any{sandbox.SeedTask, sandbox.SeedGoal, title, meta.Role, string(spec), "see the task", ts, ts}},
		{"INSERT INTO agents(name,role,goal_id,task_id,status,started) VALUES(?,?,?,?,'live',?)",
			[]any{agent, sandbox.EvalAgentRole, sandbox.SeedGoal, sandbox.SeedTask, ts}},
		{"INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES('factory',?,'request',?,?,?,?,?)",
			[]any{agent, "kickoff: evaluator " + sandbox.SeedTask, fmt.Sprintf("Your task is %s: run `factory task show %s`.", sandbox.SeedTask, sandbox.SeedTask),
				sandbox.SeedGoal, sandbox.SeedTask, ts}},
	}
	for _, s := range steps {
		if _, err := db.Exec(s.q, s.a...); err != nil {
			return fmt.Errorf("seed trial DB: %w", err)
		}
	}
	return nil
}

// Build creates trials/<tr id>/ for the skill_trials row t. It refuses (sandbox.ErrSandboxRefused) before creating
// anything when the token file is missing or not 0600.
func (b SandboxBuilder) Build(db *sql.DB, t Trial, root, home, unshare string) (Info, error) {
	if home == "" {
		home = sandbox.RealHome()
	}
	tokenFile := home + "/.config/factory/eval-oauth-token"
	if err := sandbox.CheckToken(tokenFile); err != nil {
		return Info{}, err
	}
	realClaude := home + "/.local/bin/claude"
	cand, ok, err := scout.GetCandidate(db, t.CandidateID)
	if err != nil {
		return Info{}, fmt.Errorf("load candidate %s: %w", t.CandidateID, err)
	}
	if !ok {
		return Info{}, refusef("no candidate %s", t.CandidateID)
	}
	plugin := ""
	if t.Condition == "with" {
		if !scout.IsEvalAllowed(vcand(cand), root) {
			return Info{}, refusef("%s is not eligible for evaluation (not prompt-only, held back, or not vendored)", cand.ID)
		}
		meta, ok := scout.Vendored(root, cand.ID, t.VendorSha)
		if !ok {
			return Info{}, refusef("%s@%s has no valid vendored copy; run factory skilleval vendor", cand.ID, t.VendorSha)
		}
		plugin = sandbox.Realpath(filepath.Join(scout.VendorDir(root, cand.ID, t.VendorSha), meta.Plugin))
	}
	taskDir := filepath.Join(b.TasksDir, t.Task)
	var meta taskMeta
	raw, rerr := os.ReadFile(filepath.Join(taskDir, "meta.json"))
	if rerr == nil {
		rerr = json.Unmarshal(raw, &meta)
	}
	if rerr == nil {
		_, rerr = os.ReadFile(filepath.Join(taskDir, "task.md"))
	}
	if rerr != nil {
		return Info{}, refusef("unknown eval task %s: %v", pyRepr(t.Task), rerr)
	}
	name := sandbox.TrialName(int(t.ID))
	trialsRoot := filepath.Join(sandbox.Realpath(root), "trials")
	d := filepath.Join(trialsRoot, name)
	agent := "eval-" + name
	if _, err := os.Lstat(d); err == nil {
		if err := os.RemoveAll(d); err != nil { // a retried trial starts clean
			return Info{}, fmt.Errorf("remove old trial dir %s: %w", d, err)
		}
	}
	info, berr := b.build(d, taskDir, meta, agent, plugin, realClaude, tokenFile, home, trialsRoot, unshare)
	if berr != nil {
		removeTree(d)
		return Info{}, berr
	}
	return info, nil
}

func (b SandboxBuilder) build(d, taskDir string, meta taskMeta, agent, plugin, realClaude, tokenFile, home, trialsRoot, unshare string) (Info, error) {
	for _, sub := range []string{"work", "fh", "home", "cfg", "role", "bin"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o777); err != nil {
			return Info{}, fmt.Errorf("create %s/%s: %w", d, sub, err)
		}
	}
	rc := filepath.Join(d, "home", sandbox.HomeRC[0])
	if err := os.WriteFile(rc, []byte(sandbox.HomeRC[1]), 0o644); err != nil {
		return Info{}, fmt.Errorf("write %s: %w", rc, err)
	}
	if err := os.Chmod(rc, 0o644); err != nil {
		return Info{}, fmt.Errorf("chmod %s: %w", rc, err)
	}
	work := filepath.Join(d, "work")
	if err := copyTree(filepath.Join(taskDir, "repo"), work); err != nil { // data only; nothing is run
		return Info{}, err
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "eval"}, {"config", "user.email", "eval@localhost"},
		{"add", "-A"}, {"commit", "-q", "-m", "task baseline"}} {
		if err := git(work, args...); err != nil {
			return Info{}, err
		}
	}
	if err := b.seedFH(filepath.Join(d, "fh"), taskDir, meta, agent); err != nil {
		return Info{}, err
	}
	cj, err := json.MarshalIndent(sandbox.ClaudeJSON(work), "", "  ")
	if err != nil {
		return Info{}, fmt.Errorf("encode .claude.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(d, "cfg", ".claude.json"), cj, 0o666); err != nil {
		return Info{}, fmt.Errorf("write .claude.json: %w", err)
	}
	ns, err := sandbox.EvalNsCfg(d, home, realClaude, plugin, unshare, b.Seams)
	if err != nil {
		return Info{}, refusef("%v", err)
	}
	if b.WriteShim == nil {
		return Info{}, refusef("the evaluator shim writer is not wired in this build; nothing was started")
	}
	envSet := map[string]string{"HOME": filepath.Join(d, "home"), "FACTORY_HOME": filepath.Join(d, "fh"),
		"FACTORY_ROLE": sandbox.EvalAgentRole, "CLAUDE_CONFIG_DIR": filepath.Join(d, "cfg")}
	shim, err := b.WriteShim(d, realClaude, tokenFile, home, trialsRoot, ns, envSet)
	if err != nil {
		return Info{}, err
	}
	return Info{Dir: d, Work: work, Plugin: plugin, Agent: agent, Shim: shim, Ns: ns}, nil
}

// Preflight runs the real view (same cfg, no proxy, no token) around `claude --version`. Any hide/bind failure or a
// claude that is not reachable inside gives a refusal: the mounts happen in the pane, so this is the only place a
// failure can be seen before an agent is started.
func (b SandboxBuilder) Preflight(info Info) error {
	argv, err := sandbox.PreflightArgv(info.Ns)
	if err != nil {
		return refusef("sandbox preflight could not run: %v", err)
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		for _, p := range sandbox.EnvPass {
			if k == p {
				env = append(env, kv)
			}
		}
	}
	env = append(env, "HOME="+filepath.Join(info.Dir, "home"))
	ctx, cancel := context.WithTimeout(context.Background(), PreflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.SysProcAttr = env, info.Work, &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return refusef("sandbox preflight could not run: timed out after %d seconds", int(PreflightTimeout.Seconds()))
	case errors.As(runErr, &ee):
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return refusef("sandbox preflight failed (exit %d); nothing was started: %s", ee.ExitCode(), runePrefix(msg, 400))
	case runErr != nil:
		return refusef("sandbox preflight could not run: %v", runErr)
	}
	return nil
}
