package macbox

import (
	"sort"
	"strings"
)

// claudeEnvPass is the same allow-list keys as sandbox.ClaudeEnvPass on Linux.
var claudeEnvPass = []string{
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"DISABLE_AUTOUPDATER",
}

// WorkerEnvPass is what may pass from the shim caller into the worker environment
// (same keys as sandbox.WorkerEnvPass on Linux; copied here so macbox does not
// import sandbox at runtime).
var WorkerEnvPass = append(append([]string{"PATH", "LANG", "TERM"}, claudeEnvPass...),
	"FACTORY_AGENT", "FACTORY_ROLE", "FACTORY_HOME", "HERDR_PANE_ID")

// WorkerKeepEnv are the token keys; they travel in the environment only.
var WorkerKeepEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN", "GH_TOKEN"}

// LauncherMetaEnv are set by the shim for the launcher and stripped before exec.
var LauncherMetaEnv = []string{
	"MACBOX_OWNER_UID",
	"MACBOX_AGENT_HOME",
	"MACBOX_CLAUDE_CONFIG_DIR",
	"MACBOX_GIT_CONFIG_GLOBAL",
	"MACBOX_TMPDIR",
}

// PreserveEnvKeys is the sorted key list for sudo --preserve-env=.
func PreserveEnvKeys() []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, k := range WorkerEnvPass {
		add(k)
	}
	for _, k := range WorkerKeepEnv {
		add(k)
	}
	for _, k := range LauncherMetaEnv {
		add(k)
	}
	sort.Strings(out)
	return out
}

// PreserveEnvCSV is the comma-separated preserve-env list for sudo.
func PreserveEnvCSV() string { return strings.Join(PreserveEnvKeys(), ",") }

// EnvAllowed reports whether k may survive the launcher's env filter (LC_* matches Linux).
func EnvAllowed(k string) bool {
	if strings.HasPrefix(k, "LC_") {
		return true
	}
	for _, a := range WorkerEnvPass {
		if a == k {
			return true
		}
	}
	for _, a := range WorkerKeepEnv {
		if a == k {
			return true
		}
	}
	for _, a := range LauncherMetaEnv {
		if a == k {
			return true
		}
	}
	return false
}
