package macbox

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden profiles")

var testPaths = RolePaths{
	OwnerHome:     "/Users/owner",
	FactoryHome:   "/Users/owner/factory",
	AgentDir:      "/Users/owner/factory/agents/a1",
	CoderClone:    "/Users/owner/factory/clones/T-1-coder",
	QAClone:       "/Users/owner/factory/clones/T-1-qa-clone",
	GateTree:      "/Users/owner/factory/gate/T-1",
	ReadOnlyPaths: []string{"/opt/homebrew", "/usr/local/go"},
}

var allRoles = []Role{RoleReviewer, RoleQA, RoleCoder, RoleGate}

func profileFor(t *testing.T, r Role) string {
	t.Helper()
	in, err := ForRole(r, testPaths)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Profile(in)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGolden(t *testing.T) {
	for _, r := range allRoles {
		t.Run(string(r), func(t *testing.T) {
			got := profileFor(t, r)
			f := filepath.Join("testdata", string(r)+".sb")
			if *update {
				if err := os.WriteFile(f, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/macbox -update)", err)
			}
			if string(want) != got {
				t.Errorf("profile differs from %s; run with -update and review the diff", f)
			}
		})
	}
}

func TestProfileDenyDefaultFirst(t *testing.T) {
	for _, r := range allRoles {
		lines := strings.Split(profileFor(t, r), "\n")
		if lines[0] != "(version 1)" || lines[1] != "(deny default)" {
			t.Errorf("%s: first lines %q", r, lines[:2])
		}
		g, _ := os.ReadFile(filepath.Join("testdata", string(r)+".sb"))
		gl := strings.Split(string(g), "\n")
		if gl[0] != "(version 1)" || gl[1] != "(deny default)" {
			t.Errorf("%s golden: first lines %q", r, gl[:2])
		}
	}
}

func TestProfileRejectBadPaths(t *testing.T) {
	for _, bad := range []string{`/a"b`, `/a\b`, "/a\x00b", "/a\nb", "rel/path", ""} {
		for _, field := range []string{"OwnerHome", "FactoryHome", "AgentDir", "CloneDir", "RO"} {
			in, _ := ForRole(RoleCoder, testPaths)
			switch field {
			case "OwnerHome":
				in.OwnerHome = bad
			case "FactoryHome":
				in.FactoryHome = bad
			case "AgentDir":
				in.AgentDir = bad
			case "CloneDir":
				in.CloneDir = bad
			case "RO":
				in.ReadOnlyPaths = []string{bad}
			}
			s, err := Profile(in)
			if err == nil || s != "" {
				t.Errorf("%s=%q: want error and empty profile, got err=%v len=%d", field, bad, err, len(s))
			}
		}
	}
	if _, err := Profile(ProfileInput{Role: "root"}); err == nil {
		t.Error("unknown role accepted")
	}
}

func TestProfileForbiddenMach(t *testing.T) {
	re := regexp.MustCompile(`SecurityServer|securityd|appleevents|pasteboard`)
	for _, r := range allRoles {
		if re.MatchString(profileFor(t, r)) {
			t.Errorf("%s: forbidden Mach service in profile", r)
		}
	}
	for _, s := range machServices {
		if re.MatchString(s) || strings.Contains(s, "launchd") {
			t.Errorf("forbidden service in machServices: %s", s)
		}
	}
}

func TestProfileCanonicalPaths(t *testing.T) {
	in, _ := ForRole(RoleCoder, testPaths)
	in.AgentDir = "/tmp/x"
	in.ReadOnlyPaths = []string{"/var/folders/y", "/usr/local/../local/go/"}
	s, err := Profile(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`(subpath "/private/tmp/x")`, `(subpath "/private/var/folders/y")`, `(subpath "/usr/local/go")`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(s, `"/tmp/x"`) || strings.Contains(s, `"/var/folders`) {
		t.Error("non-canonical path rendered")
	}
	if c, _ := canonical("/tmpfoo/x"); c != "/tmpfoo/x" {
		t.Errorf("/tmpfoo wrongly rewritten: %s", c)
	}
}

func TestProfileOwnerSecretsDenied(t *testing.T) {
	for _, r := range allRoles {
		s := profileFor(t, r)
		for _, d := range []string{".ssh", ".claude", ".config/gh", ".config/factory", "Library/Keychains"} {
			if !strings.Contains(s, `(deny file* (subpath "/Users/owner/`+d+`"))`) {
				t.Errorf("%s: no deny for %s", r, d)
			}
		}
		// the denies come after the last allow so they win
		if strings.LastIndex(s, "(allow") > strings.Index(s, `(deny file* (subpath "/Users/owner/.ssh"`) {
			t.Errorf("%s: an allow rule follows the owner-secret denies", r)
		}
		if !strings.Contains(s, `(deny file* (subpath "/private/tmp"))`) {
			t.Errorf("%s: shared tmp not denied", r)
		}
	}
}

func TestForRole(t *testing.T) {
	writes := func(s, p string) bool { return strings.Contains(s, `(allow file-write* (subpath "`+p+`"))`) }
	rev, qa, cod, gate := profileFor(t, RoleReviewer), profileFor(t, RoleQA), profileFor(t, RoleCoder), profileFor(t, RoleGate)
	if writes(rev, testPaths.CoderClone) || !strings.Contains(rev, `(allow file-read* (subpath "`+testPaths.CoderClone+`"))`) {
		t.Error("reviewer must read, not write, the coder clone")
	}
	if !writes(qa, testPaths.QAClone) || strings.Contains(qa, testPaths.CoderClone) {
		t.Error("qa must write only its qa clone")
	}
	if !writes(cod, testPaths.CoderClone) || strings.Contains(cod, testPaths.QAClone) {
		t.Error("coder must write its clone")
	}
	if !writes(gate, testPaths.GateTree) || strings.Contains(gate, "network-outbound") {
		t.Error("gate: tree RW and no network expected")
	}
	for name, s := range map[string]string{"reviewer": rev, "qa": qa, "coder": cod} {
		if !strings.Contains(s, "(allow network-outbound)") {
			t.Errorf("%s: network expected", name)
		}
	}
	if _, err := ForRole("x", testPaths); err == nil {
		t.Error("unknown role accepted")
	}
	if _, err := ForRole(RoleQA, RolePaths{}); err == nil {
		t.Error("missing clone accepted")
	}
}

func TestSelfTestPlan(t *testing.T) {
	for _, r := range allRoles {
		in, _ := ForRole(r, testPaths)
		var canary, clone bool
		for _, p := range SelfTestPlan(in, "n1") {
			if !strings.HasSuffix(p.Path, "-n1") {
				t.Errorf("%s: probe path %s lacks the run nonce", r, p.Path)
			}
			if p.Name == "owner-home-canary" && p.Kind == ProbeRead && !p.WantAllowed && strings.HasPrefix(p.Path, testPaths.OwnerHome+"/") {
				canary = true
			}
			if p.Kind == ProbeWrite && strings.HasPrefix(p.Path, in.CloneDir+"/") {
				clone = true
				if p.WantAllowed != in.CloneWritable {
					t.Errorf("%s: clone probe WantAllowed=%v", r, p.WantAllowed)
				}
			}
		}
		if !canary || !clone {
			t.Errorf("%s: canary=%v clone=%v", r, canary, clone)
		}
		if (r == RoleCoder || r == RoleQA) && !in.CloneWritable {
			t.Errorf("%s clone not writable", r)
		}
	}
}

func TestProfileTmpDenyBeforeAllows(t *testing.T) {
	in, _ := ForRole(RoleCoder, testPaths)
	in.AgentDir = "/tmp/agent"
	s, err := Profile(in)
	if err != nil {
		t.Fatal(err)
	}
	deny := strings.Index(s, `(deny file* (subpath "/private/tmp"))`)
	for _, a := range []string{`(allow file-read* (subpath "/private/tmp/agent"))`, `(allow file-write* (subpath "/private/tmp/agent"))`} {
		if i := strings.Index(s, a); i < 0 || i < deny {
			t.Errorf("%s must exist after the /private/tmp deny (deny=%d, allow=%d)", a, deny, i)
		}
	}
}

func TestProfileRejectSecretOverlap(t *testing.T) {
	in, _ := ForRole(RoleCoder, testPaths)
	in.AgentDir = "/Users/owner/.config/factory/agents/a1"
	if s, err := Profile(in); err == nil || s != "" {
		t.Errorf("want overlap error, got %v", err)
	}
}
