package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObsServiceNextToTheDaemon(t *testing.T) {
	setup := func(t *testing.T, goos string) (Env, ServiceHost, *[]string) {
		t.Helper()
		user := t.TempDir()
		calls := &[]string{}
		return Env{Home: filepath.Join(user, "factory"), UserHome: user},
			ServiceHost{GOOS: goos, UserHome: user, ConfigDir: filepath.Join(user, ".config"), UID: 501,
				Run: func(argv ...string) (int, string) { *calls = append(*calls, strings.Join(argv, " ")); return 0, "" }}, calls
	}
	write := func(t *testing.T, path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	t.Run("no daemon", func(t *testing.T) {
		for _, goos := range []string{"linux", "darwin"} {
			e, h, calls := setup(t, goos)
			if n := InstallObsService(e, h, true); len(*calls) != 0 || !strings.Contains(strings.Join(n, "\n"), "no obs service") {
				t.Errorf("%s: %v %v", goos, n, *calls)
			}
		}
	})
	t.Run("linux", func(t *testing.T) {
		e, h, calls := setup(t, "linux")
		dir := filepath.Join(h.ConfigDir, "systemd", "user")
		write(t, filepath.Join(dir, "hivemill.service"), "[Service]\nEnvironment=PATH=/opt/zz/bin ZZ_TOKEN=zz\n")
		write(t, filepath.Join(dir, "hivemill.service.d", "home.conf"), "[Service]\nEnvironment=\"FACTORY_HOME=%h/other\"\n")
		if InstallObsService(e, h, false); len(*calls) != 0 {
			t.Fatalf("a daemon of another home: %v", *calls)
		}
		e.Home = filepath.Join(h.UserHome, "other")
		InstallObsService(e, h, true)
		unit := filepath.Join(dir, "hivemill-obs.service")
		want := "Environment=\"PATH=/opt/zz/bin\"\nEnvironment=\"FACTORY_HOME=%h/other\"\nExecStart=%h/.local/bin/hivemill obs\n"
		if got := read(t, unit); !strings.Contains(got, want) || strings.Count(got, "Environment=") != 2 || strings.Contains(got, "ZZ_TOKEN") {
			t.Errorf("unit:\n%s", got)
		}
		wantCalls := "systemctl --user daemon-reload\nsystemctl --user enable --now hivemill-obs.service\n" +
			"systemctl --user restart hivemill-obs.service"
		if got := strings.Join(*calls, "\n"); got != wantCalls {
			t.Errorf("calls:\n%s", got)
		}
		write(t, unit, "mine")
		*calls = nil
		InstallObsService(e, h, false)
		if read(t, unit) != "mine" || len(*calls) != 0 {
			t.Errorf("an existing unit was overwritten or started: %v", *calls)
		}
	})
	t.Run("darwin", func(t *testing.T) {
		e, h, calls := setup(t, "darwin")
		agents := filepath.Join(h.UserHome, "Library", "LaunchAgents")
		daemon, err := RenderPlist(e, "daemon", PlistInput{FactoryHome: e.Home, Path: "/opt/zz/bin:/usr/bin", LogDir: e.Home + "/logs"})
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(agents, "hivemill.daemon.plist"), daemon)
		InstallObsService(e, h, false)
		plist := filepath.Join(agents, "hivemill.obs.plist")
		if got := plistStrings(read(t, plist)); got["FACTORY_HOME"] != e.Home || got["PATH"] != "/opt/zz/bin:/usr/bin" || got["Label"] != "hivemill.obs" {
			t.Errorf("plist: %v", got)
		}
		if len(*calls) != 1 || (*calls)[0] != "launchctl bootstrap gui/501 "+plist {
			t.Errorf("calls: %v", *calls)
		}
		// a loaded job refuses bootstrap: a new setting needs a restart
		*calls = nil
		run := h.Run
		h.Run = func(argv ...string) (int, string) {
			rc, out := run(argv...)
			if argv[1] == "bootstrap" {
				return 5, "already loaded"
			}
			return rc, out
		}
		InstallObsService(e, h, true)
		if len(*calls) != 2 || (*calls)[1] != "launchctl kickstart -k gui/501/hivemill.obs" {
			t.Errorf("calls: %v", *calls)
		}
	})
}
