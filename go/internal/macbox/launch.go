package macbox

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SandboxExec is the only program the launcher execs (absolute: execve does no
// PATH search, and a PATH lookup would let the caller pick the binary).
const SandboxExec = "/usr/bin/sandbox-exec"

// RefuseLauncherUID returns an error when uid is root or the owner's uid.
// ownerUID is the uid that invoked sudo (SUDO_UID); <= 0 means unknown and is
// refused, so the launcher never runs outside sudo.
func RefuseLauncherUID(uid, ownerUID int) error {
	if uid == 0 {
		return fmt.Errorf("macbox launcher: refusing to run as uid 0")
	}
	if ownerUID <= 0 {
		return fmt.Errorf("macbox launcher: SUDO_UID is missing; run the launcher only through the agent's bin/claude shim (sudo -u <worker account>)")
	}
	if uid == ownerUID {
		return fmt.Errorf("macbox launcher: refusing to run as the owner (uid %d); run it as a worker account via sudo", ownerUID)
	}
	return nil
}

// SplitLauncherArgv splits argv at the first "--" and checks that what follows
// is `/usr/bin/sandbox-exec -f <absolute profile> <program> [args...]`.
func SplitLauncherArgv(argv []string) ([]string, error) {
	for i, a := range argv {
		if a != "--" {
			continue
		}
		rest := argv[i+1:]
		if len(rest) < 4 || rest[0] != SandboxExec || rest[1] != "-f" || !path.IsAbs(rest[2]) || rest[3] == "" {
			return nil, fmt.Errorf("macbox launcher: argv after -- must be %s -f <absolute profile> <program> [args...], got %q", SandboxExec, rest)
		}
		return rest, nil
	}
	return nil, fmt.Errorf("macbox launcher: missing -- before the sandbox-exec argv")
}

// FilterEnviron drops every key not on the worker allow-list.
func FilterEnviron(environ []string) []string {
	m := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !EnvAllowed(k) {
			continue
		}
		m[k] = v
	}
	return envMapToList(m)
}

// launcherDirs maps each shim meta variable to the variable the launcher sets.
var launcherDirs = map[string]string{
	"MACBOX_AGENT_HOME":        "HOME",
	"MACBOX_CLAUDE_CONFIG_DIR": "CLAUDE_CONFIG_DIR",
	"MACBOX_GIT_CONFIG_GLOBAL": "GIT_CONFIG_GLOBAL",
	"MACBOX_TMPDIR":            "TMPDIR",
}

// ApplyLauncherDirs sets the per-agent paths (ADR 3.2) from the shim's meta
// variables and strips the meta variables. Every meta variable must be an
// absolute path; otherwise it returns an error and the launcher refuses.
func ApplyLauncherDirs(env map[string]string) error {
	keys := make([]string, 0, len(launcherDirs))
	for k := range launcherDirs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, src := range keys {
		v := env[src]
		if !path.IsAbs(v) {
			return fmt.Errorf("macbox launcher: %s must be an absolute path, got %q; regenerate the agent's bin/claude shim", src, v)
		}
		env[launcherDirs[src]] = v
	}
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	for _, k := range LauncherMetaEnv {
		delete(env, k)
	}
	return nil
}

// AgentDirs are the per-agent paths under one agent dir.
type AgentDirs struct {
	Home, ConfigDir, GitConfig, TmpDir string
}

// DirsFor returns the per-agent layout under agentDir.
func DirsFor(agentDir string) AgentDirs {
	return AgentDirs{
		Home:      path.Join(agentDir, "claude"),
		ConfigDir: path.Join(agentDir, "claude"),
		GitConfig: path.Join(agentDir, "gitconfig"),
		TmpDir:    path.Join(agentDir, "tmp"),
	}
}

// metaAssignments are the VAR=value meta variables in LauncherMetaEnv order.
func (d AgentDirs) metaAssignments() []string {
	return []string{
		"MACBOX_AGENT_HOME=" + d.Home,
		"MACBOX_CLAUDE_CONFIG_DIR=" + d.ConfigDir,
		"MACBOX_GIT_CONFIG_GLOBAL=" + d.GitConfig,
		"MACBOX_TMPDIR=" + d.TmpDir,
	}
}

// EnvList formats a map as sorted KEY=VALUE strings.
func EnvList(m map[string]string) []string { return envMapToList(m) }

func envMapToList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// EnvironMap parses K=V entries (last wins).
func EnvironMap(environ []string) map[string]string {
	m := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

// SudoUIDFromEnv reads SUDO_UID (set by sudo to the invoking uid); 0 when
// unset or not a positive decimal.
func SudoUIDFromEnv(environ []string) int {
	n, err := strconv.Atoi(EnvironMap(environ)["SUDO_UID"])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// LauncherUID is the current uid; tests replace it.
var LauncherUID = func() int { return os.Getuid() }
