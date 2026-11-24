package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const authStopMsg = "not logged in. Do this, then re-run the installer with --resume:"
const authPromptMsg = "Press Enter here when done."

// ttyHost is a fresh box on a terminal: no gh login, no Claude credentials. `gh auth login` logs gh in and prints a token.
func ttyHost(t *testing.T) *fakeHost {
	f := newFake(t)
	f.tty = true
	f.ghAuth = false
	f.enters = 5
	f.ttyRun = func(f *fakeHost, argv []string) (int, string) {
		if strings.Join(argv, " ") != "gh auth login" {
			return 2, ""
		}
		f.ghAuth = true
		return 0, "token: " + leakText + "\n"
	}
	f.onEnter = func(f *fakeHost) { f.login(t) }
	return f
}

func TestAuthTTYWaitsAndContinues(t *testing.T) {
	f := ttyHost(t)
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if strings.Contains(out, authStopMsg) || rc == 3 {
		t.Fatalf("stopped on auth: rc=%d\n%s", rc, out)
	}
	if len(f.ttyRuns) != 1 || f.waited != 1 {
		t.Errorf("gh auth login runs=%v enters=%d", f.ttyRuns, f.waited)
	}
	if !strings.Contains(out, authPromptMsg) {
		t.Errorf("no Claude prompt:\n%s", out)
	}
	if !f.mutated("git clone") {
		t.Errorf("the install did not reach the clone step:\n%s", out)
	}
}

func TestAuthNoTTYStopsUnchanged(t *testing.T) {
	f := ttyHost(t)
	f.tty = false
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 3 || !strings.Contains(out, authStopMsg) {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	if strings.Contains(out, authPromptMsg) || len(f.ttyRuns) != 0 || f.waited != 0 {
		t.Errorf("prompted or waited without a terminal: runs=%v waited=%d\n%s", f.ttyRuns, f.waited, out)
	}
}

func TestAuthNoYesStopsUnchanged(t *testing.T) {
	// --yes is what selects the mutating mode, so call the step with it unset, as a --resume style caller would
	f := ttyHost(t)
	var out strings.Builder
	c := NewCtx(f, &Args{}, &out, false)
	_, err := stepAuth(c)
	stop, ok := err.(*Stop)
	if !ok || stop.Code != 3 || !strings.Contains(stop.Msg, authStopMsg) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(out.String(), authPromptMsg) || len(f.ttyRuns) != 0 || f.waited != 0 {
		t.Errorf("prompted without --yes: runs=%v waited=%d", f.ttyRuns, f.waited)
	}
}

func TestAuthRoundsAreBounded(t *testing.T) {
	f := ttyHost(t)
	f.ttyRun = func(f *fakeHost, argv []string) (int, string) { return 1, "" }
	f.onEnter = nil
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 3 || !strings.Contains(out, authStopMsg) {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	if len(f.ttyRuns) != 3 || f.waited != 3 {
		t.Errorf("want 3 rounds, got gh runs=%d enters=%d", len(f.ttyRuns), f.waited)
	}
}

func TestAuthEOFOnEnterStops(t *testing.T) {
	f := ttyHost(t)
	f.enters = 0
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 3 || !strings.Contains(out, authStopMsg) {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
}

func TestAuthNoSecretInOutput(t *testing.T) {
	f := ttyHost(t)
	rc, out, errb := runMain(t, f, "--yes", "--no-systemd")
	if rc == 3 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	if !strings.Contains(f.ttyOut.String(), leakText) {
		t.Fatal("the fake gh did not print the token on the terminal")
	}
	// every file the run wrote under the home dir, the installer logs (~/.cache/hivemill-install/*.log) included
	all := out + errb
	_ = filepath.WalkDir(f.HomeDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, rerr := os.ReadFile(p); rerr == nil {
				all += string(b)
			}
		}
		return nil
	})
	if strings.Contains(all, leakText) {
		t.Errorf("token-like text leaked into the installer output")
	}
}
