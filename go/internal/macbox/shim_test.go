package macbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testOAuth = "oauth-token-placeholder-7f3a"
	testGh    = "gh-token-placeholder-abc"
)

// shimFixture writes token files and a recording sudo into a temp dir and
// returns shim options pointing at them.
func shimFixture(t *testing.T) (ShimOpts, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config", "factory")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	oauth := filepath.Join(cfg, "cred-oauth")
	gh := filepath.Join(cfg, "cred-gh-rw")
	if err := os.WriteFile(oauth, []byte(testOAuth+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gh, []byte(testGh+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(dir, "rec")
	sudo := filepath.Join(dir, "fake-sudo")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + rec + ".argv\n/usr/bin/env > " + rec + ".env\n"
	if err := os.WriteFile(sudo, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ShimOpts{
		Role:         "coder",
		LauncherPath: DefaultLauncher,
		ProfilePath:  "/Users/x/.local/state/factory/workers/a/profile.sb",
		RealClaude:   "/Applications/Claude.app/claude",
		AgentDir:     "/Users/x/.local/state/factory/workers/a",
		OAuthPath:    oauth,
		GhPath:       gh,
		SudoPath:     sudo,
		CallerEnv:    map[string]string{"PATH": "/usr/bin:/bin", "FACTORY_AGENT": "a", "SSH_AUTH_SOCK": "/tmp/s", "GH_TOKEN": "caller-token"},
	}, rec
}

// TestShimNoTokenArgv runs the rendered shim with a recording sudo: the shim
// file and the recorded argv hold no token value; tokens appear only in the
// recorded environment.
func TestShimNoTokenArgv(t *testing.T) {
	o, rec := shimFixture(t)
	shim := filepath.Join(t.TempDir(), "claude")
	if err := WriteShim(shim, o); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(shim)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, tok := range []string{testOAuth, testGh, "caller-token"} {
		if strings.Contains(script, tok) {
			t.Fatalf("shim file contains token value %q:\n%s", tok, script)
		}
	}
	if !strings.Contains(script, o.OAuthPath) || !strings.Contains(script, o.GhPath) {
		t.Fatalf("shim must read the token files at run time:\n%s", script)
	}
	if strings.Contains(script, "SSH_AUTH_SOCK") {
		t.Fatalf("non-allow-listed caller env baked into shim:\n%s", script)
	}
	fi, err := os.Stat(shim)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("shim mode %v, want 0755", fi.Mode().Perm())
	}

	cmd := exec.Command(shim, "--version", "-p", "hello")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run shim: %v\n%s", err, out)
	}
	argvB, err := os.ReadFile(rec + ".argv")
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(argvB)), "\n")
	joined := strings.Join(argv, " ")
	for _, tok := range []string{testOAuth, testGh} {
		if strings.Contains(joined, tok) {
			t.Fatalf("argv contains token %q: %s", tok, joined)
		}
	}
	want := []string{"-n", "-u", AccountRW, "--preserve-env=" + PreserveEnvCSV(), DefaultLauncher, "--",
		SandboxExec, "-f", o.ProfilePath, o.RealClaude, "--version", "-p", "hello"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv\n got %q\nwant %q", argv, want)
	}
	envB, err := os.ReadFile(rec + ".env")
	if err != nil {
		t.Fatal(err)
	}
	env := EnvironMap(strings.Split(strings.TrimSpace(string(envB)), "\n"))
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != testOAuth || env["GH_TOKEN"] != testGh {
		t.Fatalf("tokens must reach sudo in the env: oauth=%q gh=%q", env["CLAUDE_CODE_OAUTH_TOKEN"], env["GH_TOKEN"])
	}
	if env["MACBOX_AGENT_HOME"] != o.AgentDir+"/claude" || env["MACBOX_TMPDIR"] != o.AgentDir+"/tmp" {
		t.Fatalf("meta dirs missing from env: %v", env)
	}
	for _, k := range WorkerKeepEnv {
		if !strings.Contains(PreserveEnvCSV(), k) {
			t.Fatalf("--preserve-env lacks %s", k)
		}
	}
}

// TestShimFailsClosedOnMissingToken: an unreadable or empty token file stops
// the shim before sudo runs.
func TestShimFailsClosedOnMissingToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		brk  func(o ShimOpts)
	}{
		{"missing", func(o ShimOpts) { _ = os.Remove(o.GhPath) }},
		{"empty", func(o ShimOpts) { _ = os.WriteFile(o.OAuthPath, nil, 0o600) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, rec := shimFixture(t)
			shim := filepath.Join(t.TempDir(), "claude")
			if err := WriteShim(shim, o); err != nil {
				t.Fatal(err)
			}
			tc.brk(o)
			cmd := exec.Command(shim)
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			if err := cmd.Run(); err == nil {
				t.Fatal("shim must exit non-zero")
			}
			if _, err := os.Stat(rec + ".argv"); err == nil {
				t.Fatal("sudo ran despite a missing token")
			}
		})
	}
}

func TestShimAccountByRole(t *testing.T) {
	o, _ := shimFixture(t)
	for role, acct := range map[string]string{"coder": AccountRW, "reviewer": AccountRO, "qa": AccountRO, "researcher": AccountRO} {
		o.Role = role
		s, err := RenderShim(o)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, " -u "+acct+" ") {
			t.Fatalf("%s: shim does not sudo to %s", role, acct)
		}
	}
	o.Role = "judge"
	if _, err := RenderShim(o); err == nil {
		t.Fatal("unsandboxed role must be refused")
	}
	o.Role = "coder"
	o.OAuthPath = "relative/cred"
	if _, err := RenderShim(o); err == nil {
		t.Fatal("relative token path must be refused")
	}
}
