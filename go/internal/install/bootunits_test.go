package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bootUID = 1000 // fakeHost.euid

// darwinWithTemplates is a darwin fake host whose clone already holds the repo's launchd templates.
func darwinWithTemplates(t *testing.T) *fakeHost {
	t.Helper()
	f := newDarwin(t)
	f.login(t)
	dst := filepath.Join(f.HomeDir, "factory", "launchd")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.HomeDir, "factory", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.HomeDir, "factory", "db", "factory.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	src := "../../../launchd"
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func launchctlCalls(f *fakeHost) []string {
	var out []string
	for _, c := range f.calls {
		if c[0] == "launchctl" {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

func indexOfStr(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}
	return -1
}

func TestDarwinBootUnitsDryRun(t *testing.T) {
	f := darwinWithTemplates(t)
	rc, out, _ := runMain(t, f, "--resume", "--dry-run")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	dir := filepath.Join(f.HomeDir, "Library", "LaunchAgents")
	for _, l := range []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick"} {
		p := filepath.Join(dir, l+".plist")
		for _, want := range []string{"write " + p, "launchctl bootstrap gui/1000 " + p, "<string>" + l + "</string>"} {
			if !strings.Contains(out, want) {
				t.Errorf("dry-run lacks %q\n%s", want, out)
			}
		}
	}
	for _, want := range []string{"<string>--deadline</string>", "/opt/homebrew/bin", "<string>" + f.HomeDir + "/factory</string>"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run lacks %q", want)
		}
	}
	if strings.Contains(out, "hivemill.serve") {
		t.Errorf("serve planned without --with-serve\n%s", out)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("dry-run created %s", dir)
	}
	if got := launchctlCalls(f); len(got) != 0 {
		t.Errorf("dry-run ran launchctl: %v", got)
	}
	if f.mutated("launchctl") {
		t.Errorf("dry-run mutated")
	}
}

func TestDarwinBootUnitsDryRunBeforeClone(t *testing.T) {
	f := newDarwin(t)
	rc, out, _ := runMain(t, f, "--resume", "--dry-run")
	if rc != 0 || !strings.Contains(out, "launchctl bootstrap gui/1000 ") || !strings.Contains(out, "(after the clone)") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
}

func TestDarwinBootUnitsInstall(t *testing.T) {
	f := darwinWithTemplates(t)
	rc, out, _ := runMain(t, f, "--resume", "--yes")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	dir := filepath.Join(f.HomeDir, "Library", "LaunchAgents")
	for _, l := range []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick"} {
		p := filepath.Join(dir, l+".plist")
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o644 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "hivemill.serve.plist")); err == nil {
		t.Errorf("serve plist written without --with-serve")
	}
	n := 0
	for _, c := range launchctlCalls(f) {
		if strings.HasPrefix(c, "launchctl bootstrap ") {
			n++
			if strings.Contains(c, "hivemill.serve") {
				t.Errorf("serve bootstrapped: %s", c)
			}
		}
	}
	if n != 3 {
		t.Errorf("bootstrap calls = %d, want 3: %v", n, launchctlCalls(f))
	}
	tick, _ := os.ReadFile(filepath.Join(dir, "hivemill.tick.plist"))
	if !strings.Contains(string(tick), "/opt/homebrew/bin") || !strings.Contains(string(tick), f.HomeDir+"/.local/bin") {
		t.Errorf("PATH of tick plist:\n%s", tick)
	}
}

func TestDarwinBootUnitsInstallWithServe(t *testing.T) {
	f := darwinWithTemplates(t)
	if rc, out, _ := runMain(t, f, "--resume", "--yes", "--with-serve"); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	p := filepath.Join(f.HomeDir, "Library", "LaunchAgents", "hivemill.serve.plist")
	if indexOfStr(launchctlCalls(f), "launchctl bootstrap gui/1000 "+p) < 0 {
		t.Errorf("serve not bootstrapped: %v", launchctlCalls(f))
	}
}

func TestDarwinBootUnitsReinstall(t *testing.T) {
	f := darwinWithTemplates(t)
	for i := 0; i < 2; i++ {
		if rc, out, _ := runMain(t, f, "--resume", "--yes"); rc != 0 {
			t.Fatalf("run %d rc=%d\n%s", i, rc, out)
		}
	}
	calls := launchctlCalls(f)
	for _, l := range []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick"} {
		p := filepath.Join(f.HomeDir, "Library", "LaunchAgents", l+".plist")
		out, boot := indexOfStr(calls, "launchctl bootout gui/1000/"+l), indexOfStr(calls, "launchctl bootstrap gui/1000 "+p)
		if out < 0 || boot < 0 || out > boot {
			t.Errorf("%s: bootout at %d, bootstrap at %d\n%v", l, out, boot, calls)
		}
	}
}

func TestDarwinBootUnitsReinstallToleratesNotLoaded(t *testing.T) {
	f := darwinWithTemplates(t)
	f.rules = func(argv []string) (int, string, bool) {
		if argv[0] == "launchctl" && argv[1] == "bootout" {
			return 113, "Could not find service", true
		}
		return 0, "", false
	}
	if rc, out, _ := runMain(t, f, "--resume", "--yes"); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
}

