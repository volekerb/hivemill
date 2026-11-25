package hm

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factory/go/internal/shlex"
)

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RenderSettings substitutes the @FACTORY_* tokens of roles/hivemill/<role>.json.in and writes the result under the
// state dir. A leftover token or invalid JSON refuses the launch.
func RenderSettings(e Env, role string) (string, error) {
	src := filepath.Join(e.Home, "roles", "hivemill", role+".json.in")
	b, err := readRoleFile(src) // the home's copy, else the built-in one (standalone install)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", src, err)
	}
	text := string(b)
	for k, v := range map[string]string{"@FACTORY_LIVE_HOME@": e.Home, "@FACTORY_HOME_DIR@": e.UserHome,
		"@FACTORY_USER_CONFIG@": filepath.Join(e.UserHome, ".config")} {
		q, _ := json.Marshal(v)
		text = strings.ReplaceAll(text, k, string(q[1:len(q)-1]))
	}
	if strings.Contains(text, "@FACTORY_") {
		return "", fmt.Errorf("%s has an unknown @FACTORY_ token", src)
	}
	if text, err = standaloneGuard(text, e.Home); err != nil { // a home without the guard shim: `hivemill guard`
		return "", err
	}
	if !json.Valid([]byte(text)) {
		return "", fmt.Errorf("%s renders to invalid JSON", src)
	}
	dir := filepath.Join(e.StateDir(), "settings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, role+".json")
	return dst, writeFile(dst, text, 0o644)
}

// TrustClone pre-registers workspace trust for a clone under <home>/worktrees in ~/.claude.json, so no trust dialog
// blocks the session. Only that project's entry is added; numbers keep their exact text.
func TrustClone(e Env, clone string) error {
	wt := filepath.Join(e.Home, "worktrees") + string(os.PathSeparator)
	if !strings.HasPrefix(clone, wt) {
		return fmt.Errorf("refused: %s is not under %s", clone, wt)
	}
	path := filepath.Join(e.UserHome, ".claude.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	cur := map[string]any{}
	if err := dec.Decode(&cur); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	projects, _ := cur["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	if _, ok := projects[clone]; ok {
		return nil
	}
	projects[clone] = map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}
	cur["projects"] = projects
	out, err := json.MarshalIndent(cur, "", " ")
	if err != nil {
		return err
	}
	return writeFile(path, string(out)+"\n", 0o600)
}

// Launch is one interactive Claude Code session started in an hc terminal (never `claude -p`).
type Launch struct {
	Role, Name, Cwd, Model, Effort, PromptFile, Kickoff string
	Resume                                              string // a session id: relaunch it with `claude --resume`
	Env                                                 map[string]string
}

