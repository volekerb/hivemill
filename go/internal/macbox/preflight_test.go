package macbox

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeFile struct {
	mode os.FileMode
	uid  int
}

func (f fakeFile) Name() string       { return "t" }
func (f fakeFile) Size() int64        { return 0 }
func (f fakeFile) Mode() os.FileMode  { return f.mode }
func (f fakeFile) ModTime() time.Time { return time.Time{} }
func (f fakeFile) IsDir() bool        { return false }
func (f fakeFile) Sys() any           { return nil }

type fakeRunner struct {
	accounts      map[string]bool
	sudoFail      map[string]bool
	noSandboxExec bool
	probeFail     string
	viewReadable  bool
	aclWritable   bool
	tokens        map[string]fakeFile
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if name == "sandbox-exec" {
		if f.noSandboxExec {
			return "", fmt.Errorf("missing")
		}
		return "/usr/bin/sandbox-exec", nil
	}
	return "", fmt.Errorf("not found")
}

func (f *fakeRunner) Run(name string, args ...string) (int, string, string) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	if name == "dscl" {
		u := args[2]
		u = strings.TrimPrefix(u, "/Users/")
		if f.accounts[u] {
			return 0, "", ""
		}
		return 1, "", "not found"
	}
	if name == "sudo" && len(args) >= 3 && args[0] == "-n" && args[1] == "-u" {
		account := args[2]
		if f.sudoFail[account] {
			return 1, "", "sudo denied"
		}
		rest := args[3:]
		if len(rest) >= 2 && rest[0] == "true" {
			return 0, "", ""
		}
		if len(rest) >= 2 && rest[0] == "test" && rest[1] == "-r" {
			if f.viewReadable {
				return 0, "", ""
			}
			return 1, "", "denied"
		}
		if len(rest) >= 1 && rest[0] == "touch" {
			if f.aclWritable {
				return 0, "", ""
			}
			return 1, "", "permission denied"
		}
		if strings.Contains(cmd, "sandbox-exec") && strings.Contains(cmd, "-c") {
			for _, p := range SelfTestPlan(baseProfileIn()) {
				if !strings.Contains(cmd, p.Path) {
					continue
				}
				if f.probeFail == p.Name {
					return 1, "", "probe failed"
				}
				want := p.WantAllowed
				if p.SetupAsOwner {
					want = false
				}
				if want {
					return 0, "", ""
				}
				return 1, "", "denied"
			}
			return 0, "", ""
		}
	}
	_ = cmd
	return 0, "", ""
}

func (f *fakeRunner) Stat(name string) (os.FileInfo, error) {
	fi, ok := f.tokens[name]
	if !ok {
		return nil, fmt.Errorf("missing %s", name)
	}
	return fi, nil
}

func baseProfileIn() ProfileInput {
	return ProfileInput{
		Role: RoleCoder, OwnerHome: "/Users/o", FactoryHome: "/Users/o/factory",
		AgentDir: "/Users/o/.local/a", CloneDir: "/Users/o/factory/worktrees/x", CloneWritable: true,
	}
}

func baseSpec() Spec {
	return Spec{
		GOOS: "darwin", OwnerUID: 501, OwnerHome: "/Users/o", FactoryHome: "/Users/o/factory",
		Role: "coder", Launcher: DefaultLauncher, ProfilePath: "/Users/o/.local/a/profile.sb",
		CloneDir: "/Users/o/factory/worktrees/x", RealClaude: "/bin/sh", AgentDir: "/Users/o/.local/a",
		OAuthPath: "/Users/o/.config/factory/cred-oauth",
		GhPath:    "/Users/o/.config/factory/cred-gh-rw",
		ProfileIn:  baseProfileIn(),
	}
}

func goodRunner() *fakeRunner {
	return &fakeRunner{
		accounts: map[string]bool{AccountRO: true, AccountRW: true},
		tokens: map[string]fakeFile{
			"/Users/o/.config/factory/cred-oauth": {mode: 0o600, uid: 501},
			"/Users/o/.config/factory/cred-gh-rw": {mode: 0o600, uid: 501},
		},
	}
}

func TestNonDarwinOS(t *testing.T) {
	err := Preflight(Spec{Role: "coder"}, goodRunner())
	ref, ok := AsRefusal(err)
	if !ok || ref.Reason != ReasonOS {
		t.Fatalf("got %v want os refusal", err)
	}
}

func TestPreflight(t *testing.T) {
	t.Run("os/refused", func(t *testing.T) {
		err := Preflight(Spec{Role: "coder"}, goodRunner())
		ref, ok := AsRefusal(err)
		if !ok || ref.Reason != ReasonOS {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("os/accepted", func(t *testing.T) {
		s := baseSpec()
		if err := Preflight(s, goodRunner()); err != nil {
			t.Fatalf("darwin sim must pass os gate: %v", err)
		}
	})
	t.Run("user/refused", func(t *testing.T) {
		r := goodRunner()
		delete(r.accounts, AccountRW)
		ref, ok := AsRefusal(Preflight(baseSpec(), r))
		if !ok || ref.Reason != ReasonUser {
			t.Fatalf("got %v", ref)
		}
	})
	t.Run("user/accepted", func(t *testing.T) {
		if err := Preflight(baseSpec(), goodRunner()); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("sudo/refused", func(t *testing.T) {
		r := goodRunner()
		r.sudoFail = map[string]bool{AccountRW: true}
		ref, ok := AsRefusal(Preflight(baseSpec(), r))
		if !ok || ref.Reason != ReasonSudo {
			t.Fatalf("got %v", ref)
		}
	})
	t.Run("sudo/accepted", func(t *testing.T) {
		if err := Preflight(baseSpec(), goodRunner()); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("seatbelt/refused", func(t *testing.T) {
		r := goodRunner()
		r.probeFail = "clone-writable"
		ref, ok := AsRefusal(Preflight(baseSpec(), r))
		if !ok || ref.Reason != ReasonSeatbelt {
			t.Fatalf("got %v", ref)
		}
	})
	t.Run("seatbelt/accepted", func(t *testing.T) {
		if err := Preflight(baseSpec(), goodRunner()); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("view/refused", func(t *testing.T) {
		r := goodRunner()
		r.viewReadable = true
		ref, ok := AsRefusal(Preflight(baseSpec(), r))
		if !ok || ref.Reason != ReasonView {
			t.Fatalf("got %v", ref)
		}
	})
	t.Run("view/accepted", func(t *testing.T) {
		if err := Preflight(baseSpec(), goodRunner()); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("acl/refused", func(t *testing.T) {
		r := goodRunner()
		r.aclWritable = true
		ref, ok := AsRefusal(Preflight(baseSpec(), r))
		if !ok || ref.Reason != ReasonACL {
			t.Fatalf("got %v", ref)
		}
	})
	t.Run("acl/accepted", func(t *testing.T) {
		if err := Preflight(baseSpec(), goodRunner()); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("accepted", func(t *testing.T) {
		if err := Preflight(baseSpec(), goodRunner()); err != nil {
			t.Fatalf("all-pass: %v", err)
		}
	})
}

func TestPreflightFailClosedWithoutSeatbelt(t *testing.T) {
	s := baseSpec()
	s.SkipSeatbeltSelfTest = true
	s.CloneDir = ""
	if err := Preflight(s, goodRunner()); err == nil {
		t.Fatal("without seatbelt probe gate other checks must still refuse")
	}
}