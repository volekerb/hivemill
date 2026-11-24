package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"factory/go/internal/install"
)

// Port of tests/test_herdr_app_dashboard.py (11 cases; see ../ported_cursor.txt).
// The Python test imports plugins/hivemill/scripts/install.py and calls main(); here the real installer entry point
// is install.Main (the Go port, internal/install) over a Host that records herdr-app calls instead of running them.
// It runs against a throwaway home with a minimal Herdr app folder and never touches the real ~/.config.

const mainLayout = `{
  "id": "root", "type": "row",
  "children": [
    {"id": "sidebar", "type": "column", "children": [{"id": "agents", "type": "block", "content": {"mod": "agents"}}]},
    {"id": "work", "type": "column", "children": [
      {"id": "main", "type": "block", "content": {"tab": "active"}},
      {"id": "status", "type": "block", "size": {"px": 28}, "content": {"mod": "workspace-status"}}
    ]}
  ]
}`

// dashHost is the real host with Run replaced: every external command is recorded and answers 0, "{}".
type dashHost struct {
	*install.OSHost
	calls [][]string
}

func (h *dashHost) Run(argv []string, cwd string, timeout time.Duration, mutate bool) (int, string) {
	h.calls = append(h.calls, argv)
	return 0, "{}"
}

type dash struct {
	t                      *testing.T
	home, app, cfg, layout string
	host                   *dashHost
}

func newDash(t *testing.T) *dash {
	t.Helper()
	home := realTemp(t)
	d := &dash{t: t, home: home, app: filepath.Join(home, ".config", "herdr-app")}
	mkdir(t, filepath.Join(d.app, "layouts"))
	d.writeMain(mainLayout)
	d.cfg = filepath.Join(home, ".config", "hivemill", "dashboard.json")
	d.layout = filepath.Join(d.app, "layouts", "hivemill.json")
	return d
}

func (d *dash) writeMain(text string) {
	d.t.Helper()
	writeFile(d.t, filepath.Join(d.app, "layouts", "main.json"), text, 0o644)
}

func (d *dash) run(argv ...string) (int, string) {
	d.t.Helper()
	d.host = &dashHost{OSHost: &install.OSHost{HomeDir: d.home, Env: map[string]string{
		"PATH": ":/bin:/usr/bin", "HIVEMILL_PLUGIN_ROOT": filepath.Join(repoRoot(d.t), "plugins", "hivemill")}}}
	var out, errw bytes.Buffer
	rc := install.Main(argv, d.host, &out, &errw)
	return rc, out.String() + errw.String()
}

func (d *dash) load(path string) map[string]any { d.t.Helper(); return readJSON(d.t, path) }

func jsonOf(t *testing.T, text string) map[string]any { t.Helper(); return parseJSON(t, text) }

func children(t *testing.T, m map[string]any) []any { t.Helper(); return list(t, m, "children") }

func TestHerdrAppDashboardDryRunChangesNothing(t *testing.T) {
	d := newDash(t)
	rc, out := d.run("--herdr-app", "--dry-run")
	eq(t, "rc", rc, 0)
	contains(t, "out", out, "plan only: nothing was changed")
	for _, p := range []string{filepath.Join(d.app, "mods"), d.cfg, d.layout} {
		if exists(p) {
			t.Errorf("%s was created by a dry run", p)
		}
	}
	eq(t, "calls", len(d.host.calls), 0)
}

