package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesObsUnit(t *testing.T) {
	f := unitsFake(t)
	var out strings.Builder
	if _, err := stepServices(NewCtx(f, &Args{}, &out, false)); err != nil {
		t.Fatalf("services: %v\n%s", err, out.String())
	}
	unit, err := f.ReadFile(filepath.Join(f.HomeDir, ".config", "systemd", "user", "hivemill-obs.service"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := repoUnit(t, "hivemill.service")
	path := ""
	for _, ln := range strings.Split(daemon, "\n") {
		if strings.HasPrefix(ln, "Environment=PATH=") {
			path = ln
		}
	}
	for _, want := range []string{"After=hivemill.service\n", "Restart=always\n", "ExecStart=%h/.local/bin/hivemill obs\n",
		"WantedBy=default.target", path + "\n"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("hivemill-obs.service lacks %q:\n%s", want, unit)
		}
	}
	if !f.mutated("enable --now hivemill.service hivemill-obs.service ") {
		t.Errorf("obs is not enabled and started with the daemon: %v", f.muts)
	}
	if f.mutated("--obs-listen") {
		t.Errorf("obs_listen changed without an address: %v", f.muts)
	}
	dst := filepath.Join(f.HomeDir, ".config", "systemd", "user", "hivemill-obs.service")
	if err := f.WriteText(dst, "mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := stepServices(NewCtx(f, &Args{}, &out, false)); err != nil {
		t.Fatalf("second run: %v\n%s", err, out.String())
	}
	if got, err := f.ReadFile(dst); err != nil || string(got) != "mine" {
		t.Errorf("an existing obs unit was overwritten: %q %v", got, err)
	}
}

func TestObsListenOption(t *testing.T) {
	if _, err := ParseArgs([]string{"--yes", "--obs-listen", "somewhere"}); err == nil {
		t.Error("a host name was accepted")
	}
	a, err := ParseArgs([]string{"--yes", "--obs-listen", "192.0.2.7"})
	if err != nil || a.ObsListen != "192.0.2.7" {
		t.Fatalf("--obs-listen = %+v, %v", a, err)
	}
	run := func(t *testing.T, f *fakeHost, a *Args, plan bool) string {
		t.Helper()
		f.rules = func(argv []string) (int, string, bool) { // the built binary answers `hivemill init`
			return 0, "", filepath.Base(argv[0]) == "hivemill" && len(argv) > 1 && argv[1] == "init"
		}
		var out strings.Builder
		if _, err := stepServices(NewCtx(f, a, &out, plan)); err != nil {
			t.Fatalf("services: %v\n%s", err, out.String())
		}
		return out.String()
	}
	stored := func(f *fakeHost, addr string) bool {
		return f.mutated("hivemill init --home " + filepath.Join(f.HomeDir, "factory") + " --obs-listen " + addr)
	}
	t.Run("flag", func(t *testing.T) {
		f := unitsFake(t)
		run(t, f, a, false)
		if !stored(f, "192.0.2.7") {
			t.Errorf("calls %v", f.muts)
		}
	})
	t.Run("no systemd", func(t *testing.T) {
		f := unitsFake(t)
		run(t, f, &Args{Yes: true, NoSystemd: true, ObsListen: "192.0.2.7"}, false)
		if !stored(f, "192.0.2.7 --no-service") {
			t.Errorf("calls %v", f.muts)
		}
	})
	t.Run("older version", func(t *testing.T) {
		f := unitsFake(t)
		if err := os.Remove(filepath.Join(f.HomeDir, "factory", "systemd", "hivemill-obs.service")); err != nil {
			t.Fatal(err)
		}
		run(t, f, a, false)
		if !f.mutated("enable --now hivemill.service hivemill-digest.timer") || f.mutated("--obs-listen") {
			t.Errorf("calls %v", f.muts)
		}
	})
	t.Run("asked", func(t *testing.T) {
		f := unitsFake(t)
		f.tty, f.enters, f.typed = true, 1, "192.0.2.8"
		if out := run(t, f, &Args{Yes: true}, false); !strings.Contains(out, "from your LAN") || !stored(f, "192.0.2.8") {
			t.Errorf("not asked or not stored: %v\n%s", f.muts, out)
		}
	})
	t.Run("not an address", func(t *testing.T) {
		f := unitsFake(t)
		f.tty, f.enters, f.typed = true, 1, "my laptop"
		if out := run(t, f, &Args{Yes: true}, false); !strings.Contains(out, "unchanged") || f.mutated("--obs-listen") {
			t.Errorf("%v\n%s", f.muts, out)
		}
	})
	t.Run("plan warns", func(t *testing.T) {
		f := unitsFake(t)
		if out := run(t, f, a, true); !strings.Contains(out, "WARNING: hivemill obs listens on 192.0.2.7") || len(f.muts) != 0 {
			t.Errorf("%v\n%s", f.muts, out)
		}
	})
}
