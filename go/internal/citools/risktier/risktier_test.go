package risktier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const binSrc = "def cmd_other():\n    return 1\n\n\ndef cmd_judge():\n    return 1\n\n\ndef judge_pick():\n    return 1\n\n\ndef helper():\n    return 1\n\n\nSCHEMA = 1\n"

func baseFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.write("bin/factory_cli.py", binSrc)
	f.write("tests/test_a.py", "x = 1\n")
	f.write("tests/fixtures/f.txt", "f\n")
	f.write("web/app.py", "x = 1\n")
	f.write("docs/a.md", "# a\n")
	f.write("README.md", "hi\n")
	f.write("roles/hooks/h.sh", "echo\n")
	f.write("roles/role.md", "# r\n")
	f.write("roles/plugins/x/y.md", "# y\n")
	f.write("ci/tier_rules.json", readRules(t))
	f.commit("base")
	return f
}

func readRules(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "ci", "tier_rules.json"))
	if err != nil {
		t.Skipf("ci/tier_rules.json not found: %v", err)
	}
	return string(b)
}

func lines(n int) string { return strings.Repeat("y = 1\n", n) }

type scenario struct {
	name   string
	mutate func(f *fixture)
	want   string // the tier that must come out whatever Python says
}

var scenarios = []scenario{
	{"docs only", func(f *fixture) { f.write("docs/a.md", "# a2\n"); f.write("README.md", "yo\n") }, "T0"},
	{"roles markdown is not docs", func(f *fixture) { f.write("roles/plugins/x/y.md", "# y2\n") }, "T1"},
	{"tests only, large", func(f *fixture) { f.write("tests/test_a.py", lines(300)) }, "T1"},
	{"tests fixtures only", func(f *fixture) { f.write("tests/fixtures/f.txt", lines(300)) }, "T1"},
	{"small change", func(f *fixture) { f.write("web/app.py", "x = 2\n") }, "T1"},
	{"medium change", func(f *fixture) { f.write("web/app.py", lines(200)) }, "T2"},
	{"large change", func(f *fixture) { f.write("web/app.py", lines(650)) }, "T3"},
	{"sensitive path", func(f *fixture) { f.write("roles/hooks/h.sh", "echo 2\n") }, "T3"},
	{"sensitive roles markdown", func(f *fixture) { f.write("roles/role.md", "# r2\n") }, "T3"},
	{"sensitive def", func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def cmd_judge():\n    return 1", "def cmd_judge():\n    return 2", 1))
	}, "T3"},
	{"sensitive def by glob", func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def judge_pick():\n    return 1", "def judge_pick():\n    return 2", 1))
	}, "T3"},
	{"top-level statement in bin", func(f *fixture) { f.write("bin/factory_cli.py", binSrc+"import os\n") }, "T3"},
	{"harmless def", func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def helper():\n    return 1", "def helper():\n    return 2", 1))
	}, "T1"},
	{"unreadable sensitive file", func(f *fixture) { f.write("bin/factory_cli.py", "x = (1,\n") }, "T3"},
	{"binary file counts as large", func(f *fixture) { f.write("web/blob.bin", "\x00\x01\x00bin\x00") }, "T3"},
	{"deleted sensitive file", func(f *fixture) { f.remove("bin/factory_cli.py") }, "T3"},
	{"empty diff", func(f *fixture) {}, "T1"},
}

const pyClassify = `import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("risk_tier_ref", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
rules = mod.load_rules(sys.argv[2])
tier, reasons = mod.classify_range(sys.argv[3], sys.argv[4], sys.argv[5], rules)
json.dump({"tier": tier, "reasons": reasons}, sys.stdout)
`

type result struct {
	Tier    string   `json:"tier"`
	Reasons []string `json:"reasons"`
}

func TestRiskParity(t *testing.T) {
	script := needPython(t, "ci/risk_tier.py")
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := baseFixture(t)
			base := f.git("rev-parse", "HEAD")
			sc.mutate(f)
			head := f.commit("head")
			rulesPath := filepath.Join(f.dir, "ci", "tier_rules.json")
			so, se, code := pyRun(t, f.dir, "-c", pyClassify, script, rulesPath, f.dir, base, head)
			if code != 0 {
				t.Fatalf("python reference failed: %s", se)
			}
			var want result
			if err := json.Unmarshal([]byte(so), &want); err != nil {
				t.Fatal(err)
			}
			rules, err := LoadRules(rulesPath)
			if err != nil {
				t.Fatal(err)
			}
			tier, reasons, err := ClassifyRange(f.dir, base, head, rules)
			if err != nil {
				t.Fatal(err)
			}
			if reasons == nil {
				reasons = []string{}
			}
			if want.Reasons == nil {
				want.Reasons = []string{}
			}
			if tier != want.Tier || !reflect.DeepEqual(reasons, want.Reasons) {
				t.Errorf("go %s %q, python %s %q", tier, reasons, want.Tier, want.Reasons)
			}
			if tier != sc.want {
				t.Errorf("tier = %s, want %s", tier, sc.want)
			}
			// the command prints the same tier and reasons
			out, _, ccode := runGo(Main, "--root", f.dir, "--base", base, "--head", head)
			wantOut := tier + "\n"
			for _, r := range reasons {
				wantOut += "reason: " + r + "\n"
			}
			if ccode != 0 || out != wantOut {
				t.Errorf("command: exit %d %q, want %q", ccode, out, wantOut)
			}
		})
	}
}

