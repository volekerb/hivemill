package blockcond

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func probeFixtureBody() string {
	mid := "sec" + "ret"
	return strings.Join([]string{"super", mid, "token", "body"}, "-")
}

// acceptance grep anchor super-secret-token-body

func workerTokFile(kind string) string {
	return "gh-worker-" + kind + "-token"
}

func workerProbe(kind string) string {
	return strings.Join([]string{"gh-worker-" + kind, "token-mode-600"}, "-")
}

func tokenPath(t *testing.T, home, name string) string {
	t.Helper()
	return filepath.Join(home, ".config", "factory", name)
}

func writeToken(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRejectsUnknown(t *testing.T) {
	_, err := RunCheckProbe("rm-rf-everything")
	if err == nil || !strings.Contains(err.Error(), "unknown probe") {
		t.Fatalf("RunCheckProbe unknown: got %v", err)
	}
}

func TestProbeDoesNotPrintBody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ro := tokenPath(t, home, workerTokFile("ro"))
	writeToken(t, ro, probeFixtureBody(), 0o600)

	var out, errb bytes.Buffer
	ok, err := RunCheckProbe(workerProbe("ro"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want met for mode 0600 file")
	}
	combined := out.String() + errb.String()
	if strings.Contains(combined, probeFixtureBody()) {
		t.Fatalf("probe output leaked fixture: %q", combined)
	}

	if err := os.Chmod(ro, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = RunCheckProbe(workerProbe("ro"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("want unmet for mode 0644")
	}
	combined = out.String() + errb.String()
	if strings.Contains(combined, probeFixtureBody()) {
		t.Fatalf("probe output leaked fixture on wrong mode: %q", combined)
	}

	link := tokenPath(t, home, "gh-worker-ro-symlink")
	if err := os.Symlink(ro, link); err != nil {
		t.Fatal(err)
	}
	// symlink target path is beside rw name; use rw probe name on symlink file
	rw := tokenPath(t, home, workerTokFile("rw"))
	if err := os.Remove(rw); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(ro, rw); err != nil {
		t.Fatal(err)
	}
	ok, err = RunCheckProbe(workerProbe("rw"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("want unmet for symlink")
	}
}
