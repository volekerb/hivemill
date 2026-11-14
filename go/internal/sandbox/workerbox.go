package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"factory/go/internal/shlex"
)

// Worker profile (G-010): the per-agent claude shim and the allow-list view a sandboxed worker runs in.
//
// SECURITY CODE. The network-ON and env pass-through choices of the worker profile live here. PrepareAgent runs in
// HQ and writes <workers>/<agent>/{bin/claude, gitconfig, claude/}. The shim runs OUTSIDE the namespace as the
// owner, reads the two token files, builds the env from an allow-list and execs stage 1 (no pid namespace, network
// on), which hides the home, the runtime dir, /tmp and /dev/shm, binds back only what is listed below, drops every
// capability and execs the real claude by absolute path.

// Sandboxable is the set of roles that run in the worker profile.
var Sandboxable = map[string]bool{"reviewer": true, "qa": true, "coder": true, "researcher": true}

var slugName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Worker tmpfs caps (memory-backed, but only allocated when written) and fixed names.
const (
	WorkerTmpSize  = "512m"
	WorkerHomeSize = "64m"
	TermsName      = "scrub-terms.txt"
	ClaudeJSONMax  = 16 << 20
	GoRootRel      = ".local/go"
	GoDir          = "go"
)

// WorkerEnvPass is what the worker's claude may inherit from the shim caller; LC_* also passes.
var WorkerEnvPass = append(append([]string{"PATH", "LANG", "TERM"}, ClaudeEnvPass...),
	"FACTORY_AGENT", "FACTORY_ROLE", "FACTORY_HOME", "HERDR_PANE_ID")

// WorkerKeepEnv is exempt from the scrub list (net=True, no forwarder).
var WorkerKeepEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN", "GH_TOKEN"}

// GoModcacheRel are the module caches, first real dir wins.
var GoModcacheRel = []string{".cache/go/path/pkg/mod", ".local/lib/factory/go/mod"}

// WorkerPaths are the token and workers-root paths of one home.
type WorkerPaths struct{ Workers, OAuth, GhRO, GhRW string }

// PathsFor is workerbox._paths.
func PathsFor(home, tokenDir, workersRoot string) WorkerPaths {
	if tokenDir == "" {
		tokenDir = home + "/.config/factory"
	}
	if workersRoot == "" {
		workersRoot = home + "/.local/state/factory/workers"
	}
	return WorkerPaths{workersRoot, tokenDir + "/worker-oauth-token", tokenDir + "/gh-worker-ro-token", tokenDir + "/gh-worker-rw-token"}
}

// GhTokenFor is the gh token file of a role; "" for a role without a gh credential (it gets none).
func (p WorkerPaths) GhTokenFor(role string) string {
	switch role {
	case "reviewer", "qa", "researcher":
		return p.GhRO
	case "coder":
		return p.GhRW
	}
	return ""
}

// CheckAgentName refuses a name that is not a slug: it becomes a path.
func CheckAgentName(agent string) error {
	if !slugName.MatchString(agent) {
		return fmt.Errorf("agent name %q is not a slug ([A-Za-z0-9][A-Za-z0-9_-]*, max 64): it becomes a path", agent)
	}
	return nil
}

// RefuseCwd is T-0127's rule for the allow view: a cwd equal to or above home, factory_home, the agent dir or a hide
// tree would expose it; one inside a hide tree is hidden. Inside factory_home only worktrees/ qualifies.
func RefuseCwd(cwd, home, factoryHome, agentDir string, hide []string, researchRoot string) (string, error) {
	want := Realpath(cwd)
	if researchRoot != "" { // a researcher's cwd is a directory strictly below the research root, nothing else
		root := Realpath(researchRoot)
		if want != root && under(want, root) && !under(want, agentDir) && !under(want, factoryHome) {
			return want, nil
		}
	}
	for _, g := range append([]string{home, factoryHome, agentDir}, hide...) {
		if under(g, want) {
			return "", fmt.Errorf("cwd %s is equal to or above %s, which the worker profile hides or protects; use a worktree under %s/worktrees", cwd, g, factoryHome)
		}
	}
	if under(want, factoryHome) {
		if want == factoryHome+"/worktrees" || !under(want, factoryHome+"/worktrees") {
			return "", fmt.Errorf("cwd %s is inside %s but not under its worktrees/ dir", cwd, factoryHome)
		}
		return want, nil
	}
	for _, tree := range hide {
		if under(want, tree) {
			return "", fmt.Errorf("cwd %s is inside the hidden tree %s; use a worktree under %s/worktrees", cwd, tree, factoryHome)
		}
	}
	return want, nil
}

