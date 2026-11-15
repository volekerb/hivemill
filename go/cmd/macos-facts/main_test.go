package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With an empty PATH every probe fails to start; the run must still exit 0 and
// write a report that records each failure.
func TestProbeFailsExitsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	outFile := filepath.Join(t.TempDir(), "report.md")
	var so, se bytes.Buffer
	if code := run([]string{"--out", outFile}, &so, &se); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, se.String())
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	rep := string(b)
	for _, want := range []string{"# macOS facts report", "## sw_vers", "## launchctl_print_gui", "Exit code: -1"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

func TestStdoutDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	var so, se bytes.Buffer
	if code := run(nil, &so, &se); code != 0 || !strings.Contains(so.String(), "## id_u") {
		t.Fatalf("exit %d, out %q", code, so.String())
	}
}

func TestBadFlag(t *testing.T) {
	var so, se bytes.Buffer
	if code := run([]string{"--nope"}, &so, &se); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestRelativeHerdrResolved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	want, err := filepath.Abs("bin/herdr")
	if err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	if code := run([]string{"--herdr", "bin/herdr"}, &so, &se); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(so.String(), "codesign -dv "+want) {
		t.Errorf("report lacks absolute herdr path %q", want)
	}
}

func TestOptionLikeHerdrRefused(t *testing.T) {
	var so, se bytes.Buffer
	if code := run([]string{"--herdr", "-x"}, &so, &se); code != 2 || so.Len() != 0 {
		t.Fatalf("exit %d, out %q", code, so.String())
	}
}
