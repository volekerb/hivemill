package hm

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The role prompts, settings templates, the LaunchAgents and the tier rules are built into the binary, so a standalone
// install (`hivemill init`, no repo checkout) can launch sessions. The repo files stay the source: assets/ is a copy that
// `go generate ./internal/hm` refreshes, and TestAssetsMatchTheRepo fails when the two differ.
//
//go:generate sh -c "cp ../../../roles/hivemill/task.md ../../../roles/hivemill/task-judge.md ../../../roles/hivemill/task.json.in ../../../roles/hivemill/task-judge.json.in ../../../launchd/hivemill.daemon.plist.in assets/"
//go:generate sh -c "cp ../../../roles/hivemill/planner.md ../../../roles/hivemill/planner.json.in ../../../launchd/hivemill.digest.plist.in assets/"
//go:generate sh -c "cp ../../../launchd/hivemill.health.plist.in ../../../ci/tier_rules.json ../../../kit/skills.json assets/"
//go:generate sh -c "cp ../../../launchd/hivemill.obs.plist.in ../../../systemd/hivemill-obs.service assets/"
//go:embed assets/task.md assets/task-judge.md assets/task.json.in assets/task-judge.json.in assets/hivemill.daemon.plist.in
//go:embed assets/planner.md assets/planner.json.in assets/hivemill.digest.plist.in
//go:embed assets/hivemill.health.plist.in assets/tier_rules.json assets/skills.json
//go:embed assets/hivemill.obs.plist.in assets/hivemill-obs.service
var assets embed.FS

// AssetNames are the files in assets/, with the repo path each one is copied from.
var AssetNames = map[string]string{
	"task.md":                  "roles/hivemill/task.md",
	"task-judge.md":            "roles/hivemill/task-judge.md",
	"task.json.in":             "roles/hivemill/task.json.in",
	"task-judge.json.in":       "roles/hivemill/task-judge.json.in",
	"hivemill.daemon.plist.in": "launchd/hivemill.daemon.plist.in",
	"hivemill.health.plist.in": "launchd/hivemill.health.plist.in",
	"tier_rules.json":          "ci/tier_rules.json",
	"skills.json":              "kit/skills.json",
	"planner.md":               "roles/hivemill/planner.md",
	"planner.json.in":          "roles/hivemill/planner.json.in",
	"hivemill.digest.plist.in": "launchd/hivemill.digest.plist.in",
	"hivemill.obs.plist.in":    "launchd/hivemill.obs.plist.in",
	"hivemill-obs.service":     "systemd/hivemill-obs.service",
}

// Asset returns a built-in file by its name in assets/.
func Asset(name string) ([]byte, error) { return assets.ReadFile("assets/" + name) }

// readRoleFile reads a file of <home>/roles/hivemill (or another repo path of AssetNames). A home that has the file (a
// repo checkout) wins; a home without it (a standalone install) gets the built-in copy. Any other read error is
// returned as is, so a broken checkout never silently runs the built-in prompt.
func readRoleFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return b, err
	}
	name := filepath.Base(path)
	rel, ok := AssetNames[name]
	if !ok || !hasSuffixPath(path, rel) {
		return nil, err
	}
	if eb, e := Asset(name); e == nil {
		return eb, nil
	}
	return nil, err
}

func hasSuffixPath(path, rel string) bool {
	path, rel = filepath.ToSlash(filepath.Clean(path)), filepath.ToSlash(rel)
	return path == rel || len(path) > len(rel) && path[len(path)-len(rel)-1] == '/' && path[len(path)-len(rel):] == rel
}