// GoInfo is the Go toolchain facts of one worker.
type GoInfo struct{ Root, Modcache, Dir string }

// GoPaths is the Go toolchain for one worker, or nil when there is no usable Go (the sandbox is then as before).
// Paths are realpath'd: nsbox refuses a symlink in ro.
func GoPaths(home, agentDir, goRoot, goModcache string) *GoInfo {
	if goRoot == "" {
		goRoot = home + "/" + GoRootRel
	}
	root := Realpath(goRoot)
	exe := filepath.Join(root, "bin", "go")
	if fi, err := os.Stat(exe); err != nil || !fi.Mode().IsRegular() || syscall.Access(exe, 1) != nil {
		return nil
	}
	cands := []string{goModcache}
	if goModcache == "" {
		cands = nil
		for _, r := range GoModcacheRel {
			cands = append(cands, home+"/"+r)
		}
	}
	mod := ""
	for _, c := range cands {
		if isDir(c) {
			mod = Realpath(c)
			break
		}
	}
	return &GoInfo{root, mod, agentDir + "/" + GoDir}
}

// GoEnvFor is the env HQ sets for Go inside: offline, local toolchain, per-agent cache and tmp on disk.
func GoEnvFor(g *GoInfo) map[string]string {
	env := map[string]string{"GOROOT": g.Root, "GOCACHE": g.Dir + "/cache", "GOTMPDIR": g.Dir + "/tmp",
		"GOPATH": g.Dir + "/path", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
		"GOFLAGS": "-mod=readonly", "GOENV": "off", "GOWORK": "off"}
	if g.Modcache != "" {
		env["GOMODCACHE"] = g.Modcache
	}
	return env
}

// HideTrees is the real dirs the worker view hides: home, the runtime dir, /tmp, /dev/shm and any extra.
func HideTrees(home, runtime string, extra []string) []string {
	var out []string
	for _, d := range append([]string{home, runtime, "/tmp", "/dev/shm"}, extra...) {
		if isDir(d) {
			out = append(out, Realpath(d))
		}
	}
	return out
}

// WorkerCfgArgs are the arguments of the worker make_cfg.
type WorkerCfgArgs struct {
	AgentDir, Cwd, FactoryHome, DBDir, RealClaude string
	RwCwd                                         bool
	Home, Runtime                                 string
	HideExtra                                     []string
	ResearchRoot                                  string
	Go                                            *GoInfo
	Seams                                         Opts // UID, GID, Self, Setpriv, ResolvConf, Unshare of the nsbox cfg
}

