package spawn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EffortLevels is EFFORT_LEVELS: the values config opus_effort may take.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ResumePromptFlags is RESUME_PROMPT_FLAGS: a resumed session rebuilds its system prompt from the prompt file on
// every request.
var ResumePromptFlags = []string{"--system-prompt-snapshot", "off"}

// scrubTermsRoles is SCRUB_TERMS_ROLES: the roles that run owner-term scans.
var scrubTermsRoles = []string{"coder", "reviewer", "qa", "judge"}

// EffortArgs is effort_args(): ["--effort", level] for an Opus model, [] otherwise; an invalid level is a refusal.
func EffortArgs(level, model string) ([]string, *Refusal) {
	level = pyStrip(level)
	if level != "" && !inList(EffortLevels, level) {
		return nil, refuse(1, "config opus_effort must be one of %s or empty, got %s: fix it with `factory config opus_effort medium`",
			strings.Join(EffortLevels, ", "), pyRepr(level))
	}
	if level == "" || !strings.HasPrefix(model, "claude-opus-") {
		return nil, nil
	}
	return []string{"--effort", level}, nil
}

// SandboxWanted is sandbox_wanted(): whether the role's claude runs in the worker sandbox.
func SandboxWanted(role string, optin bool, sandboxRoles string) bool {
	if role == "researcher" {
		return true
	}
	return role != "evaluator" && role != OnDemand && (inList(csv(sandboxRoles), role) || (optin && inList(Sandboxable, role)))
}

// settingsEnv is the "env" object of a rendered settings text, in file order, values as Python str() prints them.
func settingsEnv(text string) ([]KV, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("settings JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil // a settings file that is not an object has no env
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("settings JSON: %w", err)
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("settings JSON key %s: %w", key, err)
		}
		if key == "env" {
			return orderedStrings(raw)
		}
	}
	return nil, nil
}

func orderedStrings(raw json.RawMessage) ([]KV, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("settings env: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil // null or a non-object: Python's `or {}` / .items() path is not reached with strings
	}
	var out []KV
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("settings env: %w", err)
		}
		var v any
		if err := dec.Decode(&v); err != nil && err != io.EOF {
			return nil, fmt.Errorf("settings env value: %w", err)
		}
		key, _ := kt.(string)
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case bool:
			s = map[bool]string{true: "True", false: "False"}[x]
		case nil:
			s = "None"
		default:
			return nil, fmt.Errorf("settings env %s: value %v is not a string, bool or null", key, v)
		}
		out = append(out, KV{key, s})
	}
	return out, nil
}

func setdefault(env []KV, k, v string) []KV {
	for _, e := range env {
		if e.Key == k {
			return env
		}
	}
	return append(env, KV{k, v})
}

func setKV(env []KV, k, v string) []KV {
	for i := range env {
		if env[i].Key == k {
			env[i].Value = v
			return env
		}
	}
	return append(env, KV{k, v})
}

