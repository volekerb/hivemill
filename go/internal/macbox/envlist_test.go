package macbox

import (
	"sort"
	"testing"

	"factory/go/internal/sandbox"
)

func TestEnvListMatchesLinux(t *testing.T) {
	want := append(append([]string{}, sandbox.WorkerEnvPass...), sandbox.WorkerKeepEnv...)
	sort.Strings(want)
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
