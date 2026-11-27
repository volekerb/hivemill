package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainLayout = `{
  "id": "root",
  "type": "split",
  "ratio": 0.5,
  "big": 12345678901234567890,
  "children": [
    {"id": "sidebar", "type": "block", "content": {"mod": "sidebar"}},
    {"id": "work", "type": "split", "children": [
      {"id": "status", "type": "block", "content": {"mod": "workspace-status", "props": {"x": 1.50}}}
    ]},
    "caf\u00e9 \ud83d\ude00"
  ]
}`

func herdrFixture(t *testing.T, layout string) (*fakeHost, string) {
	t.Helper()
	f := newFake(t)
	app := filepath.Join(f.HomeDir, "app")
	f.Env["HERDR_APP_HOME"] = app
	plugin := filepath.Join(f.HomeDir, "plugin")
	f.Env["HIVEMILL_PLUGIN_ROOT"] = plugin
	src := filepath.Join(plugin, "herdr-app", "mods", "hivemill")
	mustMkdir(t, src)
	for _, n := range herdrAppFiles {
		mustWrite(t, filepath.Join(src, n), "// "+n)
	}
	mustMkdir(t, filepath.Join(app, "layouts"))
	if layout != "" {
		mustWrite(t, filepath.Join(app, "layouts", "main.json"), layout)
	}
	return f, app
}

func TestHerdrAppInstallsModConfigAndLayout(t *testing.T) {
	f, app := herdrFixture(t, mainLayout)
	rc, out, _ := runMain(t, f, "--yes", "--herdr-app", "--factory", "Workshop=http://192.0.2.10:7461/", "--factory", "http://192.0.2.11:7461")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	cfgPath := filepath.Join(f.HomeDir, ".config", "hivemill", "dashboard.json")
	for _, want := range []string{
		"[run] 1 mod: copy main.js, data.js, mod.json into " + app + "/mods/hivemill",
		"[run] 2 config: write " + cfgPath + " (2 factories: http://192.0.2.10:7461, http://192.0.2.11:7461; 0 project(s))",
		"[run] 3 layout: write " + app + "/layouts/hivemill.json from main.json, the status bar becomes the Hivemill bar",
		"[run] 4 switch: show the hivemill layout if the app is running",
		"    the app is not running or herdr-app is not on PATH; pick the Hivemill layout with ctrl-shift-l",
		"RESULT PASS herdr-app layout=hivemill config=" + cfgPath,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(app, "mods", "hivemill", "main.js")); err != nil || string(b) != "// main.js" {
		t.Errorf("mod file: %q %v", b, err)
	}
	// json.dumps(..., indent=2) of the Python reference: key order kept, ensure_ascii, floats as repr, big ints exact
	wantLayout := `{
  "id": "root",
  "type": "split",
  "ratio": 0.5,
  "big": 12345678901234567890,
  "children": [
    {
      "id": "sidebar",
      "type": "block",
      "content": {
        "mod": "sidebar"
      }
    },
    {
      "id": "work",
      "type": "split",
      "children": [
        {
          "id": "status",
          "type": "block",
          "content": {
            "mod": "hivemill",
            "props": {
              "mode": "bar"
            }
          }
        }
      ]
    },
    "caf\u00e9 \ud83d\ude00"
  ]
}
`
	if b, _ := os.ReadFile(filepath.Join(app, "layouts", "hivemill.json")); string(b) != wantLayout {
		t.Errorf("layout:\n%s\nwant:\n%s", b, wantLayout)
	}
	wantCfg := `{
  "factories": [
    {
      "url": "http://192.0.2.10:7461",
      "label": "Workshop"
    },
    {
      "url": "http://192.0.2.11:7461"
    }
  ],
  "projects": []
}
`
	if b, _ := os.ReadFile(cfgPath); string(b) != wantCfg {
		t.Errorf("config:\n%s\nwant:\n%s", b, wantCfg)
	}
	if got := mustReadFile(t, filepath.Join(app, "layouts", "main.json")); got != mainLayout {
		t.Error("main.json must not change")
	}
}

func TestHerdrAppDryRunChangesNothing(t *testing.T) {
	f, app := herdrFixture(t, mainLayout)
	rc, out, _ := runMain(t, f, "--dry-run", "--herdr-app", "--no-switch", "--no-sidebar")
	if rc != 0 || !strings.Contains(out, "[plan] 4 switch: skipped (--no-switch)") || !strings.Contains(out, "the status bar becomes the Hivemill bar, the sidebar hidden") || !strings.HasSuffix(out, "plan only: nothing was changed\n") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(app, "mods")); err == nil {
		t.Error("dry run created the mod dir")
	}
	if _, err := os.Stat(filepath.Join(app, "layouts", "hivemill.json")); err == nil {
		t.Error("dry run wrote the layout")
	}
	if len(f.muts) != 0 {
		t.Errorf("mutations: %v", f.muts)
	}
}