// planStart is the pure part of _spawn_start(): model, effort, runtime, settings, env, cwd, brief, kickoff and the
// claude argv. The sandbox PATH, the worker gh credential env, the worktree and every pane/DB step stay with the caller.
func planStart(req Request, f Facts, p SpawnPlan, rt string, sandboxOptin bool, resume string, fresh bool) (*Start, *Refusal) {
	role, name, org := req.Role, p.Name, p.Org
	st := &Start{Runtime: rt}
	st.Model = req.Model
	if st.Model == "" {
		st.Model = RoleModel(role, org, f.Config)
	}
	if strings.Contains(strings.ToLower(st.Model), "fable") {
		return nil, refuse(3, "model %s is a Fable model; the factory never uses Fable", st.Model)
	}
	effort, r := EffortArgs(f.Config["opus_effort"], st.Model)
	if r != nil {
		return nil, r
	}
	st.Effort = effort
	settings, sr := SettingsText(f.RoleSettingsTemplate, filepath.Join(f.RolesDir, "settings", role+".json.in"), f.Tokens)
	if sr != nil {
		why := sr.Msg
		if rs := []rune(why); len(rs) > 300 {
			why = string(rs[:300])
		}
		return nil, &Refusal{Msg: fmt.Sprintf("settings render failed for %s: %s; nothing was started", role, why), Code: 3, Reason: ReasonSettings}
	}
	st.SettingsText = settings
	if role == "evaluator" {
		st.SettingsText, st.EvaluatorSettings = "", true
	}
	st.SettingsPath = filepath.Join(f.RunDir, "settings", role+".json")
	st.Sandboxed = SandboxWanted(role, sandboxOptin, f.Config["sandbox_roles"])
	st.GhCred = !st.Sandboxed && inList(csv(f.Config["worker_gh_roles"]), role)

	cwd := req.Cwd
	if role == "researcher" { // always validated, also for an explicit --cwd
		if cwd != "" && cwd != f.ResearchDir {
			return nil, refuse(4, "researcher cwd must be its research dir %s, not %s; drop --cwd (or fix the recorded cwd)", f.ResearchDir, cwd)
		}
		cwd = f.ResearchDir
	}
	if cwd == "" {
		if role == "coder" || role == "qa" || role == "reviewer" {
			st.NeedWorktree = true
		} else {
			cwd = f.Home
		}
	}
	st.Cwd = cwd

	env := []KV{{"FACTORY_AGENT", name}, {"FACTORY_ROLE", role}, {"FACTORY_HOME", f.Home}}
	if terms := pyStrip(f.Config["scrub_terms_file"]); terms != "" && inList(scrubTermsRoles, role) {
		env = append(env, KV{"SCRUB_TERMS_FILE", terms})
	}
	if role == "evaluator" {
		for _, kv := range req.EnvExtra {
			env = setKV(env, kv.Key, kv.Value)
		}
	} else {
		env = setKVs(env, workerCacheEnv()) // T-1698: Go and pnpm scratch on disk, never the RAM-backed /tmp
	}
	if rt != "claude" && role == "evaluator" {
		return nil, refuse(3, "runtime %s: evaluator/skilleval stays on Claude", rt)
	}
	senv, err := settingsEnv(settings)
	if err != nil {
		return nil, &Refusal{Msg: fmt.Sprintf("settings render failed for %s: %v; nothing was started", role, err), Code: 3, Reason: ReasonSettings}
	}
	st.EnvBase = len(env)
	for _, kv := range senv {
		env = setdefault(env, kv.Key, kv.Value)
	}
	st.Env = env
	st.PromptPath = filepath.Join(f.RunDir, name+".prompt.md")
	st.PromptText = Brief(f.CommonText, f.RoleText, org, f.EnabledOrgs > 1)
	st.Kickoff = Kickoff(role, name, p.Goal, p.Team, p.Task, req.Prompt)
	if rt != "claude" {
		return st, nil
	}

	settingsPath, promptPath := st.SettingsPath, st.PromptPath
	if role == "evaluator" { // the live home is hidden in the sandbox: claude reads copies kept in the trial
		fh, _ := kvGet(req.EnvExtra, "FACTORY_HOME")
		rdir := filepath.Join(filepath.Dir(fh), "role")
		settingsPath, promptPath = filepath.Join(rdir, "settings.json"), filepath.Join(rdir, "prompt.md")
		st.SettingsPath, st.PromptPath = settingsPath, promptPath
	}
	a := []string{"--model", st.Model, "--permission-mode", "auto", "--settings", settingsPath,
		"--append-system-prompt-file", promptPath}
	if role != "evaluator" {
		a = append(a, "--add-dir", f.Home)
	}
	a = append(a, "-n", name)
	a = append(a, effort...)
	if role != "boss" { // the owner works in the boss pane with the claude.ai connectors; everyone else loads no MCP servers
		a = append(a, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	}
	if f.RolePluginDirExists && role != "evaluator" {
		a = append(a, "--plugin-dir", filepath.Join(f.RolesDir, "plugins", role))
	} else {
		for _, d := range req.PluginDirs {
			a = append(a, "--plugin-dir", d)
		}
	}
	if role == "coder" && p.Goal != "" && goalIDRE.MatchString(p.Goal) && f.GoalPluginDir != "" {
		a = append(a, "--plugin-dir", f.GoalPluginDir)
	}
	if resume != "" {
		a = append(a, "--resume", resume)
		a = append(a, ResumePromptFlags...)
	} else {
		a = append(a, "--session-id", req.SessionID)
		if fresh {
			a = append(a, ResumePromptFlags...)
		}
	}
	st.ClaudeArgs = a
	return st, nil
}

// workerCacheEnv is worker_cache_env (T-1698): disk-backed Go and pnpm caches under ~/.cache. The go tmp dir is created
// because go refuses a missing GOTMPDIR; a failure is ignored, the worker's first go call then says so itself.
func workerCacheEnv() []KV {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	cache := filepath.Join(home, ".cache")
	_ = os.MkdirAll(filepath.Join(cache, "go", "tmp"), 0o755)
	return []KV{
		{"GOTMPDIR", filepath.Join(cache, "go", "tmp")},
		{"GOCACHE", filepath.Join(cache, "go", "build")},
		{"GOMODCACHE", filepath.Join(cache, "go", "path", "pkg", "mod")},
		{"npm_config_store_dir", filepath.Join(cache, "pnpm-store")},
	}
}

func setKVs(env []KV, kvs []KV) []KV {
	for _, kv := range kvs {
		env = setKV(env, kv.Key, kv.Value)
	}
	return env
}