// WorkerCfg is the allow-list cfg for one worker. All paths are resolved here, in HQ, never inside.
func WorkerCfg(a WorkerCfgArgs) (*Cfg, error) {
	home := Realpath(a.Home)
	factoryHome := Realpath(a.FactoryHome)
	dbDir := Realpath(a.DBDir)
	hide := HideTrees(home, a.Runtime, a.HideExtra)
	want, err := RefuseCwd(a.Cwd, home, factoryHome, a.AgentDir, hide, a.ResearchRoot)
	if err != nil {
		return nil, err
	}
	claude := Abs(a.RealClaude)
	install := Realpath(claude)
	ro := []string{factoryHome, a.AgentDir + "/bin", a.AgentDir + "/gitconfig"}
	if share := home + "/.local/share/claude"; under(install, share) && isDir(filepath.Dir(install)) {
		ro = append(ro, filepath.Dir(install))
	}
	rw := []string{dbDir, a.AgentDir + "/claude"}
	if a.Go != nil { // toolchain and module cache read-only (outside home they are already visible); one rw dir per agent
		for _, d := range []string{a.Go.Root, a.Go.Modcache} {
			if d != "" && under(d, home) {
				ro = append(ro, d)
			}
		}
		rw = append(rw, a.Go.Dir)
	}
	if a.RwCwd {
		rw = append(rw, want)
	} else {
		ro = append(ro, want)
	}
	var masks []string
	if isDir(factoryHome + "/worktrees") {
		masks = []string{factoryHome + "/worktrees"}
	}
	o := a.Seams
	o.TrialDir, o.RealClaude, o.Home, o.Mode, o.Net, o.NoPid = a.AgentDir+"/claude", claude, home, "allow", true, true
	o.KeepEnv, o.HideDirs, o.HideFiles, o.Rw, o.Ro, o.Masks, o.Cwd = WorkerKeepEnv, hide, nil, rw, ro, masks, want
	o.TmpfsSize = TmpfsSize
	o.TmpfsSizes = map[string]string{Realpath("/tmp"): WorkerTmpSize, home: WorkerHomeSize}
	if o.Unshare == "" {
		o.Unshare = "unshare"
	}
	cfg, err := MakeCfg(o)
	if err != nil {
		return nil, err
	}
	// the factory CLI is a symlink under the hidden home: list the link in ro, like the claude link. MakeCfg refuses
	// symlinks in ro (it would bind a dangling link), so it is appended here.
	if link := home + "/.local/bin/factory"; isLink(link) {
		cfg.Ro = append(cfg.Ro, link)
	}
	// worker-guard reads the owner's scrub terms and fails closed without them: bind exactly that one file read-only
	// when it exists. Never its directory (it holds the tokens).
	terms := home + "/.config/factory/" + TermsName
	if isLink(terms) {
		return nil, fmt.Errorf("%s is a symlink; replace it with the regular file (the worker profile will not bind a link or follow it out of ~/.config/factory)", terms)
	}
	if isFile(terms) {
		cfg.Ro = append(cfg.Ro, terms)
	}
	return cfg, nil
}

// BuildEnv is the allow-list env for stage 1: WorkerEnvPass and LC_* from the caller, plus the values set here.
func BuildEnv(caller map[string]string, cfgHome, configDir, gitconfig, oauth, gh string, goEnv map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range caller {
		if contains(WorkerEnvPass, k) || strings.HasPrefix(k, "LC_") {
			env[k] = v
		}
	}
	for k, v := range map[string]string{"HOME": cfgHome, "CLAUDE_CONFIG_DIR": configDir, "GIT_CONFIG_GLOBAL": gitconfig,
		"DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_OAUTH_TOKEN": oauth, "GH_TOKEN": gh} {
		env[k] = v
	}
	if len(goEnv) > 0 { // set by HQ, never passed through; goroot/bin goes last so the claude shim stays first
		for k, v := range goEnv {
			env[k] = v
		}
		if p := env["PATH"]; p != "" {
			env["PATH"] = p + ":" + goEnv["GOROOT"] + "/bin"
		} else {
			env["PATH"] = goEnv["GOROOT"] + "/bin:/usr/bin:/bin"
		}
	}
	return env
}

// EnvList is a map as sorted KEY=VALUE strings.
func EnvList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// ShimCfg is baked into the worker's bin/claude.
type ShimCfg struct {
	NS         *Cfg              `json:"ns"`
	Home       string            `json:"home"`
	ConfigDir  string            `json:"config_dir"`
	Gitconfig  string            `json:"gitconfig"`
	OAuthToken string            `json:"oauth_token"`
	GhToken    string            `json:"gh_token"`
	Role       string            `json:"role"`
	GoEnv      map[string]string `json:"go_env,omitempty"`
}