func TestDarwinBootUnitsBootstrapFailureFailsStep(t *testing.T) {
	f := darwinWithTemplates(t)
	f.rules = func(argv []string) (int, string, bool) {
		if argv[0] == "launchctl" && argv[1] == "bootstrap" {
			return 5, "Bootstrap failed: 5: Input/output error", true
		}
		return 0, "", false
	}
	rc, out, _ := runMain(t, f, "--resume", "--yes")
	if rc != 1 || !strings.Contains(out, "RESULT FAIL step=services") || !strings.Contains(out, "Input/output error") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
}

func TestDarwinBootUnitsUninstall(t *testing.T) {
	f := darwinWithTemplates(t)
	if rc, out, _ := runMain(t, f, "--resume", "--yes", "--with-serve"); rc != 0 {
		t.Fatalf("install rc=%d\n%s", rc, out)
	}
	f.calls = nil
	rc, out, _ := runMain(t, f, "--yes", "--uninstall-boot-units")
	if rc != 0 || !strings.Contains(out, "RESULT PASS") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	calls := launchctlCalls(f)
	for _, l := range []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick", "hivemill.serve"} {
		if indexOfStr(calls, "launchctl bootout gui/1000/"+l) < 0 {
			t.Errorf("no bootout of %s: %v", l, calls)
		}
		if _, err := os.Stat(filepath.Join(f.HomeDir, "Library", "LaunchAgents", l+".plist")); err == nil {
			t.Errorf("%s.plist still there", l)
		}
	}
}

func TestDarwinBootUnitsUninstallDryRunChangesNothing(t *testing.T) {
	f := darwinWithTemplates(t)
	if rc, out, _ := runMain(t, f, "--resume", "--yes"); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	f.calls = nil
	rc, out, _ := runMain(t, f, "--dry-run", "--uninstall-boot-units")
	if rc != 0 || !strings.Contains(out, "launchctl bootout gui/1000/hivemill.tick") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	if len(launchctlCalls(f)) != 0 {
		t.Errorf("dry-run ran launchctl")
	}
	if _, err := os.Stat(filepath.Join(f.HomeDir, "Library", "LaunchAgents", "hivemill.tick.plist")); err != nil {
		t.Errorf("plist removed by dry-run: %v", err)
	}
}

func TestUninstallBootUnitsRefusedOnLinuxAndCheck(t *testing.T) {
	if rc, _, errw := runMain(t, newFake(t), "--yes", "--uninstall-boot-units"); rc != 2 || !strings.Contains(errw, "macOS") {
		t.Errorf("linux: rc=%d %s", rc, errw)
	}
	if rc, _, _ := runMain(t, newDarwin(t), "--check", "--uninstall-boot-units"); rc != 2 {
		t.Errorf("--check: rc=%d", rc)
	}
}

func TestDarwinNoBootUnitsAliasSkipsStep(t *testing.T) {
	for _, flag := range []string{"--no-systemd", "--no-boot-units"} {
		f := darwinWithTemplates(t)
		rc, out, _ := runMain(t, f, "--resume", "--yes", flag)
		if rc != 0 || len(launchctlCalls(f)) != 0 {
			t.Errorf("%s: rc=%d launchctl=%v\n%s", flag, rc, launchctlCalls(f), out)
		}
		if _, err := os.Stat(filepath.Join(f.HomeDir, "Library", "LaunchAgents")); err == nil {
			t.Errorf("%s: wrote LaunchAgents", flag)
		}
	}
}

func TestLinuxNoBootUnitsAliasSkipsSystemd(t *testing.T) {
	f := newFake(t)
	f.login(t)
	rc, out, _ := runMain(t, f, "--resume", "--yes", "--no-boot-units")
	if rc != 0 || f.mutated("systemctl") || f.mutated("systemd/install.sh") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
}

func TestDarwinTickPlistGolden(t *testing.T) {
	f := darwinWithTemplates(t)
	if rc, out, _ := runMain(t, f, "--resume", "--yes"); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	for _, l := range []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick"} {
		got, err := os.ReadFile(filepath.Join(f.HomeDir, "Library", "LaunchAgents", l+".plist"))
		if err != nil {
			t.Fatal(err)
		}
		g := strings.ReplaceAll(string(got), f.HomeDir, "<HOME>")
		gp := filepath.Join("testdata", "darwin", l+".plist.golden")
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(gp, []byte(g), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(gp)
		if err != nil || string(want) != g {
			t.Errorf("%s differs from %s (UPDATE_GOLDEN=1 regenerates)\n%s", l, gp, g)
		}
	}
}

func TestDarwinBootUnitsBootoutFailureIsNotFatalAndBootstrapRetries(t *testing.T) {
	f := darwinWithTemplates(t)
	fails := 0
	f.rules = func(argv []string) (int, string, bool) {
		if argv[0] != "launchctl" {
			return 0, "", false
		}
		if argv[1] == "bootout" {
			return 5, "Boot-out failed: 5: Input/output error", true
		}
		if fails < 1 { // the first bootstrap loses the race with the asynchronous bootout
			fails++
			return 5, "Bootstrap failed: 5", true
		}
		return 0, "", false
	}
	if rc, out, _ := runMain(t, f, "--resume", "--yes"); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
}
