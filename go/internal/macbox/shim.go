package macbox

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"factory/go/internal/shlex"
)

// DefaultSudo is the sudo the shim runs (absolute, never a PATH lookup).
const DefaultSudo = "/usr/bin/sudo"

// ShimOpts are the paths baked into bin/claude. The shim holds token file
// paths only: it reads the 0600 owner files at run time, so no token value is
// ever written into the 0755 shim that the worker accounts can read.
type ShimOpts struct {
	Role         string // picks the worker account (AccountForRole)
	LauncherPath string
	ProfilePath  string
	RealClaude   string
	AgentDir     string // per-agent HOME, CLAUDE_CONFIG_DIR, gitconfig, TMPDIR (DirsFor)
	OAuthPath    string // ~/.config/factory token file, 0600 owner
	GhPath       string // ~/.config/factory token file, 0600 owner
	SudoPath     string // default DefaultSudo; tests point it at a recorder
	CallerEnv    map[string]string
}

// tokenFiles pairs each token variable with the file it is read from.
func (o ShimOpts) tokenFiles() [][2]string {
	return [][2]string{{"CLAUDE_CODE_OAUTH_TOKEN", o.OAuthPath}, {"GH_TOKEN", o.GhPath}}
}

// RenderShim returns the agent's bin/claude script. Tokens are read from
// their files into the environment and never appear in argv.
func RenderShim(o ShimOpts) (string, error) {
	account, err := AccountForRole(o.Role)
	if err != nil {
		return "", err
	}
	sudo := o.SudoPath
	if sudo == "" {
		sudo = DefaultSudo
	}
	for name, v := range map[string]string{
		"launcher": o.LauncherPath, "profile": o.ProfilePath, "real claude": o.RealClaude,
		"agent dir": o.AgentDir, "oauth token file": o.OAuthPath, "gh token file": o.GhPath, "sudo": sudo,
	} {
		if !path.IsAbs(v) {
			return "", fmt.Errorf("macbox shim: %s must be an absolute path, got %q", name, v)
		}
	}
	var b strings.Builder
	fmt.Fprintln(&b, "#!/bin/sh")
	fmt.Fprintln(&b, "set -eu")
	for _, t := range o.tokenFiles() {
		// Assignment and export are separate so set -e sees a failing cat.
		fmt.Fprintf(&b, "%s=\"$(/bin/cat -- %s)\"\n", t[0], shlex.Quote(t[1]))
		fmt.Fprintf(&b, "[ -n \"$%s\" ] || { echo 'bin/claude: the %s token file is empty' >&2; exit 1; }\n", t[0], t[0])
		fmt.Fprintf(&b, "export %s\n", t[0])
	}
	var keys []string
	for k, v := range o.CallerEnv {
		if v != "" && EnvAllowed(k) && !isTokenKey(k) && !isMetaKey(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shlex.Quote(o.CallerEnv[k]))
	}
	for _, kv := range DirsFor(o.AgentDir).metaAssignments() {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "export %s=%s\n", k, shlex.Quote(v))
	}
	fmt.Fprintf(&b, "exec %s -n -u %s --preserve-env=%s %s -- %s -f %s %s \"$@\"\n",
		shlex.Quote(sudo), shlex.Quote(account), PreserveEnvCSV(),
		shlex.Quote(o.LauncherPath), SandboxExec, shlex.Quote(o.ProfilePath), shlex.Quote(o.RealClaude))
	return b.String(), nil
}

func isTokenKey(k string) bool {
	for _, t := range WorkerKeepEnv {
		if t == k {
			return true
		}
	}
	return false
}

func isMetaKey(k string) bool {
	for _, t := range LauncherMetaEnv {
		if t == k {
			return true
		}
	}
	return false
}

// WriteShim renders the shim and writes it to path with mode 0755 (owner-owned
// because the owner writes it; group and other get no write bit).
func WriteShim(p string, o ShimOpts) error {
	s, err := RenderShim(o)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
		return err
	}
	// WriteFile keeps the mode of an existing file; force 0755.
	return os.Chmod(p, 0o755)
}