func TestHerdrAppDashboardInstallCopiesModWritesConfigAndLayout(t *testing.T) {
	d := newDash(t)
	rc, out := d.run("--herdr-app", "--yes")
	if rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "RESULT PASS herdr-app layout=hivemill") {
		t.Fatalf("last line = %q\n%s", lines[len(lines)-1], out)
	}
	mod := filepath.Join(repoRoot(t), "plugins", "hivemill", "herdr-app", "mods", "hivemill")
	for _, f := range []string{"main.js", "data.js", "mod.json"} {
		eq(t, f, readFile(t, filepath.Join(d.app, "mods", "hivemill", f)), readFile(t, filepath.Join(mod, f)))
	}
	cfg := d.load(d.cfg)
	eq(t, "factory.url", sub(t, cfg, "factory")["url"], any("http://127.0.0.1:8765"))
	eq(t, "orgs", cfg["orgs"], any("auto"))
	if p, isList := cfg["projects"].([]any); !isList || len(p) != 0 {
		t.Fatalf("projects = %v", cfg["projects"])
	}
	lay := d.load(d.layout)
	status := children(t, children(t, lay)[1].(map[string]any))[1].(map[string]any)
	if !reflect.DeepEqual(status["content"], map[string]any{"mod": "hivemill", "props": map[string]any{"mode": "bar"}}) {
		t.Fatalf("status content = %v", status["content"])
	}
	// Nothing else in the layout changes, and main.json itself is left alone.
	status["content"] = map[string]any{"mod": "workspace-status"}
	main := jsonOf(t, mainLayout)
	if !reflect.DeepEqual(lay, main) {
		t.Fatalf("layout differs from main beyond the status bar:\n%v\n%v", lay, main)
	}
	if !reflect.DeepEqual(d.load(filepath.Join(d.app, "layouts", "main.json")), main) {
		t.Fatal("main.json was changed")
	}
	// adapted: Python's json.dumps writes `{"name": "hivemill"}`, the Go installer writes it compact; the argument is
	// JSON the app parses, so it is compared as JSON (follow-up: byte parity of the layout.switch argument).
	if len(d.host.calls) != 1 || len(d.host.calls[0]) != 4 || !reflect.DeepEqual(d.host.calls[0][:3], []string{"herdr-app", "do", "layout.switch"}) {
		t.Fatalf("calls = %v", d.host.calls)
	}
	if !reflect.DeepEqual(jsonOf(t, d.host.calls[0][3]), map[string]any{"name": "hivemill"}) {
		t.Fatalf("layout.switch argument = %s", d.host.calls[0][3])
	}
}

func TestHerdrAppDashboardRerunKeepsProjectsAndChangesOnlyGivenFlags(t *testing.T) {
	d := newDash(t)
	writeFile(t, d.cfg, `{"factory": {"url": "http://127.0.0.1:9000", "web_url": "http://box:9000"}, "orgs": "auto",
 "projects": [{"name": "App", "github": "o/app", "space": "app"}], "usage_file": "~/u.json"}`, 0o644)
	rc, out := d.run("--herdr-app", "--yes", "--no-switch", "--orgs", "claude", "--factory-machine", "box")
	if rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, out)
	}
	cfg := d.load(d.cfg)
	if !reflect.DeepEqual(cfg["orgs"], []any{"claude"}) {
		t.Fatalf("orgs = %v", cfg["orgs"])
	}
	if !reflect.DeepEqual(cfg["factory"], map[string]any{"url": "http://127.0.0.1:9000", "web_url": "http://box:9000", "machine": "box"}) {
		t.Fatalf("factory = %v", cfg["factory"])
	}
	if !reflect.DeepEqual(cfg["projects"], []any{map[string]any{"name": "App", "github": "o/app", "space": "app"}}) {
		t.Fatalf("projects = %v", cfg["projects"])
	}
	eq(t, "usage_file", cfg["usage_file"], any("~/u.json"))
	eq(t, "calls", len(d.host.calls), 0)
}

func TestHerdrAppDashboardEmptyMachineClearsIt(t *testing.T) {
	d := newDash(t)
	writeFile(t, d.cfg, `{"factory": {"url": "http://127.0.0.1:8765", "machine": "box"}}`, 0o644)
	rc, out := d.run("--herdr-app", "--yes", "--no-switch", "--factory-machine", "")
	if rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, out)
	}
	if m, present := sub(t, d.load(d.cfg), "factory")["machine"]; !present || m != nil {
		t.Fatalf("machine = %v (present %v), want null", m, present)
	}
}

func TestHerdrAppDashboardLayoutWithoutAStatusBarGetsOneUnderTheWorkArea(t *testing.T) {
	d := newDash(t)
	main := jsonOf(t, mainLayout)
	work := children(t, main)[1].(map[string]any)
	work["children"] = work["children"].([]any)[:1]
	b, err := json.Marshal(main)
	ok(t, err)
	d.writeMain(string(b))
	rc, out := d.run("--herdr-app", "--yes", "--no-switch")
	if rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, out)
	}
	kids := children(t, children(t, d.load(d.layout))[1].(map[string]any))
	last := kids[len(kids)-1].(map[string]any)
	if !reflect.DeepEqual(last["content"], map[string]any{"mod": "hivemill", "props": map[string]any{"mode": "bar"}}) {
		t.Fatalf("content = %v", last["content"])
	}
	if !reflect.DeepEqual(last["size"], map[string]any{"px": float64(28)}) {
		t.Fatalf("size = %v", last["size"])
	}
}