// ShimMain is what <agent dir>/bin/claude does, outside the namespace: tokens (an error when bad, exit 3), env, exec
// stage 1. It returns only on error.
func ShimMain(sc *ShimCfg, args []string, callerEnv []string) error {
	oauth, err := ReadToken(sc.OAuthToken)
	if err != nil {
		return err
	}
	gh, err := ReadToken(sc.GhToken)
	if err != nil {
		return err
	}
	env := BuildEnv(EnvMap(callerEnv), sc.Home, sc.ConfigDir, sc.Gitconfig, oauth, gh, sc.GoEnv)
	argv, err := ComposeStage1(sc.NS, args)
	if err != nil {
		return err
	}
	return syscall.Exec(argv[0], argv, EnvList(env))
}

// ShimScript is the text of bin/claude.
func ShimScript(self string, sc *ShimCfg) (string, error) {
	js, err := json.Marshal(sc)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#!/bin/sh\nexec %s worker-shim %s \"$@\"\n", shlex.Quote(self), shlex.Quote(string(js))), nil
}

// atomicWrite is a temp file (O_EXCL) plus rename over name: a symlink at name is replaced, not followed.
func atomicWrite(dir, name string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".")
	if err != nil {
		return fmt.Errorf("cannot write %s: %v; remove whatever is there and retry", filepath.Join(dir, name), err)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Chmod(mode); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot write %s: %v; remove whatever is there and retry", filepath.Join(dir, name), err)
	}
	return nil
}

// q is a git config value in double quotes; a newline, CR or NUL is refused.
func q(v string) (string, error) {
	if strings.ContainsAny(v, "\n\r\x00") {
		return "", errors.New("git config value with a newline or NUL")
	}
	return gq(v), nil
}

// WorkerGhHelper is the scoped git credential helper: answers only for slugs, with $GH_TOKEN (which stays in the
// sandbox env).
func WorkerGhHelper(self string, slugs []string) (string, error) {
	if len(slugs) == 0 {
		return "", fmt.Errorf("worker gh repos must be OWNER/NAME slugs, got %q; set config worker_gh_repos", slugs)
	}
	for _, s := range slugs {
		if !ValidSlug(s) {
			return "", fmt.Errorf("worker gh repos must be OWNER/NAME slugs, got %q; set config worker_gh_repos", slugs)
		}
	}
	return GhHelper(self, slugs), nil
}

// RenderGitconfig is the worker's gitconfig text.
func RenderGitconfig(helper, name, email string) (string, error) {
	var out []string
	if name != "" || email != "" {
		out = append(out, "[user]")
	}
	for _, kv := range [][2]string{{"name", name}, {"email", email}} {
		if kv[1] == "" {
			continue
		}
		v, err := q(kv[1])
		if err != nil {
			return "", err
		}
		out = append(out, "\t"+kv[0]+" = "+v)
	}
	h, err := q(helper)
	if err != nil {
		return "", err
	}
	out = append(out, "[credential]", "\tuseHttpPath = true", `[credential "https://github.com"]`, "\thelper = "+h)
	return strings.Join(out, "\n") + "\n", nil
}

var claudeSeed = map[string]any{"hasCompletedOnboarding": true, "theme": "dark", "projects": map[string]any{}}
var projectSeed = map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}

func cloneMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonIndent1(v any) []byte {
	b, _ := json.MarshalIndent(v, "", " ")
	return append(b, '\n')
}

// readSmall reads a regular file without following a symlink, at most ClaudeJSONMax+1 bytes. ok is false for a
// missing or unreadable file, a symlink or a non-regular file.
func readSmall(path string) (data []byte, ok bool, st syscall.Stat_t) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, st
	}
	defer syscall.Close(fd)
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, false, st
	}
	buf := make([]byte, ClaudeJSONMax+1)
	n, _ := syscall.Read(fd, buf)
	if n < 0 {
		n = 0
	}
	return buf[:n], true, st
}