func TestHerdrAppAddsBarWhenThereIsNoStatusBlock(t *testing.T) {
	f, app := herdrFixture(t, `{"id":"root","children":[{"id":"sidebar"},{"id":"work","children":[]}]}`)
	rc, out, _ := runMain(t, f, "--yes", "--herdr-app", "--no-switch", "--no-sidebar")
	if rc != 0 || !strings.Contains(out, "with the Hivemill bar added under the work area, the sidebar hidden") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	want := `{
  "id": "root",
  "children": [
    {
      "id": "sidebar",
      "hidden": true
    },
    {
      "id": "work",
      "children": [
        {
          "id": "hivemill-bar",
          "type": "block",
          "size": {
            "px": 28
          },
          "content": {
            "mod": "hivemill",
            "props": {
              "mode": "bar"
            }
          }
        }
      ]
    }
  ]
}
`
	if got := mustReadFile(t, filepath.Join(app, "layouts", "hivemill.json")); got != want {
		t.Errorf("layout:\n%s", got)
	}
}

func TestHerdrAppKeepsExistingDashboardConfig(t *testing.T) {
	f, _ := herdrFixture(t, mainLayout)
	cfgDir := filepath.Join(f.HomeDir, ".config", "hivemill")
	mustMkdir(t, cfgDir)
	mustWrite(t, filepath.Join(cfgDir, "dashboard.json"),
		`{"projects":[{"id":"a"}],"factory":{"web_url":"http://w","machine":"old"},"orgs":"auto","zzz":1.0e2,`+
			`"factories":[{"space":"box/hivemill","url":"http://127.0.0.1:9/","label":"Old"},{"url":"http://127.0.0.1:8"},{"url":""},7]}`)
	rc, out, _ := runMain(t, f, "--yes", "--herdr-app", "--no-switch", "--factory", "New=http://127.0.0.1:9")
	if rc != 0 || !strings.Contains(out, "2 config: update ") || !strings.Contains(out, "(1 factory: http://127.0.0.1:9; 1 project(s))") ||
		!strings.Contains(out, "    dropped the old dashboard's settings (factory, orgs): the dashboard now reads each factory's `hivemill obs`") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	want := `{
  "projects": [
    {
      "id": "a"
    }
  ],
  "zzz": 100.0,
  "factories": [
    {
      "space": "box/hivemill",
      "url": "http://127.0.0.1:9",
      "label": "New"
    }
  ]
}
`
	if got := mustReadFile(t, filepath.Join(cfgDir, "dashboard.json")); got != want {
		t.Errorf("config:\n%s\nwant:\n%s", got, want)
	}
}

func TestHerdrAppRefusals(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		f, _ := herdrFixture(t, mainLayout)
		f.euid = 0
		if rc, out, _ := runMain(t, f, "--yes", "--herdr-app"); rc != 1 || !strings.Contains(out, "refusing to run as root") {
			t.Errorf("rc=%d %s", rc, out)
		}
	})
	t.Run("app not set up", func(t *testing.T) {
		f, _ := herdrFixture(t, "")
		if rc, out, _ := runMain(t, f, "--yes", "--herdr-app"); rc != 3 || !strings.Contains(out, "the Herdr app is not set up here") {
			t.Errorf("rc=%d %s", rc, out)
		}
	})
	t.Run("dashboard files missing", func(t *testing.T) {
		f, _ := herdrFixture(t, mainLayout)
		f.Env["HIVEMILL_PLUGIN_ROOT"] = filepath.Join(f.HomeDir, "nowhere")
		if rc, out, _ := runMain(t, f, "--yes", "--herdr-app"); rc != 1 || !strings.Contains(out, "the dashboard files are missing") {
			t.Errorf("rc=%d %s", rc, out)
		}
	})
	t.Run("main.json is not JSON", func(t *testing.T) {
		f, app := herdrFixture(t, "{nope")
		rc, out, _ := runMain(t, f, "--yes", "--herdr-app")
		if rc != 1 || !strings.Contains(out, "RESULT FAIL step=herdr-app-preflight") {
			t.Errorf("rc=%d %s", rc, out)
		}
		if _, err := os.Stat(filepath.Join(app, "mods")); err == nil {
			t.Error("a broken main.json must stop before anything is written")
		}
	})
	t.Run("main.json is a list", func(t *testing.T) {
		f, _ := herdrFixture(t, "[1]")
		if rc, out, _ := runMain(t, f, "--yes", "--herdr-app"); rc != 1 || !strings.Contains(out, "RESULT FAIL") {
			t.Errorf("rc=%d %s", rc, out)
		}
	})
}

func TestOrderedJSONRoundTrip(t *testing.T) {
	in := `{"b":1,"a":[true,false,null,"x\n\t\u0001\u007f\u20ac"],"b":2,"n":-0,"f":1e22,"g":1.5e-7,"h":0.0001,"i":-0.0,"e":{},"l":[]}`
	v, err := decodeOrdered([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeOrdered(v)
	if err != nil {
		t.Fatal(err)
	}
	// Python: json.dumps(json.loads(in), indent=2); a duplicate key keeps its first position and the last value
	want := `{
  "b": 2,
  "a": [
    true,
    false,
    null,
    "x\n\t\u0001\u007f\u20ac"
  ],
  "n": 0,
  "f": 1e+22,
  "g": 1.5e-07,
  "h": 0.0001,
  "i": -0.0,
  "e": {},
  "l": []
}`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"", "{", "[1,]", `{"a":1} x`, `{1:2}`, strings.Repeat("[", 600)} {
		if _, err := decodeOrdered([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
