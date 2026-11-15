package launchd

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func tmpl() *Templates { return NewTemplates(os.DirFS("../../../launchd")) }

func vars() Vars {
	return Vars{FactoryHome: "/Users/a/factory", Path: "/Users/a/bin:/usr/bin:/bin", HerdrBin: "/Users/a/bin/herdr", FactoryBin: "/Users/a/bin/factory"}
}

// plistKeys parses a plist and returns top-level key -> raw string values following it (strings or array items).
func plistDoc(t *testing.T, b []byte) (keys []string, strs []string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = true
	var cur string
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("invalid XML: %v\n%s", err, b)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			depth++
			cur = elemName(e.Name)
		case xml.EndElement:
			depth--
			cur = ""
		case xml.CharData:
			s := strings.TrimSpace(string(e))
			if s == "" {
				continue
			}
			switch cur {
			case "key":
				keys = append(keys, s)
			case "string":
				strs = append(strs, s)
			}
		}
	}
}

// elemName is the element name without namespace (printed form of xml.Name is "{space name}").
func elemName(n xml.Name) string { return strings.TrimSpace(strings.Trim(fmt.Sprint(n), "{}")) }

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestRenderKeys(t *testing.T) {
	want := map[string][]string{
		"hivemill.herdr-server": {"RunAtLoad", "KeepAlive"},
		"hivemill.up":           {"RunAtLoad"},
		"hivemill.tick":         {"StartInterval"},
		"hivemill.serve":        {"RunAtLoad", "KeepAlive"},
	}
	for _, l := range Labels {
		b, err := tmpl().Render(l, vars())
		if err != nil {
			t.Fatal(err)
		}
		keys, strs := plistDoc(t, b)
		for _, k := range append(want[l], "Label", "ProgramArguments", "EnvironmentVariables", "FACTORY_HOME", "PATH", "StandardOutPath", "StandardErrorPath") {
			if !has(keys, k) {
				t.Errorf("%s: missing key %s in %v", l, k, keys)
			}
		}
		if !has(strs, l) || !has(strs, "/Users/a/factory/logs/"+l+".log") {
			t.Errorf("%s: label or log path missing in %v", l, strs)
		}
	}
}

func TestRenderArgv(t *testing.T) {
	get := func(l string, v Vars) []string {
		b, err := tmpl().Render(l, v)
		if err != nil {
			t.Fatal(err)
		}
		_, s := plistDoc(t, b)
		return s
	}
	if s := get("hivemill.tick", vars()); !has(s, "--deadline") || !has(s, "240s") {
		t.Errorf("tick argv lacks --deadline 240s: %v", s)
	}
	if s := get("hivemill.up", vars()); !has(s, "--wait-herdr") || !has(s, "60") {
		t.Errorf("up argv lacks --wait-herdr 60: %v", s)
	}
	if s := get("hivemill.herdr-server", vars()); has(s, "/usr/bin/caffeinate") {
		t.Errorf("caffeinate on by default: %v", s)
	}
	v := vars()
	v.Caffeinate = true
	s := get("hivemill.herdr-server", v)
	i := indexOf(s, "/usr/bin/caffeinate")
	if i < 0 || s[i+1] != "-s" || s[i+2] != v.HerdrBin {
		t.Errorf("caffeinate prefix wrong: %v", s)
	}
}

func indexOf(l []string, s string) int {
	for i, x := range l {
		if x == s {
			return i
		}
	}
	return -1
}

func TestRenderEscapes(t *testing.T) {
	v := vars()
	v.FactoryHome = "/Users/a&b/<x>"
	v.Path = "/p&q:/<bin>"
	b, err := tmpl().Render("hivemill.tick", v)
	if err != nil {
		t.Fatal(err)
	}
	keys, strs := plistDoc(t, b)
	if !has(strs, "/Users/a&b/<x>") || !has(strs, "/p&q:/<bin>") || !has(strs, "/Users/a&b/<x>/logs/hivemill.tick.log") {
		t.Errorf("values did not round-trip: %v", strs)
	}
	if n := len(keys); n != 8 {
		t.Errorf("injected keys? got %d keys %v", n, keys)
	}
	// a value that tries to inject a key
	v.Path = "</string><key>Evil</key><string>x"
	b, _ = tmpl().Render("hivemill.tick", v)
	if k, _ := plistDoc(t, b); has(k, "Evil") {
		t.Error("key injection through Path")
	}
}

