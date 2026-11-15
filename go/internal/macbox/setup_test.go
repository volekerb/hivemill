package macbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var setupOpts = SetupOpts{OwnerUser: "a", OwnerHome: "/Users/a", FactoryHome: "/Users/a/factory"}

func mustSetup(t *testing.T) string {
	t.Helper()
	s, err := SetupScript(setupOpts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustUninstall(t *testing.T) string {
	t.Helper()
	s, err := UninstallScript(setupOpts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "setup", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from generated script; run go test ./internal/macbox -update and review", p)
	}
}

func TestSetupGolden(t *testing.T) {
	golden(t, "setup.golden", mustSetup(t))
	golden(t, "uninstall.golden", mustUninstall(t))
}

func TestSetupParsesWithSh(t *testing.T) {
	for name, s := range map[string]string{"setup": mustSetup(t), "uninstall": mustUninstall(t)} {
		p := filepath.Join(t.TempDir(), name+".sh")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", name, err, out)
		}
	}
}

func TestSudoersExactLine(t *testing.T) {
	s := mustSetup(t)
	want := "a ALL=(_hivemill_ro,_hivemill_rw) NOPASSWD:SETENV: /usr/local/libexec/hivemill/macbox-launch"
	m := regexp.MustCompile(`(?s)<<'SUDOERS_EOF'\n(.*?)\nSUDOERS_EOF`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("no sudoers heredoc")
	}
	if m[1] != want {
		t.Errorf("sudoers body %q, want exactly %q", m[1], want)
	}
	// owner to worker only: no rule names a worker account as the caller.
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "_hivemill") && strings.Contains(l, "ALL=") {
			t.Errorf("worker account is a sudoers caller: %q", l)
		}
	}
}

func TestSudoersValidatedFirst(t *testing.T) {
	s := mustSetup(t)
	v := strings.Index(s, "visudo -cf")
	mv := strings.Index(s, "mv \"$TMP\" /etc/sudoers.d/hivemill")
	if v < 0 || mv < 0 || v > mv {
		t.Fatalf("visudo -cf at %d must come before the mv at %d", v, mv)
	}
	if !strings.Contains(s, "chmod 0440") {
		t.Error("sudoers file is not mode 0440")
	}
}

func TestACLHomeTraverseOnly(t *testing.T) {
	s := mustSetup(t)
	if !strings.Contains(s, `chmod +a "group:_hivemill allow search" "$OWNER_HOME"`) {
		t.Error("OwnerHome ACL is not exactly search")
	}
	if aclHome != "group:_hivemill allow search" {
		t.Errorf("aclHome %q", aclHome)
	}
	for _, bad := range []string{"list", "read", "write"} {
		if strings.Contains(aclHome, bad) {
			t.Errorf("home ACL grants %s", bad)
		}
	}
	if !strings.Contains(s, `"$FACTORY_HOME/db"`) || !strings.Contains(aclDB, "write") ||
		!strings.Contains(aclDB, "file_inherit") || !strings.Contains(aclDB, "directory_inherit") {
		t.Error("FactoryHome/db lacks inherited write")
	}
	if strings.Contains(aclRead, "write") || strings.Contains(aclRead, "add_file") {
		t.Error("factory tree ACL grants write")
	}
}

func TestSetupRootAndCaller(t *testing.T) {
	s := mustSetup(t)
	for _, w := range []string{"id -u", "SUDO_USER"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestSetupRejectInjection(t *testing.T) {
	for name, v := range map[string]string{"newline": "/Users/a\nrm -rf /", "quote": "/Users/a'b", "dquote": `/Users/a"b`, "backslash": `/Users/a\b`} {
		for field, o := range map[string]SetupOpts{
			"owner-home":   {OwnerUser: "a", OwnerHome: v, FactoryHome: "/f"},
			"factory-home": {OwnerUser: "a", OwnerHome: "/h", FactoryHome: v},
			"launcher":     {OwnerUser: "a", OwnerHome: "/h", FactoryHome: "/f", LauncherPath: v},
			"owner-user":   {OwnerUser: strings.TrimPrefix(v, "/Users/"), OwnerHome: "/h", FactoryHome: "/f"},
		} {
			if _, err := SetupScript(o); err == nil {
				t.Errorf("SetupScript accepted %s in %s", name, field)
			}
			if _, err := UninstallScript(o); err == nil {
				t.Errorf("UninstallScript accepted %s in %s", name, field)
			}
		}
	}
	for _, o := range []SetupOpts{
		{OwnerUser: "a b", OwnerHome: "/h", FactoryHome: "/f"},
		{OwnerUser: "a", OwnerHome: "rel", FactoryHome: "/f"},
		{OwnerUser: "a", OwnerHome: "/h", FactoryHome: "/f", LauncherPath: "/opt/my launch"},
		{OwnerUser: "a", OwnerHome: "/h", FactoryHome: "/f", LauncherPath: "/opt/x,y"},
		{OwnerUser: "", OwnerHome: "/h", FactoryHome: "/f"},
	} {
		if _, err := SetupScript(o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}

func TestUninstallReversesEverySetupStep(t *testing.T) {
	setup, un := mustSetup(t), mustUninstall(t)
	for _, id := range []string{StepAccounts, StepLauncher, StepSudoers, StepACL} {
		if !strings.Contains(setup, "echo \"step "+id+":") {
			t.Errorf("setup lacks step %s", id)
		}
		if !strings.Contains(un, "echo \"undo step "+id+":") {
			t.Errorf("uninstall lacks undo of step %s", id)
		}
	}
	for _, w := range []string{"chmod -a", "rm -f /etc/sudoers.d/hivemill", `rm -f "$LAUNCHER"`, `dscl . -delete "/Users/$u"`, "dscl . -delete /Groups/", "id -u", "SUDO_USER"} {
		if !strings.Contains(un, w) {
			t.Errorf("uninstall lacks %q", w)
		}
	}
}

func TestUninstallIdempotentShape(t *testing.T) {
	un := mustUninstall(t)
	// every destructive dscl is guarded by a read; rm uses -f.
	if strings.Count(un, "dscl . -read") < 2 || strings.Contains(un, "rm /") {
		t.Error("uninstall is not guarded for a second run")
	}
}