// SeedClaudeJSON seeds <configDir>/.claude.json once; on later calls it only adds the cwd project entry when it is
// missing. The file is worker-writable: never followed, a symlink / junk / non-regular file is replaced by a fresh
// seed via temp + rename (which replaces a link itself, never its target); one over 16 MiB is refused, not truncated.
func SeedClaudeJSON(configDir, cwd string) error {
	path := filepath.Join(configDir, ".claude.json")
	var cur map[string]any
	if raw, ok, _ := readSmall(path); ok {
		if len(raw) > ClaudeJSONMax {
			return fmt.Errorf("%s is larger than %d bytes; it was left untouched. Inspect or trim it (or move it aside to reseed) and retry", path, ClaudeJSONMax)
		}
		_ = json.Unmarshal(raw, &cur)
	}
	projects, okp := map[string]any(nil), false
	if cur != nil {
		if p, has := cur["projects"]; !has {
			projects, okp = map[string]any{}, true
		} else if pm, isMap := p.(map[string]any); isMap {
			projects, okp = pm, true
		}
	}
	if !okp {
		cur = cloneMap(claudeSeed)
		projects = cur["projects"].(map[string]any)
	} else {
		if have, isMap := projects[cwd].(map[string]any); isMap {
			if have["hasTrustDialogAccepted"] == true {
				return nil
			}
			// T-1291: an entry that exists without trust: keep its other keys, set the trust flags, touch no other project
			for k, v := range projectSeed {
				have[k] = v
			}
			cur["projects"] = projects
			return atomicWrite(configDir, ".claude.json", jsonIndent1(cur), 0o600)
		}
		cur["projects"] = projects
	}
	projects[cwd] = cloneMap(projectSeed)
	return atomicWrite(configDir, ".claude.json", jsonIndent1(cur), 0o600)
}

// ClonePathRefusal is "" when path may carry a pre-registered trust entry, else the reason: it must lie strictly
// under the worktrees/ directory (by realpath), not be worktrees/ itself, and have no symlink in any component.
func ClonePathRefusal(path, worktrees string) string {
	path, worktrees = Abs(path), Abs(worktrees)
	if Realpath(worktrees) != worktrees {
		return "worktrees/ itself is or sits behind a symlink"
	}
	if Realpath(path) != path {
		return "the clone path has a symlink component"
	}
	if path == worktrees || !strings.HasPrefix(path, worktrees+"/") {
		return "the clone path is not under worktrees/"
	}
	return ""
}

type ownerIdent struct{ ino, mtime, size int64 }

func readOwnerJSON(home string) (ownerIdent, map[string]any, error) {
	path := filepath.Join(home, ".claude.json")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return ownerIdent{}, nil, fmt.Errorf("%s is missing", path)
		}
		return ownerIdent{}, nil, fmt.Errorf("%s is unreadable or a symlink: %v", path, err)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return ownerIdent{}, nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return ownerIdent{}, nil, fmt.Errorf("%s is not a regular file", path)
	}
	if int(st.Uid) != os.Getuid() {
		return ownerIdent{}, nil, fmt.Errorf("%s is not owned by uid %d", path, os.Getuid())
	}
	buf := make([]byte, ClaudeJSONMax+1)
	n, _ := syscall.Read(fd, buf)
	if n > ClaudeJSONMax {
		return ownerIdent{}, nil, fmt.Errorf("%s is larger than %d bytes", path, ClaudeJSONMax)
	}
	var anyv any
	if err := json.NewDecoder(bytes.NewReader(buf[:max(n, 0)])).Decode(&anyv); err != nil {
		return ownerIdent{}, nil, fmt.Errorf("%s is not valid JSON", path)
	}
	cur, isObj := anyv.(map[string]any)
	if !isObj {
		return ownerIdent{}, nil, fmt.Errorf("%s is not a JSON object with a projects object", path)
	}
	if p, has := cur["projects"]; has {
		if _, ok := p.(map[string]any); !ok {
			return ownerIdent{}, nil, fmt.Errorf("%s is not a JSON object with a projects object", path)
		}
	}
	return ownerIdent{int64(st.Ino), st.Mtim.Nano(), st.Size}, cur, nil
}

