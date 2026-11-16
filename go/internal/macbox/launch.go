package macbox

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// RefuseLauncherUID returns an error when uid is root or the owner's uid.
func RefuseLauncherUID(uid, ownerUID int) error {
	if uid == 0 {
		return fmt.Errorf("macbox launcher: refusing to run as uid 0")
	}
	if ownerUID <= 0 {
		return fmt.Errorf("macbox launcher: MACBOX_OWNER_UID is missing; refusing to run")
	}
	if uid == ownerUID {
		return fmt.Errorf("macbox launcher: refusing to run as the owner (uid %d)", ownerUID)
	}
	return nil
}

// SplitLauncherArgv splits argv at the first "--". Everything before is discarded
// (reserved); everything after is what sandbox-exec runs.
func SplitLauncherArgv(argv []string) ([]string, error) {
	for i, a := range argv {
		if a == "--" {
			rest := argv[i+1:]
			if len(rest) == 0 {
				return nil, fmt.Errorf("macbox launcher: nothing after --")
			}
			return rest, nil
		}
	}
	return nil, fmt.Errorf("macbox launcher: missing -- before sandbox-exec argv")
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

// ApplyLauncherDirs sets per-agent paths the launcher owns (ADR 3.2).
func ApplyLauncherDirs(env map[string]string) {
	for src, dst := range map[string]string{
		"MACBOX_AGENT_HOME":        "HOME",
		"MACBOX_CLAUDE_CONFIG_DIR": "CLAUDE_CONFIG_DIR",
		"MACBOX_GIT_CONFIG_GLOBAL": "GIT_CONFIG_GLOBAL",
		"MACBOX_TMPDIR":            "TMPDIR",
	} {
		if v := env[src]; v != "" {
			env[dst] = v
		}
	}
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	for _, k := range LauncherMetaEnv {
		delete(env, k)
	}
	delete(env, "MACBOX_OWNER_UID")
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

// OwnerUIDFromEnv reads MACBOX_OWNER_UID; 0 when unset or invalid.
func OwnerUIDFromEnv(environ []string) int {
	v := EnvironMap(environ)["MACBOX_OWNER_UID"]
	if v == "" {
		return 0
	}
	var n int
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// LauncherUID is the current uid, mockable in tests via OverrideUID.
var LauncherUID = func() int { return os.Getuid() }
