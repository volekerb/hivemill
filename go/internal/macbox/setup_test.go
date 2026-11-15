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
	if !strings.Contains(s, `chmod +a "group:_hivemill allow search" "$HOME_REAL"`) {
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
	if !strings.Contains(s, `"$FACTORY_REAL/db"`) || !strings.Contains(aclDB, "write") ||
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
			"owner-home":   {OwnerUser: "a", OwnerHome: v, FactoryHome: v + "/f"},
			"factory-home": {OwnerUser: "a", OwnerHome: "/Users", FactoryHome: v},
			"launcher":     {OwnerUser: "a", OwnerHome: "/h", FactoryHome: "/h/f", LauncherPath: LauncherDir + "/" + strings.TrimPrefix(v, "/Users/")},
			"owner-user":   {OwnerUser: strings.TrimPrefix(v, "/Users/"), OwnerHome: "/h", FactoryHome: "/h/f"},
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
		{OwnerUser: "a b", OwnerHome: "/h", FactoryHome: "/h/f"},
		{OwnerUser: "ALL", OwnerHome: "/h", FactoryHome: "/h/f"},
		{OwnerUser: "a", OwnerHome: "rel", FactoryHome: "rel/f"},
		{OwnerUser: "a", OwnerHome: "/h", FactoryHome: "/h/f", LauncherPath: LauncherDir + "/my launch"},
		{OwnerUser: "a", OwnerHome: "/h", FactoryHome: "/h/f", LauncherPath: LauncherDir + "/x,y"},
		{OwnerUser: "", OwnerHome: "/h", FactoryHome: "/h/f"},
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

func TestSetupRejectLauncherOutsideRootPrefix(t *testing.T) {
	for _, l := range []string{
		"/usr/local/bin/macbox-launch", // install would re-own a Homebrew prefix
		"/tmp/x",
		"/Users/a/x/y/z", // owner-writable ancestors
		LauncherDir + "/sub/x",
		LauncherDir + "/",
		LauncherDir + "/..",
		LauncherDir + "/.hidden",
		"/usr/local/libexec/hivemillx/macbox-launch",
	} {
		o := setupOpts
		o.LauncherPath = l
		if _, err := SetupScript(o); err == nil {
			t.Errorf("SetupScript accepted launcher %q", l)
		}
		if _, err := UninstallScript(o); err == nil {
			t.Errorf("UninstallScript accepted launcher %q", l)
		}
	}
	o := setupOpts
	o.LauncherPath = LauncherDir + "/macbox-launch-2"
	s, err := SetupScript(o)
	if err != nil {
		t.Fatalf("rejected a launcher inside %s: %v", LauncherDir, err)
	}
	if !strings.Contains(s, "LAUNCHER='"+LauncherDir+"/macbox-launch-2'") {
		t.Error("custom launcher name not used")
	}
}

func TestSetupRejectFactoryHomeOutsideOwnerHome(t *testing.T) {
	for _, c := range []struct{ home, factory string }{
		{"/Users/a", "/"},
		{"/Users/a", "/etc"},
		{"/Users/a", "/Users/a"},
		{"/Users/a", "/Users/a/"},
		{"/Users/a", "/Users/a/."},
		{"/Users/a", "/Users/ab/factory"},
		{"/Users/a", "/Users"},
		{"/", "/factory"},
	} {
		o := SetupOpts{OwnerUser: "a", OwnerHome: c.home, FactoryHome: c.factory}
		if _, err := SetupScript(o); err == nil {
			t.Errorf("SetupScript accepted home %q factory %q", c.home, c.factory)
		}
		if _, err := UninstallScript(o); err == nil {
			t.Errorf("UninstallScript accepted home %q factory %q", c.home, c.factory)
		}
	}
	o := SetupOpts{OwnerUser: "a", OwnerHome: "/Users/a/", FactoryHome: "/Users/a/src/factory/"}
	s, err := SetupScript(o)
	if err != nil {
		t.Fatalf("rejected a nested factory home: %v", err)
	}
	if !strings.Contains(s, "OWNER_HOME='/Users/a'\n") || !strings.Contains(s, "FACTORY_HOME='/Users/a/src/factory'\n") {
		t.Error("paths are not cleaned in the script")
	}
}

// The script re-checks the resolved (pwd -P) paths and the launcher
// ancestors before its first change, and never re-owns an existing directory.
func TestSetupRuntimeChecksBeforeChanges(t *testing.T) {
	s := mustSetup(t)
	first := strings.Index(s, "dscl . -create")
	for _, w := range []string{`pwd -P`, `"$HOME_REAL"/?*) ;;`, `root_dir_ok "$d"`, `-user root ! -perm -0020 ! -perm -0002`} {
		i := strings.Index(s, w)
		if i < 0 || i > first {
			t.Errorf("%q missing or after the first change (at %d, first change %d)", w, i, first)
		}
	}
	if strings.Contains(s, "install -d") {
		t.Error("install -d re-owns existing directories")
	}
	if strings.Count(s, `chown root:wheel "$d"`) != 1 ||
		!strings.Contains(s, "if [ ! -e \"$d\" ]; then\n    mkdir \"$d\"\n    chown root:wheel \"$d\"") {
		t.Error("launcher directories are not created only when absent")
	}
	if strings.Contains(s, "GroupMembership") || !strings.Contains(s, "dseditgroup -o edit -a") {
		t.Error("group membership is not added idempotently")
	}
}

func TestUninstallRemovesIntermediateSearch(t *testing.T) {
	un := mustUninstall(t)
	for _, w := range []string{`chmod -a "group:_hivemill allow search" "$d"`, `chmod -a "group:_hivemill allow search" "$HOME_REAL"`, `rmdir "$LAUNCHER_DIR"`} {
		if !strings.Contains(un, w) {
			t.Errorf("uninstall lacks %q", w)
		}
	}
	if !strings.Contains(mustSetup(t), `chmod +a "group:_hivemill allow search" "$d"`) {
		t.Error("setup does not grant search on dirs between home and factory")
	}
}