// editOwnerProjects adds (only when missing) or removes (only when present) projects[key] in <home>/.claude.json,
// merged onto a fresh read, written via temp + rename at 0600. A file changed between read and write is re-read once,
// then given up. Returns "added", "removed" or "unchanged".
func editOwnerProjects(home, key string, add bool) (string, error) {
	path := filepath.Join(home, ".claude.json")
	for range 2 {
		ident, cur, err := readOwnerJSON(home)
		if err != nil {
			return "", err
		}
		projects, _ := cur["projects"].(map[string]any)
		_, has := projects[key]
		if has == add {
			return "unchanged", nil
		}
		if add {
			if projects == nil {
				projects = map[string]any{}
				cur["projects"] = projects
			}
			projects[key] = cloneMap(projectSeed)
		} else {
			delete(projects, key)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(path, &st); err != nil {
			return "", fmt.Errorf("%s vanished: %v", path, err)
		}
		if (ownerIdent{int64(st.Ino), st.Mtim.Nano(), st.Size}) != ident {
			continue // a live Claude process rewrote it since our read: merge onto the new content
		}
		if err := atomicWrite(home, ".claude.json", jsonIndent1(cur), 0o600); err != nil {
			return "", err
		}
		if add {
			return "added", nil
		}
		return "removed", nil
	}
	return "", fmt.Errorf("%s changed twice while editing; the dialog may show once", path)
}

// SetCloneTrust pre-registers (add) or drops workspace trust for one factory clone in the owner's ~/.claude.json
// (T-1220). Scope first: refused unless the clone is strictly under worktrees with no symlink. Returns the outcome
// ("added", "removed", "unchanged" or "refused") and the reason for a refusal; it never touches another key.
func SetCloneTrust(home, worktrees, clone string, add bool) (string, string) {
	if why := ClonePathRefusal(clone, worktrees); why != "" {
		return "refused", why
	}
	res, err := editOwnerProjects(home, Abs(clone), add)
	if err != nil {
		return "refused", err.Error()
	}
	return res, ""
}

// CheckCwd is the cwd rule of PrepareAgent, alone: no side effects. HQ's preflight calls it so a refusal comes before
// any pane is touched.
func CheckCwd(agent, cwd, factoryHome, home, tokenDir, workersRoot, runtime string, hideExtra []string, researchRoot string) error {
	if err := CheckAgentName(agent); err != nil {
		return err
	}
	if home == "" {
		home = RealHome()
	}
	home = Realpath(home)
	p := PathsFor(home, tokenDir, workersRoot)
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	_, err := RefuseCwd(cwd, home, Realpath(factoryHome), filepath.Join(Realpath(p.Workers), agent), HideTrees(home, runtime, hideExtra), researchRoot)
	return err
}

// PrepareAgentArgs are PrepareAgent's arguments; the optional ones are test seams HQ does not pass.
type PrepareAgentArgs struct {
	Agent, Role, Cwd, FactoryHome, DBDir, RealClaude string
	RwCwd                                            bool
	Home, TokenDir, WorkersRoot, Runtime             string
	HideExtra                                        []string
	Identity                                         *[2]string
	Self                                             string // the binary the shim and the helper run; "" = this one
	GhTokenPath                                      string
	GhSlugs                                          []string
	ResearchRoot                                     string
	GoRoot, GoModcache                               string
	Seams                                            Opts
}

// PrepareAgent creates or refreshes <workers>/<agent> and returns it.
func PrepareAgent(a PrepareAgentArgs) (string, error) {
	if a.Role != "researcher" {
		a.ResearchRoot = ""
	}
	if err := CheckAgentName(a.Agent); err != nil {
		return "", err
	}
	if !Sandboxable[a.Role] {
		roles := make([]string, 0, len(Sandboxable))
		for r := range Sandboxable {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		return "", fmt.Errorf("role %q is not sandboxable; one of %v", a.Role, roles)
	}
	if filepath.Base(Abs(a.RealClaude)) != "claude" {
		return "", fmt.Errorf("real_claude must be a path whose basename is claude, got %s", a.RealClaude)
	}
	home := a.Home
	if home == "" {
		home = RealHome()
	}
	home = Realpath(home)
	p := PathsFor(home, a.TokenDir, a.WorkersRoot)
	runtime := a.Runtime
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	// refuse a bad cwd before anything is created or written
	if err := CheckCwd(a.Agent, a.Cwd, a.FactoryHome, home, a.TokenDir, a.WorkersRoot, runtime, a.HideExtra, a.ResearchRoot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(p.Workers, 0o700); err != nil {
		return "", err
	}
	root := Realpath(p.Workers)
	agentDir := filepath.Join(root, a.Agent)
	if filepath.Dir(agentDir) != root {
		return "", fmt.Errorf("agent dir %s escapes %s", agentDir, p.Workers)
	}
	for _, d := range []string{agentDir, agentDir + "/bin"} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		fi, err := os.Lstat(d)
		var st *syscall.Stat_t
		if err == nil {
			st, _ = fi.Sys().(*syscall.Stat_t)
		}
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || st == nil || int(st.Uid) != os.Getuid() {
			return "", fmt.Errorf("%s must be a real directory owned by uid %d", d, os.Getuid())
		}
	}
	self := a.Self
	if self == "" {
		self, _ = os.Executable()
	}
	helper, err := WorkerGhHelper(self, a.GhSlugs)
	if err != nil {
		return "", err
	}
	name, email := "", ""
	if a.Identity != nil {
		name, email = a.Identity[0], a.Identity[1]
	} else {
		name, email = GitValue(home, "user.name", a.Cwd), GitValue(home, "user.email", a.Cwd)
	}
	gc, err := RenderGitconfig(helper, name, email)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(agentDir, "gitconfig", []byte(gc), 0o644); err != nil { // before WorkerCfg: it refuses a symlink there
		return "", err
	}
	goInfo := GoPaths(home, agentDir, a.GoRoot, a.GoModcache)
	if goInfo != nil { // worker-writable: refuse a symlink or foreign owner before anything is created below it
		for _, rel := range []string{GoDir, GoDir + "/cache", GoDir + "/tmp"} {
			d := filepath.Join(agentDir, rel)
			fd, err := openDir(d, -1)
			if err != nil {
				return "", fmt.Errorf("%s must be a real directory owned by uid %d (%v); remove it", d, os.Getuid(), err)
			}
			syscall.Close(fd)
		}
	}
	seams := a.Seams
	seams.Self = self
	cfg, err := WorkerCfg(WorkerCfgArgs{AgentDir: agentDir, Cwd: a.Cwd, FactoryHome: a.FactoryHome, DBDir: a.DBDir,
		RealClaude: a.RealClaude, RwCwd: a.RwCwd, Home: home, Runtime: runtime, HideExtra: a.HideExtra,
		ResearchRoot: a.ResearchRoot, Go: goInfo, Seams: seams})
	if err != nil {
		return "", err
	}
	configDir := agentDir + "/claude"
	cfd, err := openDir(configDir, -1) // symlink / foreign owner => refuse, before anything is written below it
	if err != nil {
		return "", fmt.Errorf("%s must be a real directory owned by uid %d (%v)", configDir, os.Getuid(), err)
	}
	syscall.Close(cfd)
	ghTok := a.GhTokenPath
	if ghTok == "" {
		ghTok = p.GhTokenFor(a.Role)
	}
	sc := &ShimCfg{NS: cfg, Home: home, ConfigDir: configDir, Gitconfig: agentDir + "/gitconfig", OAuthToken: p.OAuth, GhToken: ghTok, Role: a.Role}
	if goInfo != nil {
		sc.GoEnv = GoEnvFor(goInfo)
	}
	shim, err := ShimScript(self, sc)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(agentDir+"/bin", "claude", []byte(shim), 0o755); err != nil {
		return "", err
	}
	if err := SeedClaudeJSON(configDir, *cfg.Cwd); err != nil {
		return "", err
	}
	return agentDir, nil
}
