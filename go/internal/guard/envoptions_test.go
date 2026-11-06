package guard

import (
	"strings"
	"testing"
)

func TestEnvOptionsClearAfterUnsetDashValue(t *testing.T) {
	opts := envOptions([]string{"-u", "-i", "factory", "task", "show", "T-1"})
	var kinds []string
	for _, o := range opts {
		kinds = append(kinds, o.kind+":"+o.value)
	}
	want := []string{"unset:-i", "clear:-i"}
	if len(kinds) != len(want) {
		t.Fatalf("got %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("got %v want %v", kinds, want)
		}
	}
}

func TestCheckScrubEnvUiFactory(t *testing.T) {
	scrub, err := checkScrub("env", []string{"-u", "-i", "factory", "task", "show", "T-1"})
	if err != nil {
		t.Fatal(err)
	}
	if scrub != "env -i" {
		t.Fatalf("scrub=%q", scrub)
	}
}

func TestCheckEnvUiFactoryBlocked(t *testing.T) {
	cwd := t.TempDir()
	err := Check("env -u -i factory task show T-1", cwd, "coder", false)
	if err == nil || !strings.Contains(err.Error(), "env -i in a command line that runs factory") {
		t.Fatalf("got %v", err)
	}
}
