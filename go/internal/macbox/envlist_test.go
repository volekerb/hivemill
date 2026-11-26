package macbox

import (
	"sort"
	"testing"
)

func TestEnvListMatchesLinux(t *testing.T) {
	// Mirrors the Linux worker env list (pass and keep keys), sorted.
	want := []string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"DISABLE_AUTOUPDATER",
		"FACTORY_AGENT",
		"FACTORY_HOME",
		"FACTORY_ROLE",
		"GH_TOKEN",
		"HERDR_PANE_ID",
		"LANG",
		"PATH",
		"TERM",
	}
	got := append(append([]string{}, WorkerEnvPass...), WorkerKeepEnv...)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("len %d want %d\ngot  %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestEnvAllowedDropsSecret(t *testing.T) {
	if EnvAllowed("SSH_AUTH_SOCK") || EnvAllowed("HOME") {
		t.Fatal("non-allow-listed keys must be dropped")
	}
	if !EnvAllowed("PATH") || !EnvAllowed("LC_CTYPE") || !EnvAllowed("GH_TOKEN") {
		t.Fatal("allow-listed keys must pass")
	}
}
