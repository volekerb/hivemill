package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Evaluator trial sandbox (G-009 T8a): the fail-closed checks and the nsbox cfg of one skill trial.
//
// SECURITY CODE. Nothing here reads the eval token: CheckToken only lstat()s the file. Only the shim opens it, inside
// the pane. The trial dir layout, the git baseline and the factory.db seed of scout/sandbox.py build_sandbox depend
// on the scout DB and vendor packages and move with the skilleval consumer (P4-L); the sandbox-defining parts are here.

// Eval constants.
const (
	EvalAgentRole = "evaluator"
	SeedGoal      = "G-001"
	SeedTask      = "T-0001"
	DeadSocket    = "/proc/nonexistent/eval.sock"
)

// EvalEnvKeys are the env keys the live spawn may hand the evaluator through env_extra; anything else is refused.
var EvalEnvKeys = []string{"FACTORY_HOME", "HOME", "CLAUDE_CONFIG_DIR", "PATH", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER"}

// HomeRC is the one file of an eval home: an empty HOME makes interactive zsh run zsh-newuser-install.
var HomeRC = [2]string{".zshrc", "# factory skilleval trial: suppress zsh-newuser-install\n"}

// ErrSandboxRefused means the sandbox cannot be built or is not safe to start; nothing is spawned.
var ErrSandboxRefused = errors.New("sandbox refused")

func refused(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrSandboxRefused, fmt.Sprintf(format, a...))
}

// TrialName is tr0007 for trial 7.
func TrialName(id int) string { return fmt.Sprintf("tr%04d", id) }

// CheckToken lstat()s only (never opens): a regular file, mode 0600, owned by us. The value is read by the shim alone.
func CheckToken(path string) error {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		st.Mode&0o7777 != 0o600 || int(st.Uid) != os.Getuid() {
		return refused("%s (%s: must exist, be a regular file, mode 0600, owned by uid %d)", NoTokenMsg, path, os.Getuid())
	}
	return nil
}

// CheckUserns refuses when user namespaces are unavailable: there is no unsandboxed fallback.
func CheckUserns(unshare string) error {
	if ok, why := UsernsAvailable(unshare); !ok {
		return refused("user namespaces unavailable, no unsandboxed fallback: %s", why)
	}
	return nil
}

// ClaudeJSON is the seed of <trial>/cfg/.claude.json: onboarding done, the work dir trusted.
func ClaudeJSON(work string) map[string]any {
	seed := cloneMap(claudeSeed)
	seed["projects"].(map[string]any)[Realpath(work)] = cloneMap(projectSeed)
	return seed
}

// EvalNsCfg is the allow-list nsbox cfg for one trial: tmpfs over the real home, the runtime dir, /tmp and /dev/shm,
// bound back only the trial dir (rw), its bin/scout/role (ro), the claude install and the one vendored plugin (ro).
func EvalNsCfg(trial, home, realClaude, vendorPlugin, unshare string, seams Opts) (*Cfg, error) {
	trial = Realpath(trial)
	home = Realpath(home)
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if seams.Runtime != "" {
		runtime = seams.Runtime
	}
	var hide []string
	for _, d := range []string{home, runtime, "/tmp", "/dev/shm"} {
		if isDir(d) {
			hide = append(hide, Realpath(d))
		}
	}
	claude := Abs(realClaude)
	ro := []string{trial + "/bin", trial + "/scout", trial + "/role"}
	install := Realpath(claude)
	if share := home + "/.local/share/claude"; strings.HasPrefix(install, share+"/") && isDir(filepath.Dir(install)) {
		ro = append(ro, filepath.Dir(install))
	}
	if vendorPlugin != "" {
		ro = append(ro, Realpath(vendorPlugin))
	}
	trialsRoot := filepath.Dir(trial)
	var masks []string // siblings stay hidden even when the trials root lies outside every hide tree
	inHide := false
	for _, h := range hide {
		inHide = inHide || under(trialsRoot, h)
	}
	if !inHide {
		masks = append(masks, trialsRoot)
	}
	o := seams
	o.TrialDir, o.RealClaude, o.Home, o.TrialsRoot, o.Mode, o.Forward = trial, claude, home, trialsRoot, "allow", true
	if unshare != "" {
		o.Unshare = unshare
	} else if o.Unshare == "" {
		o.Unshare = "unshare"
	}
	o.HideDirs, o.HideFiles, o.Rw, o.Ro, o.Masks, o.Cwd = hide, nil, []string{trial}, ro, masks, trial+"/work"
	return MakeCfg(o)
}

// PreflightArgv is the real view (same cfg, no proxy, no token) around `claude --version`: any hide/bind failure or
// a claude that is not reachable inside shows up as a non-zero exit before an agent is started.
func PreflightArgv(c *Cfg) ([]string, error) {
	cc := *c
	cc.ForwardSock = nil
	return ComposeStage1(&cc, []string{"--version"})
}