// StartSession renders the settings, writes the spawn script, starts the terminal, waits for the TUI and sends the
// kickoff. It returns the session id given to claude with --session-id, so spend maps to the task from the first request.
func (r *Runner) StartSession(run *Run, l Launch) (string, error) {
	settings, err := RenderSettings(r.Env, l.Role)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.Env.StateDir(), "spawn")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sysprompt := filepath.Join(dir, l.Name+".prompt.md")
	pb, err := readRoleFile(l.PromptFile)
	if err != nil {
		return "", err
	}
	if err := writeFile(sysprompt, string(pb), 0o600); err != nil {
		return "", err
	}
	sid := uuid4()
	args := []string{"--model", l.Model, "--effort", l.Effort, "--permission-mode", "auto", "--settings", settings,
		"--append-system-prompt-file", sysprompt, "--add-dir", r.Env.Home, "-n", l.Name,
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	if l.Resume != "" {
		sid = l.Resume // --resume keeps the session id; the SessionStart hook records any new one
		args = append(args, "--resume", l.Resume, "--system-prompt-snapshot", "off")
	} else {
		args = append(args, "--session-id", sid)
	}
	env := map[string]string{"PATH": sessionPath(r.Env.UserHome), "LANG": "C.UTF-8", "TERM": "xterm-256color", "HOME": r.Env.UserHome,
		"FACTORY_AGENT": l.Name, "FACTORY_ROLE": l.Role, "FACTORY_HOME": r.Env.Home}
	for k, v := range l.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	argv := []string{"env", "-i"}
	for _, k := range keys {
		argv = append(argv, k+"="+env[k])
	}
	argv = append(argv, "claude")
	argv = append(argv, args...)
	script := filepath.Join(dir, l.Name+".sh")
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shlex.Quote(a)
	}
	if err := writeFile(script, "#!/bin/sh\nexec "+strings.Join(quoted, " ")+"\n", 0o700); err != nil {
		return "", err
	}
	spawnArgs, _ := json.Marshal(map[string]string{"role": l.Role, "lane": "option-b", "host_backend": "hc", "model": l.Model,
		"effort": l.Effort, "runtime": "claude", "cwd": l.Cwd})
	if err := r.Store.AgentLive(l.Name, l.Role, run.Task, run.Goal, "", l.Model, sid, l.Cwd, string(spawnArgs)); err != nil {
		return "", err
	}
	if err := r.WriteGuardExport(time.Now()); err != nil { // the new agent's goal, before its first tool call
		return "", err
	}
	// Pin the terminal to this machine: an hc wrapper may place worker-named terminals elsewhere, where the hook
	// binary and the factory DB are not local.
	runArgs := []string{"run", "--bg", "--no-parent", "--name", l.Name, "--cwd", l.Cwd}
	if core := r.coreName(); core != "" {
		runArgs = append(runArgs, "--on", core)
	}
	if list, ok := Terminals(r.Tools); ok && alive(list, true, l.Name) {
		// idempotent: a terminal of this name already runs (a pass that crashed after starting it): adopt it
		return sid, nil
	}
	if rc, _, errs := r.Tools.HC(append(runArgs, "sh", script)...); rc != 0 {
		return "", fmt.Errorf("hc run %s: exit %d: %s", l.Name, rc, firstLine(errs))
	}
	wait := r.ReadyWait
	if wait == 0 {
		wait = 90 * time.Second
	}
	r.waitReady(l.Name, wait)
	if rc, _, errs := r.Tools.HC("send", l.Name, l.Kickoff); rc != 0 {
		return sid, fmt.Errorf("hc send %s: exit %d: %s", l.Name, rc, firstLine(errs))
	}
	role := "task"
	if l.Role == "task-judge" {
		role = "judge"
	}
	_ = r.Store.AddSession(Session{SID: sid, Task: run.Task, Role: role, Agent: l.Name, Model: l.Model, Effort: l.Effort, Started: nowISO()})
	_ = r.Store.Event(r.actor(), "agent", l.Name, "spawn", map[string]string{"role": l.Role, "model": l.Model, "lane": "option-b"})
	_ = r.Log.Emit("session.started", run, Event{Session: sid, Role: role, Model: l.Model, Effort: l.Effort, Data: map[string]any{"agent": l.Name}})
	return sid, nil
}

// waitReady waits (one blocking loop on the local hc listing, no network) until the terminal reports an idle agent.
func (r *Runner) waitReady(name string, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		list, ok := Terminals(r.Tools)
		for _, t := range list {
			if ok && t.Name == name && t.Agent == "claude-code" && (t.Status == "idle" || t.Status == "done") {
				return
			}
		}
		time.Sleep(r.Poll)
	}
}

// Prompt types text into a session (a warm resume: same session, same cache).
func (r *Runner) Prompt(name, text string) error {
	if rc, _, errs := r.Tools.HC("send", name, text); rc != 0 {
		return fmt.Errorf("hc send %s: exit %d: %s", name, rc, firstLine(errs))
	}
	return nil
}

// Close ends a session's terminal and marks its agents row exited.
func (r *Runner) Close(name string) {
	if name == "" {
		return
	}
	r.Tools.HC("kill", name)
	_ = r.Store.AgentEnded(name)
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

// sessionPath is the caller's PATH with ~/.local/bin first (claude, hc, factory and hivemill live there; a non-login
// shell such as ssh or systemd may not have it).
func sessionPath(userHome string) string {
	p := os.Getenv("PATH")
	if p == "" {
		p = "/usr/local/bin:/usr/bin:/bin"
	}
	local := filepath.Join(userHome, ".local", "bin")
	for _, d := range filepath.SplitList(p) {
		if d == local {
			return p
		}
	}
	return local + string(os.PathListSeparator) + p
}

// coreName is the name of the local hc core ("" when hc cannot say).
func (r *Runner) coreName() string {
	rc, out, _ := r.Tools.HC("server", "status")
	if rc != 0 {
		return ""
	}
	var st struct {
		Core struct {
			Name string `json:"name"`
		} `json:"core"`
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return ""
	}
	return st.Core.Name
}
