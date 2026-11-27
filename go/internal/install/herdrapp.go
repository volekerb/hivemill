package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --herdr-app installs the Hivemill dashboard into the Herdr app on the machine you work from (macOS or Linux; it
// may not be the factory's box). It touches only ~/.config/herdr-app/mods/hivemill, ~/.config/herdr-app/layouts/
// hivemill.json and ~/.config/hivemill/dashboard.json. See docs/herdr-app-dashboard.md.
const (
	herdrAppMod    = "hivemill"
	herdrAppLayout = "hivemill"
)

var herdrAppFiles = []string{"main.js", "data.js", "mod.json"}

func barBlock() *OMap {
	props := NewOMap()
	props.Set("mode", "bar")
	m := NewOMap()
	m.Set("mod", herdrAppMod)
	m.Set("props", props)
	return m
}

func herdrAppHome(h Host) string {
	if v := h.Environ()["HERDR_APP_HOME"]; v != "" {
		return v
	}
	return filepath.Join(h.Home(), ".config", "herdr-app")
}

// pluginRoot is the plugin directory that holds herdr-app/. The verifier shim exports HIVEMILL_PLUGIN_ROOT; without
// it the binary is assumed to sit in <root>/scripts.
func pluginRoot(h Host) string {
	env := h.Environ()
	for _, k := range []string{"HIVEMILL_PLUGIN_ROOT", "CLAUDE_PLUGIN_ROOT"} {
		if v := env[k]; v != "" {
			return v
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(exe))
}

func herdrAppSource(h Host) string {
	return filepath.Join(pluginRoot(h), "herdr-app", "mods", herdrAppMod)
}

// barLayout is the app's main layout with its status bar block showing the Hivemill bar. The block is the node whose
// content is the shipped `workspace-status` mod (id "status" in Herdr's main).
func barLayout(main any, hideSidebar bool) (*OMap, bool, error) {
	lay, ok := copyValue(main).(*OMap)
	if !ok {
		return nil, false, errors.New("the Herdr app main layout is not a JSON object")
	}
	found := false
	var walk func(n any)
	walk = func(n any) {
		node, ok := n.(*OMap)
		if !ok {
			return
		}
		if content, ok := node.Vals["content"].(*OMap); ok {
			if mod, _ := content.Vals["mod"].(string); mod == "workspace-status" || mod == herdrAppMod {
				node.Set("content", barBlock())
				found = true
			}
		}
		if ch, ok := node.Vals["children"].([]any); ok {
			for _, c := range ch {
				walk(c)
			}
		}
	}
	walk(lay)
	children, _ := lay.Vals["children"].([]any)
	for _, ch := range children {
		if m, ok := ch.(*OMap); ok && hideSidebar {
			if id, _ := m.Vals["id"].(string); id == "sidebar" {
				m.Set("hidden", true) // a picture station: no space or agent names in shots
			}
		}
	}
	if !found {
		work := lay
		for _, ch := range children {
			if m, ok := ch.(*OMap); ok {
				if id, _ := m.Vals["id"].(string); id == "work" {
					work = m
					break
				}
			}
		}
		size := NewOMap()
		size.Set("px", Num("28"))
		block := NewOMap()
		block.Set("id", "hivemill-bar")
		block.Set("type", "block")
		block.Set("size", size)
		block.Set("content", barBlock())
		cur, present := work.Vals["children"]
		switch list := cur.(type) {
		case nil:
			if present {
				return nil, false, errors.New("the layout's children is null, expected a list")
			}
			work.Set("children", []any{block})
		case []any:
			work.Set("children", append(list, block))
		default:
			return nil, false, errors.New("the layout's children is not a list")
		}
	}
	return lay, found, nil
}

// objectOrEmpty mirrors dict(x or {}) for a decoded JSON value.
func objectOrEmpty(v any, what string) (*OMap, error) {
	if !truthy(v) {
		return NewOMap(), nil
	}
	m, ok := v.(*OMap)
	if !ok {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	return copyValue(m).(*OMap), nil
}

// ObsPort is the port of `hivemill obs` (config obs_port).
const ObsPort = 7461

// herdrAppOldKeys are the settings of the dashboard before it read the obs API.
var herdrAppOldKeys = []string{"factory", "teams", "orgs"}

// dashboardConfig is ~/.config/hivemill/dashboard.json: the existing one with only the flags given changed;
// projects are kept. --factory replaces the list of factories (an entry whose URL stays keeps its other fields).
// The old dashboard's keys (factory, teams, orgs) named a server that is gone: they are dropped and returned.
func dashboardConfig(existing any, a *Args) (*OMap, []string, error) {
	cfg, err := objectOrEmpty(existing, "dashboard.json")
	if err != nil {
		return nil, nil, err
	}
	var dropped []string
	for _, k := range herdrAppOldKeys {
		if _, ok := cfg.Get(k); ok {
			cfg.Del(k)
			dropped = append(dropped, k)
		}
	}
	var old []any
	if cur, _ := cfg.Get("factories"); cur != nil {
		list, _ := cur.([]any)
		for _, e := range list {
			if m, ok := e.(*OMap); ok {
				if u, _ := m.Vals["url"].(string); u != "" {
					old = append(old, m)
				}
			}
		}
	}
	if len(a.Factories) > 0 {
		byURL := map[string]*OMap{}
		for _, e := range old {
			m := e.(*OMap)
			byURL[strings.TrimRight(m.Vals["url"].(string), "/")] = m
		}
		old = nil
		for _, spec := range a.Factories {
			label, url, ok := parseFactory(spec)
			if !ok {
				return nil, nil, errors.New("--factory takes URL or LABEL=URL, with an http(s) URL")
			}
			f := NewOMap()
			if prev := byURL[url]; prev != nil {
				f = copyValue(prev).(*OMap)
			}
			f.Set("url", url)
			if label != "" {
				f.Set("label", label)
			}
			old = append(old, f)
		}
	}
	if len(old) == 0 {
		f := NewOMap()
		f.Set("url", fmt.Sprintf("http://%s:%d", Loopback, ObsPort))
		old = []any{f}
	}
	cfg.Set("factories", old)
	if _, ok := cfg.Get("projects"); !ok {
		cfg.Set("projects", []any{})
	}
	return cfg, dropped, nil
}

// projectsLen reads the projects field for the plan line.
func projectsLen(v any) (int, error) {
	switch t := v.(type) {
	case []any:
		return len(t), nil
	case *OMap:
		return len(t.Keys), nil
	case string:
		return len([]rune(t)), nil
	}
	return 0, errors.New("dashboard.json projects has no length")
}

func runHerdrApp(host Host, a *Args, out io.Writer) int {
	c := NewCtx(host, a, out, a.DryRun)
	app := herdrAppHome(host)
	src := herdrAppSource(host)
	modDir := filepath.Join(app, "mods", herdrAppMod)
	layoutPath := filepath.Join(app, "layouts", herdrAppLayout+".json")
	mainPath := filepath.Join(app, "layouts", "main.json")
	cfgPath := filepath.Join(host.Home(), ".config", "hivemill", "dashboard.json")
	mode := "run"
	if c.plan {
		mode = "plan"
	}
	name := "preflight"
	err := func() error {
		if host.Euid() == 0 {
			return stopf(1, "refusing to run as root: run it as the user who uses the Herdr app")
		}
		for _, f := range herdrAppFiles {
			if !host.Exists(filepath.Join(src, f)) {
				return stopf(1, "the dashboard files are missing next to the installer (%s); use a full hivemill checkout or plugin", src)
			}
		}
		if !host.Exists(mainPath) {
			return stopf(3, "the Herdr app is not set up here (no %s). Install the Herdr app, open it once, then re-run this command", mainPath)
		}
		raw, err := host.ReadFile(mainPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", mainPath, err)
		}
		main, err := decodeOrdered(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", mainPath, err)
		}
		var existing any
		haveExisting := false
		if host.Exists(cfgPath) {
			raw, err := host.ReadFile(cfgPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", cfgPath, err)
			}
			if existing, err = decodeOrdered(raw); err != nil {
				return fmt.Errorf("%s: %w", cfgPath, err)
			}
			haveExisting = existing != nil
		}
		cfg, dropped, err := dashboardConfig(existing, a)
		if err != nil {
			return err
		}
		lay, replaced, err := barLayout(main, a.NoSidebar)
		if err != nil {
			return err
		}
		facs, _ := cfg.Vals["factories"].([]any)
		urls := make([]string, len(facs))
		for i, f := range facs {
			urls[i], _ = f.(*OMap).Vals["url"].(string)
		}
		nproj, err := projectsLen(cfg.Vals["projects"])
		if err != nil {
			return err
		}

		name = "mod"
		c.emit(fmt.Sprintf("[%s] 1 mod: copy %s into %s", mode, strings.Join(herdrAppFiles, ", "), modDir))
		if !c.plan {
			if err := host.Mkdirs(modDir); err != nil {
				return fmt.Errorf("create %s: %w", modDir, err)
			}
			for _, f := range herdrAppFiles {
				if err := host.InstallFile(filepath.Join(src, f), filepath.Join(modDir, f), 0o644); err != nil {
					return err
				}
			}
		}
		name = "config"
		verb := "write"
		if haveExisting {
			verb = "update"
		}
		plural := "ies"
		if len(facs) == 1 {
			plural = "y"
		}
		c.emit(fmt.Sprintf("[%s] 2 config: %s %s (%d factor%s: %s; %d project(s))", mode, verb, cfgPath, len(facs), plural, strings.Join(urls, ", "), nproj))
		if len(dropped) > 0 {
			c.note(fmt.Sprintf("dropped the old dashboard's settings (%s): the dashboard now reads each factory's `hivemill obs`", strings.Join(dropped, ", ")))
		}
		if !c.plan {
			if err := host.Mkdirs(filepath.Dir(cfgPath)); err != nil {
				return fmt.Errorf("create %s: %w", filepath.Dir(cfgPath), err)
			}
			text, err := encodeOrdered(cfg)
			if err != nil {
				return fmt.Errorf("encode the dashboard config: %w", err)
			}
			if err := host.WriteText(cfgPath, text+"\n"); err != nil {
				return err
			}
		}
		name = "layout"
		how := "with the Hivemill bar added under the work area"
		if replaced {
			how = "the status bar becomes the Hivemill bar"
		}
		if a.NoSidebar {
			how += ", the sidebar hidden"
		}
		c.emit(fmt.Sprintf("[%s] 3 layout: write %s from main.json, %s", mode, layoutPath, how))
		if !c.plan {
			text, err := encodeOrdered(lay)
			if err != nil {
				return fmt.Errorf("encode the layout: %w", err)
			}
			if err := host.WriteText(layoutPath, text+"\n"); err != nil {
				return err
			}
		}
		name = "switch"
		if a.NoSwitch {
			c.emit(fmt.Sprintf("[%s] 4 switch: skipped (--no-switch); pick the Hivemill layout with ctrl-shift-l", mode))
			return nil
		}
		c.emit(fmt.Sprintf("[%s] 4 switch: show the %s layout if the app is running", mode, herdrAppLayout))
		if !c.plan {
			arg, err := json.Marshal(map[string]string{"name": herdrAppLayout})
			if err != nil {
				return fmt.Errorf("encode the layout name: %w", err)
			}
			if rc, _ := host.Run([]string{"herdr-app", "do", "layout.switch", string(arg)}, "", 20*time.Second, true); rc != 0 {
				c.note("the app is not running or herdr-app is not on PATH; pick the Hivemill layout with ctrl-shift-l")
			}
		}
		return nil
	}()
	if err != nil {
		return c.finish(name, "herdr-app-", err)
	}
	if c.plan {
		c.emit("plan only: nothing was changed")
		return 0
	}
	c.emit(fmt.Sprintf("RESULT PASS herdr-app layout=%s config=%s", herdrAppLayout, cfgPath))
	return 0
}
