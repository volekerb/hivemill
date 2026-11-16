package macbox

import (
	"fmt"
	"os"
	"strings"

	"factory/go/internal/shlex"
)

// ShimOpts are the paths and accounts baked into bin/claude.
type ShimOpts struct {
	Role          string
	Account       string
	LauncherPath  string
	ProfilePath   string
	RealClaude    string
	OwnerUID      int
	AgentHome     string
	ConfigDir     string
	GitConfig     string
	TmpDir        string
	OAuthToken    string
	GhToken       string
	CallerEnv     map[string]string // allow-listed keys only
}

// RenderShim writes the agent's bin/claude shim (owner-owned, 0755). Tokens
// appear only in export statements, never in argv passed to sudo or claude.
func RenderShim(o ShimOpts) (string, error) {
	if o.Account == "" || o.LauncherPath == "" || o.ProfilePath == "" || o.RealClaude == "" {
		return "", fmt.Errorf("macbox shim: account, launcher, profile and real claude are required")
	}
	var b strings.Builder
	fmt.Fprintln(&b, "#!/bin/sh")
	fmt.Fprintln(&b, "set -eu")
	if o.OAuthToken != "" {
		fmt.Fprintf(&b, "export CLAUDE_CODE_OAUTH_TOKEN=%s\n", shlex.Quote(o.OAuthToken))
	}
	if o.GhToken != "" {
		fmt.Fprintf(&b, "export GH_TOKEN=%s\n", shlex.Quote(o.GhToken))
	}
	for _, k := range WorkerEnvPass {
		if v, ok := o.CallerEnv[k]; ok && v != "" {
			fmt.Fprintf(&b, "export %s=%s\n", k, shlex.Quote(v))
		}
	}
	for k, v := range o.CallerEnv {
		if strings.HasPrefix(k, "LC_") && v != "" {
			fmt.Fprintf(&b, "export %s=%s\n", k, shlex.Quote(v))
		}
	}
	if o.OwnerUID > 0 {
		fmt.Fprintf(&b, "export MACBOX_OWNER_UID=%d\n", o.OwnerUID)
	}
	for src, val := range map[string]string{
		"MACBOX_AGENT_HOME":        o.AgentHome,
		"MACBOX_CLAUDE_CONFIG_DIR": o.ConfigDir,
		"MACBOX_GIT_CONFIG_GLOBAL": o.GitConfig,
		"MACBOX_TMPDIR":            o.TmpDir,
	} {
		if val != "" {
			fmt.Fprintf(&b, "export %s=%s\n", src, shlex.Quote(val))
		}
	}
	fmt.Fprintf(&b, "exec sudo -n -u %s --preserve-env=%s %s -- sandbox-exec -f %s %s \"$@\"\n",
		shlex.Quote(o.Account), PreserveEnvCSV(),
		shlex.Quote(o.LauncherPath), shlex.Quote(o.ProfilePath), shlex.Quote(o.RealClaude))
	return b.String(), nil
}

// ShimRecordedArgv returns the argv the shim would pass to sudo (for tests).
func ShimRecordedArgv(o ShimOpts, extraArgs []string) []string {
	inner := []string{
		"sandbox-exec", "-f", o.ProfilePath, o.RealClaude,
	}
	launchArgv := append([]string{o.LauncherPath, "--"}, inner...)
	return append(append([]string{
		"sudo", "-n", "-u", o.Account,
		"--preserve-env=" + PreserveEnvCSV(),
	}, launchArgv...), extraArgs...)
}

// ShimEnvMap is the environment the shim exports (tokens only here).
func ShimEnvMap(o ShimOpts) map[string]string {
	m := map[string]string{}
	if o.OAuthToken != "" {
		m["CLAUDE_CODE_OAUTH_TOKEN"] = o.OAuthToken
	}
	if o.GhToken != "" {
		m["GH_TOKEN"] = o.GhToken
	}
	for k, v := range o.CallerEnv {
		if EnvAllowed(k) && v != "" {
			m[k] = v
		}
	}
	if o.OwnerUID > 0 {
		m["MACBOX_OWNER_UID"] = fmt.Sprintf("%d", o.OwnerUID)
	}
	for src, val := range map[string]string{
		"MACBOX_AGENT_HOME":        o.AgentHome,
		"MACBOX_CLAUDE_CONFIG_DIR": o.ConfigDir,
		"MACBOX_GIT_CONFIG_GLOBAL": o.GitConfig,
		"MACBOX_TMPDIR":            o.TmpDir,
	} {
		if val != "" {
			m[src] = val
		}
	}
	return m
}

// WriteShim renders and writes path mode 0755.
func WriteShim(path string, o ShimOpts) error {
	s, err := RenderShim(o)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(s), 0o755)
}