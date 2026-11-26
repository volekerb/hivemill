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
	for _, l := range bootJobs {
		b, err := os.ReadFile(filepath.Join(src, l+".plist.in"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, l+".plist.in"), b, 0o644); err != nil {
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
	for _, l := range bootJobs {
		p := filepath.Join(dir, l+".plist")
		for _, want := range []string{"write " + p, "launchctl bootstrap gui/1000 " + p, "<string>" + l + "</string>"} {
			if !strings.Contains(out, want) {
				t.Errorf("dry-run lacks %q\n%s", want, out)
			}
		}
	}
	for _, want := range []string{"/opt/homebrew/bin", "<string>" + f.HomeDir + "/factory</string>"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run lacks %q", want)
		}
	}
	for _, old := range []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick", "hivemill.serve"} {
		if strings.Contains(out, old) {
			t.Errorf("%s planned\n%s", old, out)
		}
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
	for _, l := range bootJobs {
		p := filepath.Join(dir, l+".plist")
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o644 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != len(bootJobs) {
		t.Errorf("plists written: %v %v", ents, err)
	}
	n := 0
	for _, c := range launchctlCalls(f) {
		if strings.HasPrefix(c, "launchctl bootstrap ") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("bootstrap calls = %d, want 3: %v", n, launchctlCalls(f))
	}
	daemon, _ := os.ReadFile(filepath.Join(dir, "hivemill.daemon.plist"))
	if !strings.Contains(string(daemon), "/opt/homebrew/bin") || !strings.Contains(string(daemon), f.HomeDir+"/.local/bin") {
		t.Errorf("PATH of daemon plist:\n%s", daemon)
	}
}

func TestDarwinBootUnitsInstallWithServe(t *testing.T) {
	f := darwinWithTemplates(t)
	if rc, out, _ := runMain(t, f, "--resume", "--yes", "--with-serve"); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	// --with-serve is still accepted but there is no serve job any more
	for _, c := range launchctlCalls(f) {
		if strings.Contains(c, "hivemill.serve") {
			t.Errorf("serve job managed: %s", c)
		}
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
	for _, l := range bootJobs {
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
	for _, l := range ldLabels {
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
	if rc != 0 || !strings.Contains(out, "launchctl bootout gui/1000/hivemill.daemon") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	if len(launchctlCalls(f)) != 0 {
		t.Errorf("dry-run ran launchctl")
	}
	if _, err := os.Stat(filepath.Join(f.HomeDir, "Library", "LaunchAgents", "hivemill.daemon.plist")); err != nil {
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
	for _, l := range bootJobs {
		got, err := os.ReadFile(filepath.Join(f.HomeDir, "Library", "LaunchAgents", l+".plist"))
		if err != nil {
			t.Fatal(err)
		}
		// the expected plist is the repo's template with this host's values filled in
		tmpl, err := os.ReadFile(filepath.Join("../../../launchd", l+".plist.in"))
		if err != nil {
			t.Fatal(err)
		}
		fh := filepath.Join(f.HomeDir, "factory")
		want := strings.NewReplacer("{{.FactoryHome}}", fh, "{{.LogDir}}", fh+"/logs",
			"{{.Path}}", f.HomeDir+"/.local/bin:/opt/homebrew/bin:/usr/bin:/bin").Replace(string(tmpl))
		if string(got) != want {
			t.Errorf("%s plist differs from its template\n%s", l, got)
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
