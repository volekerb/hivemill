package hm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Claude Code reports these deny entries as no-ops: file-path rules apply only
// through Edit(...), and the MultiEdit and Advisor tools do not exist.
var noOpDeny = regexp.MustCompile(`^(Write|MultiEdit|NotebookEdit)\(|^(MultiEdit|Advisor)$`)

// TestSettingsDenyRulesEffective renders every role settings template and checks
// that no deny entry is a no-op and that every path listed in
// testdata/settings_deny_paths.txt is still denied, with Edit(path).
func TestSettingsDenyRulesEffective(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	onDisk := Env{Home: t.TempDir(), UserHome: userHome}   // renders roles/hivemill
	embedded := Env{Home: t.TempDir(), UserHome: userHome} // no files: renders go/internal/hm/assets
	if err := os.MkdirAll(filepath.Join(onDisk.Home, "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "roles", "hivemill"), filepath.Join(onDisk.Home, "roles", "hivemill")); err != nil {
		t.Fatal(err)
	}
	tokens := func(home string) *strings.Replacer {
		return strings.NewReplacer("@FACTORY_LIVE_HOME@", home, "@FACTORY_HOME_DIR@", userHome,
			"@FACTORY_USER_CONFIG@", filepath.Join(userHome, ".config"))
	}

	pinned := map[string][]string{}
	list, err := os.ReadFile("testdata/settings_deny_paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, paths, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("bad line: %q", line)
		}
		pinned[name] = strings.Fields(paths)
	}

	var names []string
	for _, dir := range []string{"roles/hivemill", "roles/settings", "go/internal/hm/assets"} {
		found, err := filepath.Glob(filepath.Join(repo, dir, "*.json.in"))
		if err != nil || len(found) == 0 {
			t.Fatalf("no templates in %s: %v", dir, err)
		}
		for _, f := range found {
			names = append(names, dir+"/"+filepath.Base(f))
		}
	}
	for name := range pinned {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Errorf("pinned template is gone: %v", err)
		}
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			role := strings.TrimSuffix(filepath.Base(name), ".json.in")
			var body []byte
			home := onDisk.Home
			if strings.HasPrefix(name, "roles/settings/") {
				raw, err := os.ReadFile(filepath.Join(repo, name))
				if err != nil {
					t.Fatal(err)
				}
				body = []byte(tokens(home).Replace(string(raw)))
			} else {
				env := onDisk
				if strings.HasPrefix(name, "go/") {
					env = embedded
				}
				home = env.Home
				path, err := RenderSettings(env, role)
				if err != nil {
					t.Fatal(err)
				}
				if body, err = os.ReadFile(path); err != nil {
					t.Fatal(err)
				}
			}
			var s struct {
				Permissions struct {
					Deny []string `json:"deny"`
				} `json:"permissions"`
			}
			if err := json.Unmarshal(body, &s); err != nil {
				t.Fatal(err)
			}
			if len(s.Permissions.Deny) == 0 {
				t.Fatal("no deny entries")
			}
			deny := map[string]bool{}
			for _, rule := range s.Permissions.Deny {
				if noOpDeny.MatchString(rule) {
					t.Errorf("deny entry has no effect: %s", rule)
				}
				if deny[rule] {
					t.Errorf("duplicate deny entry: %s", rule)
				}
				deny[rule] = true
			}
			for _, p := range pinned[name] {
				if want := "Edit(" + tokens(home).Replace(p) + ")"; !deny[want] {
					t.Errorf("missing deny entry: %s", want)
				}
			}
		})
	}
}
