// Package cursorharness is the Go port of scout/cursor_harness.py: it materialises per-role Cursor policy into an
// isolated workspace and builds the Cursor agent CLI argv (docs/runtimes.md, docs/research/cursor-harness.md).
package cursorharness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"factory/go/internal/scout"
	"factory/go/internal/shlex"
)

// CursorRoles lists every factory role that may use Cursor (the skilleval evaluator stays on Claude).
var CursorRoles = map[string]bool{
	"boss": true, "director": true, "lead": true, "pm": true, "coder": true, "qa": true, "reviewer": true,
	"judge": true, "cleaner": true, "staff-engineer": true, "scout": true,
}

// HQRoles are the roles whose hooks run with --hq.
var HQRoles = map[string]bool{"boss": true, "director": true, "lead": true}

const (
	AdapterName = "cursor-guard-adapter.py"
	GuardName   = "worker-guard.py"
)

// SortedRoles is sorted(CURSOR_ROLES).
func SortedRoles() []string {
	out := make([]string, 0, len(CursorRoles))
	for r := range CursorRoles {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// PyStrip is Python str.strip(): it removes Unicode whitespace, which unicode.IsSpace plus U+001C..U+001F cover
// (strings.TrimSpace misses the four separators).
func PyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pyRepr(s string) string { return "'" + strings.ReplaceAll(s, "'", "\\'") + "'" }

// ParsePilotRoles normalises cursor_pilot_roles; empty means off. Unknown names are an error.
func ParsePilotRoles(value string) ([]string, error) {
	var names []string
	for _, x := range strings.Split(value, ",") {
		if x = PyStrip(x); x != "" {
			names = append(names, x)
		}
	}
	var bad []string
	for _, n := range names {
		if !CursorRoles[n] {
			bad = append(bad, pyRepr(n))
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("cursor_pilot_roles: %s not in cursor allow-list (%s)", strings.Join(bad, ", "),
			strings.Join(SortedRoles(), ", "))
	}
	seen, out := map[string]bool{}, []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// RoleUsesCursor is true when value lists role; a bad value counts as false.
func RoleUsesCursor(value, role string) bool {
	names, err := ParsePilotRoles(value)
	if err != nil {
		return false
	}
	for _, n := range names {
		if n == role {
			return true
		}
	}
	return false
}

// permTokenRE is re.match(r"^(Bash|Edit|Write|NotebookEdit|Read)\((.*)\)\s*$", token): `.` does not match a newline
// and `\s*$` is Python's Unicode whitespace (incl. NBSP, \v, U+001C..U+001F, U+0085) plus the optional final newline.
var permTokenRE = regexp.MustCompile(`^(Bash|Edit|Write|NotebookEdit|Read)\(([^\n]*)\)[\p{Z}\s\v\x1c-\x1f\x85]*$`)

// TranslatePermissionToken maps one Claude Code permission string to a Cursor CLI token; ok=false drops it.
func TranslatePermissionToken(token string) (string, bool) {
	if token == "" || token == "Advisor" || strings.HasPrefix(token, "Workflow(") {
		return "", false
	}
	if token == "Edit" || token == "Write" || token == "NotebookEdit" {
		return "Write(**)", true
	}
	m := permTokenRE.FindStringSubmatch(token)
	if m == nil {
		return "", false
	}
	switch m[1] {
	case "Bash":
		return "Shell(" + m[2] + ")", true
	case "Edit", "Write", "NotebookEdit":
		return "Write(" + m[2] + ")", true
	case "Read":
		return "Read(" + m[2] + ")", true
	}
	return "", false
}

// Perms is the translated {"allow": [...], "deny": [...]} object.
type Perms struct{ Allow, Deny []string }

// TranslatePermissions maps Claude settings permissions to Cursor cli.json permissions (deduplicated, in order).
func TranslatePermissions(allow, deny []string) Perms {
	tr := func(in []string) []string {
		out, seen := []string{}, map[string]bool{}
		for _, tok := range in {
			if t, ok := TranslatePermissionToken(tok); ok && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		return out
	}
	return Perms{Allow: tr(allow), Deny: tr(deny)}
}

// OMap returns the object as an ordered JSON value.
func (p Perms) OMap() scout.OMap {
	return scout.OMap{{K: "allow", V: p.Allow}, {K: "deny", V: p.Deny}}
}

// HooksDirForRepo is <repo>/roles/hooks.
func HooksDirForRepo(repoRoot string) string { return filepath.Join(repoRoot, "roles", "hooks") }

func resolve(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// AdapterCommand is the shell-quoted adapter command line of one hook event.
func AdapterCommand(hooksDir, role, event string, hq bool) string {
	parts := []string{"python3", resolve(filepath.Join(hooksDir, AdapterName))}
	if hq {
		parts = append(parts, "--hq")
	}
	parts = append(parts, "--role", role, "--event", event)
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = shlex.Quote(p)
	}
	return strings.Join(q, " ")
}

// HooksJSON is the Cursor-native hooks.json that always fails closed into the adapter.
func HooksJSON(hooksDir, role string, hq bool) scout.OMap {
	shell := AdapterCommand(hooksDir, role, "beforeShellExecution", hq)
	tool := AdapterCommand(hooksDir, role, "preToolUse", hq)
	mcp := AdapterCommand(hooksDir, role, "beforeMCPExecution", hq)
	return scout.OMap{
		{K: "version", V: 1},
		{K: "hooks", V: scout.OMap{
			{K: "beforeShellExecution", V: []any{scout.OMap{{K: "command", V: shell}, {K: "failClosed", V: true}}}},
			{K: "beforeMCPExecution", V: []any{scout.OMap{{K: "command", V: mcp}, {K: "failClosed", V: true}}}},
			{K: "preToolUse", V: []any{scout.OMap{{K: "command", V: tool}, {K: "failClosed", V: true},
				{K: "matcher", V: "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook"}}}},
		}},
	}
}

// WriteAtomic writes text to path through a temp file in the same directory.
func WriteAtomic(path, text string, mode os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), os.Getpid()))
	defer func() {
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove %s: %w", tmp, rerr)
		}
	}()
	if err := os.WriteFile(tmp, []byte(text), 0o666); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// WorktreesRoot is <home>/worktrees with home resolved.
func WorktreesRoot(home string) string { return filepath.Join(resolve(home), "worktrees") }

func isUnder(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/")
}

// IsFactoryWorktreePath is true for <home>/worktrees and anything under it.
func IsFactoryWorktreePath(home, path string) bool {
	return isUnder(resolve(path), WorktreesRoot(home))
}

// StateRoot is XDG_STATE_HOME, else ~/.local/state. getenv and home are injected.
func StateRoot(getenv func(string) string, home string) string {
	if x := getenv("XDG_STATE_HOME"); x != "" {
		return x
	}
	return filepath.Join(home, ".local", "state")
}

// NearestGitHolder is the nearest directory (path or an ancestor) with a .git entry; ok=false when none.
func NearestGitHolder(path string) (string, bool) {
	p := resolve(path)
	for d := p; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

// InsideGitWorktree is true when path or an ancestor has a .git.
func InsideGitWorktree(path string) bool { _, ok := NearestGitHolder(path); return ok }

// IsolatedPolicyDir is STATE/factory/cursor/<name> (0700), outside every git work tree and FACTORY_HOME.
func IsolatedPolicyDir(home, name, stateRoot string) (string, error) {
	homeR := resolve(home)
	ws := resolve(filepath.Join(stateRoot, "factory", "cursor", name))
	if isUnder(ws, homeR) || InsideGitWorktree(ws) {
		return "", fmt.Errorf("cursor policy dir %s is inside a git work tree or FACTORY_HOME; Cursor would not load its hooks. "+
			"Set XDG_STATE_HOME to a directory outside every git work tree (default ~/.local/state)", ws)
	}
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", ws, err)
	}
	if err := os.Chmod(ws, 0o700); err != nil {
		return "", fmt.Errorf("chmod %s: %w", ws, err)
	}
	return ws, nil
}

// PolicyWorkspace is where .cursor/ policy is written: the isolated dir for HQ and worktree cwds, else cwd.
func PolicyWorkspace(home, name, cwd, stateRoot string) (string, error) {
	homeR, cwdR := resolve(home), resolve(cwd)
	if cwdR == homeR || IsFactoryWorktreePath(homeR, cwdR) {
		return IsolatedPolicyDir(homeR, name, stateRoot)
	}
	return cwdR, nil
}

// AssertPolicyTargetAllowed refuses to materialise policy into a shared checkout or task worktree.
func AssertPolicyTargetAllowed(home, target string) error {
	homeR, pathR := resolve(home), resolve(target)
	if IsFactoryWorktreePath(homeR, pathR) {
		return fmt.Errorf("cursor policy must not be written into a factory worktree (%s); "+
			"use policy_workspace() and --add-dir for the task tree", pathR)
	}
	if isUnder(pathR, homeR) {
		return fmt.Errorf("cursor policy must not be written under FACTORY_HOME (%s); "+
			"use STATE/factory/cursor/<agent> via policy_workspace() (XDG_STATE_HOME)", pathR)
	}
	if holder, ok := NearestGitHolder(pathR); ok {
		return fmt.Errorf("cursor policy must not be written inside a git work tree (%s, git root %s); "+
			"Cursor would load hooks from the git top level instead. Use a dir outside it (XDG_STATE_HOME)", pathR, holder)
	}
	return nil
}

// AddDirRootHasPolicyLeak is true when an --add-dir root has .cursor/ or AGENTS.md.
func AddDirRootHasPolicyLeak(path string) bool {
	p := resolve(path)
	if _, err := os.Stat(filepath.Join(p, ".cursor")); err == nil {
		return true
	}
	st, err := os.Stat(filepath.Join(p, "AGENTS.md"))
	return err == nil && st.Mode().IsRegular()
}

// AssertAddDirRootsClean refuses a spawn when any --add-dir root carries pilot policy files.
func AssertAddDirRootsClean(addDirs []string) error {
	for _, raw := range addDirs {
		p := resolve(raw)
		if AddDirRootHasPolicyLeak(p) {
			return fmt.Errorf("cursor spawn must not --add-dir a root with .cursor/ or AGENTS.md (%s); "+
				"one-off owner cleanup: remove ~/factory/.cursor and ~/factory/AGENTS.md", p)
		}
	}
	return nil
}

// AddDirsFromArgv is every value that follows --add-dir.
func AddDirsFromArgv(argv []string) []string {
	out := []string{}
	for i, x := range argv {
		if x == "--add-dir" && i+1 < len(argv) {
			out = append(out, argv[i+1])
		}
	}
	return out
}

// Prepared is the status dict MaterializeCursorPolicy returns.
type Prepared struct {
	Cwd, Role                string
	HQ                       bool
	Hooks, CLI, Rule, Agents string
	Permissions              Perms
	Notes                    []string
}

// OMap is the dict in Python's key order.
func (p Prepared) OMap() scout.OMap {
	return scout.OMap{{K: "cwd", V: p.Cwd}, {K: "role", V: p.Role}, {K: "hq", V: p.HQ}, {K: "hooks", V: p.Hooks},
		{K: "cli", V: p.CLI}, {K: "rule", V: p.Rule}, {K: "agents", V: p.Agents}, {K: "permissions", V: p.Permissions.OMap()},
		{K: "notes", V: p.Notes}}
}

// MaterializeOpts are the keyword arguments of materialize_cursor_policy.
type MaterializeOpts struct {
	HooksDir     string
	Allow, Deny  []string
	SystemPrompt string
	HQ           bool
	FactoryHome  string // "" = no home check (Python: FACTORY_HOME unset)
}

// MaterializeCursorPolicy writes .cursor/{hooks.json,cli.json,rules/...} and AGENTS.md under cwd.
func MaterializeCursorPolicy(cwd, role string, o MaterializeOpts) (Prepared, error) {
	if o.FactoryHome != "" {
		if err := AssertPolicyTargetAllowed(o.FactoryHome, cwd); err != nil {
			return Prepared{}, err
		}
	}
	if err := os.MkdirAll(cwd, 0o777); err != nil {
		return Prepared{}, fmt.Errorf("create %s: %w", cwd, err)
	}
	notes := []string{}
	perms := TranslatePermissions(o.Allow, o.Deny)
	if len(perms.Deny) == 0 && len(o.Deny) > 0 {
		notes = append(notes, "all Claude deny rules dropped in translation (check Advisor/Workflow-only templates)")
	}
	cursorDir := filepath.Join(cwd, ".cursor")
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	cliPath := filepath.Join(cursorDir, "cli.json")
	rulePath := filepath.Join(cursorDir, "rules", "factory-role.mdc")
	agentsPath := filepath.Join(cwd, "AGENTS.md")

	write := func(p, text string) error { return WriteAtomic(p, text, 0o644) }
	if err := write(hooksPath, scout.Dumps(HooksJSON(o.HooksDir, role, o.HQ), 2, false)+"\n"); err != nil {
		return Prepared{}, err
	}
	cli := scout.OMap{{K: "permissions", V: perms.OMap()}, {K: "attribution", V: scout.OMap{
		{K: "attributeCommitsToAgent", V: false}, {K: "attributePRsToAgent", V: false}}}}
	if err := write(cliPath, scout.Dumps(cli, 2, false)+"\n"); err != nil {
		return Prepared{}, err
	}
	rule := "---\ndescription: Hivemill factory role prompt (cursor runtime)\nalwaysApply: true\n---\n\n" +
		strings.TrimRightFunc(o.SystemPrompt, isPySpace) + "\n"
	if err := write(rulePath, rule); err != nil {
		return Prepared{}, err
	}
	agents := fmt.Sprintf("# Factory role: %s\n\nFollow `.cursor/rules/factory-role.mdc`. Guard hooks are in `.cursor/hooks.json` "+
		"(worker-guard via %s).\n", role, AdapterName)
	if err := write(agentsPath, agents); err != nil {
		return Prepared{}, err
	}
	mcp := scout.Dumps(scout.OMap{{K: "mcpServers", V: scout.OMap{}}}, 2, false) + "\n"
	if err := write(filepath.Join(cursorDir, "mcp.json"), mcp); err != nil {
		return Prepared{}, err
	}
	notes = append(notes, "wrote empty .cursor/mcp.json; user ~/.cursor/mcp.json may still load")
	for _, f := range []struct{ name, label string }{{AdapterName, "adapter"}, {GuardName, "worker-guard"}} {
		p := filepath.Join(o.HooksDir, f.name)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			notes = append(notes, fmt.Sprintf("MISSING %s at %s", f.label, p))
		}
	}
	return Prepared{Cwd: resolve(cwd), Role: role, HQ: o.HQ, Hooks: hooksPath, CLI: cliPath, Rule: rulePath,
		Agents: agentsPath, Permissions: perms, Notes: notes}, nil
}

// FindAgentBinary is shutil.which("agent") or which("cursor-agent"); "" when neither is on PATH.
func FindAgentBinary() string {
	for _, n := range []string{"agent", "cursor-agent"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// MapModel maps a factory model id to a Cursor --model slug ("auto" when empty).
func MapModel(m string) string {
	if m = PyStrip(m); m != "" {
		return m
	}
	return "auto"
}

// ArgvOpts are the keyword arguments of build_cursor_argv.
type ArgvOpts struct {
	Cwd, Model, Prompt, Resume string
	PluginDirs, AddDirs        []string
	PrintMode, Force, Sandbox  bool
}

// BuildCursorArgv is the argv (binary first) for a pilot/headless run.
func BuildCursorArgv(o ArgvOpts) []string {
	bin := FindAgentBinary()
	if bin == "" {
		bin = "agent"
	}
	argv := []string{bin, "--workspace", resolve(o.Cwd), "--model", MapModel(o.Model), "--trust"}
	if o.PrintMode {
		argv = append(argv, "--print", "--output-format", "text")
	}
	if o.Force {
		argv = append(argv, "--force")
	}
	if o.Sandbox {
		argv = append(argv, "--sandbox", "enabled")
	}
	if o.Resume != "" {
		argv = append(argv, "--resume", o.Resume)
	}
	for _, d := range o.PluginDirs {
		argv = append(argv, "--plugin-dir", d)
	}
	for _, d := range o.AddDirs {
		argv = append(argv, "--add-dir", d)
	}
	if o.Prompt != "" {
		argv = append(argv, o.Prompt)
	}
	return argv
}

// Row is one line of the works/needs-work matrix.
type Row struct{ Item, Status, Detail string }

// WorksNeedsWork is the static matrix for `factory cursor-pilot status`.
func WorksNeedsWork() []Row {
	agent := FindAgentBinary()
	st, detail := "NEEDS_WORK", "install: curl https://cursor.com/install | bash"
	if agent != "" {
		st, detail = "WORKS", agent
	}
	return []Row{
		{"Headless agent CLI installed", st, detail},
		{"Materialize .cursor/hooks.json + cli.json + role rule", "WORKS", "scout.cursor_harness.materialize_cursor_policy; HQ and worktrees → STATE/factory/cursor/<name>"},
		{"Shell gate via beforeShellExecution → worker-guard", "WORKS", "cursor-guard-adapter.py; require failClosed:true"},
		{"Write gate via preToolUse → worker-guard", "WORKS", "adapter remaps Write/Delete path keys to file_path"},
		{"Permission deny translation Bash→Shell Edit→Write", "WORKS", "prefix rules remain weaker than the parser (same as Claude)"},
		{"All roles via agent_runtimes=*=cursor", "WORKS", "evaluator stays Claude (skilleval); others allow-listed"},
		{"factory spawn via herdr --kind cursor", "WORKS", "same place/prompt path as Claude; kind=cursor"},
		{"worker ghcred / Cursor --sandbox (no Claude nsbox)", "WORKS", "pane env gets ghcred; --sandbox enabled when sandbox_roles match"},
		{"MCP strict empty (user MCP blind)", "WORKS", "beforeMCPExecution → cursor-guard-adapter (non-boss deny; boss → worker-guard)"},
		{"Process audits expecting claude argv", "NEEDS_WORK", "claude_procs / flagless checks ignore cursor-agent (OK for pure-Cursor)"},
		{"Plugin dir .claude-plugin packs via --plugin-dir", "UNKNOWN", "Cursor has --plugin-dir; Hivemill plugin layout may need packaging tweaks"},
		{"skilleval evaluator on Cursor", "NEEDS_WORK", "evaluator stays Claude-only"},
	}
}