// Parity of the pure classifier on rule shapes the fixtures do not reach.
func TestRiskParityTierFunction(t *testing.T) {
	script := needPython(t, "ci/risk_tier.py")
	rules, err := LoadRules(filepath.Join(repoRoot(t), "ci", "tier_rules.json"))
	if err != nil {
		t.Skip(err)
	}
	cases := []struct {
		paths []string
		lines int
	}{
		{[]string{"docs/x.md", "README.md"}, 10},
		{[]string{"docs/x.md", "ci/x.py"}, 10},
		{[]string{"ci/x.py"}, 151},
		{[]string{"tests/test_x.py", "tests/fixtures/a/b.json"}, 500},
		{[]string{"tests/helper.py"}, 500},
		{[]string{"roles/plugins/p/readme.md"}, 5},
		{[]string{"roles/role.md"}, 5},
		{[]string{"roles/a/b/c.md"}, 5},
		{[]string{"go/internal/guard/x.go"}, 1},
		{[]string{"go/internal/other/x.go"}, 1},
		{[]string{"scout/nsbox.py"}, 1},
		{[]string{"ci/guard_corpus.json"}, 1},
		{[]string{}, 0},
		{[]string{"a.txt"}, 600},
		{[]string{"a.txt"}, 601},
	}
	for _, c := range cases {
		name := fmt.Sprintf("%v/%d", c.paths, c.lines)
		t.Run(name, func(t *testing.T) {
			arg, _ := json.Marshal(map[string]any{"paths": c.paths, "lines": c.lines})
			snippet := "import importlib.util, json, sys\nspec = importlib.util.spec_from_file_location('r', sys.argv[1])\nmod = importlib.util.module_from_spec(spec)\nspec.loader.exec_module(mod)\na = json.loads(sys.argv[3])\nr = mod.load_rules(sys.argv[2])\nsys.stdout.write(mod.tier({}, a['paths'], a['lines'], r) + '|' + ';'.join(mod.reasons({}, a['paths'], a['lines'], r)))\n"
			so, se, code := pyRun(t, t.TempDir(), "-c", snippet, script, filepath.Join(repoRoot(t), "ci", "tier_rules.json"), string(arg))
			if code != 0 {
				t.Fatalf("python reference failed: %s", se)
			}
			got, err := Tier(map[string][2]string{}, c.paths, c.lines, rules)
			if err != nil {
				t.Fatal(err)
			}
			why, _ := Reasons(map[string][2]string{}, c.paths, c.lines, rules)
			if g := got + "|" + strings.Join(why, ";"); g != so {
				t.Errorf("go %q, python %q", g, so)
			}
		})
	}
}

func TestMaxTier(t *testing.T) {
	cases := [][3]string{{"T1", "T3", "T3"}, {"T3", "T1", "T3"}, {"", "T2", "T2"}, {"T2", "", "T2"}, {"T0", "T0", "T0"}}
	for _, c := range cases {
		if got := MaxTier(c[0], c[1]); got != c[2] {
			t.Errorf("MaxTier(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestLoadRulesFailsClosed(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	missingKey := filepath.Join(dir, "r.json")
	os.WriteFile(missingKey, []byte(`{"docs_globs": []}`), 0o644)
	for _, p := range []string{filepath.Join(dir, "absent.json"), missingKey} {
		if _, err := LoadRules(p); err == nil || !strings.Contains(err.Error(), "restore ci/tier_rules.json") {
			t.Errorf("LoadRules(%s) = %v, want an error naming the fix", p, err)
		}
	}
	_, se, code := runGo(Main, "--base", "HEAD", "--rules", filepath.Join(dir, "absent.json"))
	if code != 2 || !strings.Contains(se, "cannot load tier rules") {
		t.Errorf("command with bad rules: exit %d %q, want 2", code, se)
	}
	f := baseFixture(t)
	if _, _, code := runGo(Main, "--root", f.dir, "--base", "--evil"); code == 0 {
		t.Errorf("a ref starting with '-' must be refused")
	}
	if _, _, err := ClassifyRange(f.dir, "-x", "HEAD", Rules{}); err == nil {
		t.Errorf("ClassifyRange accepted a ref starting with '-'")
	}
}
