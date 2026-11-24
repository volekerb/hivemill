package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	modDir  = "plugins/hivemill/herdr-app/mods/hivemill"
	modTest = "plugins/hivemill/herdr-app/tests/dashboard.test.mjs"
)

func (r *runState) modSteps() {
	var node string
	if !r.step("node", func() (string, error) {
		var err error
		node, err = r.lookPath("node")
		return "node=" + node, err
	}, false) {
		return
	}
	env := r.childEnv()
	if !r.step("node-check", func() (string, error) {
		files, err := filepath.Glob(filepath.Join(r.tree, modDir, "*.js"))
		if err != nil || len(files) == 0 {
			return "", fmt.Errorf("no .js files in %s", modDir)
		}
		sort.Strings(files)
		for i, f := range files {
			code, tail, rerr := r.execLog(fmt.Sprintf("node-check-%d", i+1), node, []string{"--check", f}, r.tree, env)
			if rerr != nil {
				return "", rerr
			}
			if code != 0 {
				return "", fmt.Errorf("node --check %s exit %d: %s", filepath.Base(f), code, tail)
			}
		}
		return fmt.Sprintf("files=%d", len(files)), nil
	}, false) {
		return
	}
	if !r.step("mod-json", func() (string, error) {
		b, err := os.ReadFile(filepath.Join(r.tree, modDir, "mod.json"))
		if err != nil {
			return "", fmt.Errorf("read mod.json: %w", err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return "", fmt.Errorf("mod.json is not valid JSON: %w", err)
		}
		return "json=ok", nil
	}, false) {
		return
	}
	r.step("dashboard-test", func() (string, error) {
		code, tail, err := r.execLog("dashboard-test", node, []string{"--test", modTest}, r.tree, env)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return "", fmt.Errorf("node --test dashboard.test.mjs exit %d: %s", code, tail)
		}
		return "node --test dashboard.test.mjs exit=0", nil
	}, false)
}