func TestHerdrAppDashboardNoSidebarHidesItAndSurvivesARerun(t *testing.T) {
	d := newDash(t)
	for i := 0; i < 2; i++ {
		rc, out := d.run("--herdr-app", "--yes", "--no-switch", "--no-sidebar")
		if rc != 0 {
			t.Fatalf("rc = %d\n%s", rc, out)
		}
		contains(t, "out", out, "the sidebar hidden")
		if h, _ := children(t, d.load(d.layout))[0].(map[string]any)["hidden"].(bool); !h {
			t.Fatalf("run %d: the sidebar is not hidden", i+1)
		}
	}
	d.run("--herdr-app", "--yes", "--no-switch")
	if _, has := children(t, d.load(d.layout))[0].(map[string]any)["hidden"]; has {
		t.Fatal("a rerun without --no-sidebar must show the sidebar again")
	}
}

func TestHerdrAppDashboardNoHerdrAppIsAHandOffNotAFailure(t *testing.T) {
	d := newDash(t)
	ok(t, os.Remove(filepath.Join(d.app, "layouts", "main.json")))
	rc, out := d.run("--herdr-app", "--yes")
	eq(t, "rc", rc, 3)
	contains(t, "out", out, "Install the Herdr app")
	if exists(filepath.Join(d.app, "mods")) || exists(d.cfg) {
		t.Fatal("a hand-off must change nothing")
	}
}

func TestHerdrAppDashboardBrokenConfigFailsTheConfigStepWithoutWriting(t *testing.T) {
	d := newDash(t)
	writeFile(t, d.cfg, "{not json", 0o644)
	rc, out := d.run("--herdr-app", "--yes")
	eq(t, "rc", rc, 1)
	contains(t, "out", out, "RESULT FAIL step=herdr-app-preflight")
	if exists(d.layout) {
		t.Fatal("the layout was written despite the broken config")
	}
}

func TestHerdrAppDashboardUsageErrors(t *testing.T) {
	d := newDash(t)
	for _, argv := range [][]string{
		{"--herdr-app", "--check"},
		{"--dry-run", "--orgs", "claude"},
		{"--yes", "--factory-url", "http://x"},
		{"--dry-run", "--no-sidebar"},
		{"--herdr-app", "--yes", "--orgs", "openai"},
		{"--herdr-app", "--yes", "--factory-url", "ftp://x"},
		{"--herdr-app", "--yes", "--factory-machine", "-oProxy=x"},
	} {
		if rc, out := d.run(argv...); rc != 2 {
			t.Errorf("%v: rc = %d, want 2\n%s", argv, rc, out)
		}
	}
	if exists(d.cfg) {
		t.Fatal("a usage error must not write the config")
	}
}

func TestHerdrAppDashboardShippedModFiles(t *testing.T) {
	root := repoRoot(t)
	app := filepath.Join(root, "plugins", "hivemill", "herdr-app")
	eq(t, "place", readJSON(t, filepath.Join(app, "mods", "hivemill", "mod.json"))["place"], any("none"))
	example := readJSON(t, filepath.Join(app, "dashboard.example.json"))
	var keys []string
	for k := range example {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	eq(t, "example keys", strings.Join(keys, ","),
		"alerts,context_pattern,demo,factory,machine_labels,machines,orgs,projects,teams,usage_file")
	src := readFile(t, filepath.Join(app, "mods", "hivemill", "data.js"))
	for _, k := range keys {
		if !strings.Contains(src, k+":") {
			t.Errorf("data.js DEFAULTS must know every example key: %s", k)
		}
	}
}

func TestHerdrAppDashboardNodeSuiteForTheModPasses(t *testing.T) {
	// The mod's data layer has its own node tests (plugins/hivemill/herdr-app/tests); run them here.
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH: the mod's tests need node 22 or newer")
	}
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "plugins", "hivemill", "herdr-app", "tests", "*.test.mjs"))
	ok(t, err)
	if len(files) == 0 {
		t.Fatal("no node test files found")
	}
	sort.Strings(files)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // the Python test's timeout=120
	defer cancel()
	cmd := exec.CommandContext(ctx, node, append([]string{"--test"}, files...)...)
	cmd.Dir = root
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		tail := so.String()
		if len(tail) > 3000 {
			tail = tail[len(tail)-3000:]
		}
		t.Fatalf("node --test: %v\n%s\n%s", err, tail, se.String())
	}
	contains(t, "node output", so.String(), "fail 0")
}