func TestRenderRejects(t *testing.T) {
	for name, mod := range map[string]func(*Vars){
		"relative home": func(v *Vars) { v.FactoryHome = "factory" },
		"tilde bin":     func(v *Vars) { v.FactoryBin = "~/bin/factory" },
		"empty path":    func(v *Vars) { v.Path = "" },
		"control char":  func(v *Vars) { v.Path = "/a\x01b" },
	} {
		v := vars()
		mod(&v)
		if _, err := tmpl().Render("hivemill.up", v); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := tmpl().Render("hivemill.nope", vars()); err == nil {
		t.Error("unknown label accepted")
	}
}

func TestRenderAllAbsolute(t *testing.T) {
	m, err := tmpl().RenderAll(vars())
	if err != nil || len(m) != 4 {
		t.Fatalf("RenderAll: %v %d", err, len(m))
	}
	for l, b := range m {
		if strings.Contains(string(b), "%h") || strings.Contains(string(b), "~") {
			t.Errorf("%s contains %%h or ~", l)
		}
	}
}

func TestInstall(t *testing.T) {
	m, _ := tmpl().RenderAll(vars())
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o077) // umask must not make the files stricter than 0644
	defer syscall.Umask(old)
	if err := Install(dir, m); err != nil {
		t.Fatal(err)
	}
	for _, l := range Labels {
		st, err := os.Stat(filepath.Join(dir, l+".plist"))
		if err != nil || st.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v %v", l, err, st)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 4 {
		t.Errorf("temp files left behind: %d entries", len(ents))
	}
}

func TestInstallRefusesWritableDir(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o775, 0o757} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		err := Install(dir, map[string][]byte{"hivemill.up": []byte("x")})
		if err == nil || !strings.Contains(err.Error(), "writable") {
			t.Errorf("mode %o: err=%v", mode, err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Errorf("mode %o: wrote files", mode)
		}
	}
	if err := Install(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("missing dir accepted")
	}
}

func TestRunnerArgv(t *testing.T) {
	f := &FakeRunner{}
	_ = f.Bootstrap(501, "/p/x.plist")
	_ = f.Bootout(501, "hivemill.tick")
	_ = f.Kickstart(501, "hivemill.tick")
	want := [][]string{
		{"launchctl", "bootstrap", "gui/501", "/p/x.plist"},
		{"launchctl", "bootout", "gui/501/hivemill.tick"},
		{"launchctl", "kickstart", "-k", "gui/501/hivemill.tick"},
	}
	if !reflect.DeepEqual(f.Calls, want) {
		t.Errorf("calls %v", f.Calls)
	}
}

func TestRunnerExecNotLoaded(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "lc")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> "+dir+"/argv\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := ExecRunner{Launchctl: script}
	if err := r.Bootout(501, "hivemill.tick"); !errors.Is(err, ErrNotLoaded) {
		t.Errorf("bootout exit 3: %v", err)
	}
	if err := r.Bootstrap(501, "/p"); err == nil || errors.Is(err, ErrNotLoaded) {
		t.Errorf("bootstrap exit 3 must be a real error: %v", err)
	}
	b, _ := os.ReadFile(dir + "/argv")
	if string(b) != "bootout gui/501/hivemill.tick\nbootstrap gui/501 /p\n" {
		t.Errorf("argv %q", b)
	}
}

func TestUninstallToleratesNotLoaded(t *testing.T) {
	m, _ := tmpl().RenderAll(vars())
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	if err := Install(dir, m); err != nil {
		t.Fatal(err)
	}
	f := &FakeRunner{Err: map[string]error{"launchctl bootout gui/501/hivemill.tick": ErrNotLoaded}}
	res, err := Uninstall(dir, 501, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 4 || len(res) != 4 {
		t.Fatalf("calls %v", f.Calls)
	}
	for _, r := range res {
		if r.NotLoaded != (r.Label == "hivemill.tick") || !r.Removed {
			t.Errorf("result %+v", r)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("plists left: %v", ents)
	}
	// second run: nothing installed, nothing loaded
	if _, err := Uninstall(dir, 501, &FakeRunner{}); err != nil {
		t.Errorf("rerun: %v", err)
	}
}

func TestUninstallReportsRealFailure(t *testing.T) {
	dir := t.TempDir()
	f := &FakeRunner{Err: map[string]error{"launchctl bootout gui/501/hivemill.up": errors.New("boom")}}
	res, err := Uninstall(dir, 501, f)
	if err == nil || !strings.Contains(err.Error(), "boom") || len(res) != 4 {
		t.Errorf("err=%v res=%v", err, res)
	}
}

func TestDeadline(t *testing.T) {
	if Deadline(TickDeadline) != "240s" {
		t.Error(Deadline(TickDeadline))
	}
}
