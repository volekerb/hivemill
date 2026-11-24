package s2cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"factory/go/internal/cli"
	ft "factory/go/internal/factorytest"
)

// Port of tests/test_flags.py (15 cases; see ported_s2cli.txt). DEFAULT_CONFIG is cli.DefaultConfig (generated from
// the Python literal). The registry is bin/flags.json: the real file when the repo has it, else the copy in
// testdata/flags.json (the merged-tree Go proof extracts only go/).

type regEntry map[string]any

func (e regEntry) str(k string) (string, bool) { s, ok := e[k].(string); return s, ok }

func defaultConfig() map[string]string {
	m := map[string]string{}
	for _, d := range cli.DefaultConfig {
		m[d.Key] = d.Value
	}
	return m
}

func configKeys() []string {
	var out []string
	for k := range defaultConfig() {
		out = append(out, k)
	}
	return out
}

func readRegistry(t testing.TB) []regEntry {
	t.Helper()
	b, err := os.ReadFile("testdata/flags.json")
	if root, rerr := ft.RepoRoot(); rerr == nil {
		if rb, err2 := os.ReadFile(filepath.Join(root, "bin", "flags.json")); err2 == nil {
			b, err = rb, nil
		}
	}
	if err != nil {
		t.Fatalf("no registry: %v", err)
	}
	entries, err := parseRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// parseRegistry decodes the registry document; the error says what is wrong. It never panics (FuzzParseRegistry).
func parseRegistry(b []byte) ([]regEntry, error) {
	var doc struct {
		Keys []regEntry `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("registry is not valid JSON with a keys list: %w", err)
	}
	return doc.Keys, nil
}

func live(e regEntry) bool { s, _ := e.str("state"); return s != "removed" }

func keyOf(e regEntry) string { s, _ := e.str("key"); return s }

var flagNames = []string{
	"block_conditions", "lead_parking", "stale_policy", "refresh_lane", "lead_less_dispatch", "review_autospawn",
	"prcheck_required", "git_push", "state_push", "mech_merge", "risk_tiers", "red_main_freeze", "gate_sandbox",
	"test_sandbox",
	"test_cgroup", "test_sandbox_subuid", "gate_post_status", "sandbox_roles", "worker_gh_roles", "task_clones",
	"tick_async_start", "lead_wake_debounce_s", "tick_phase_timers", "orphan_reap_mode", "scout_enabled",
	"gate_fast_projects", "issue_intake_repos", "cursor_pilot_roles", "agent_runtimes",
	"red_pr_owner_alert", "coder_ci_handoff", "idle_coder_reap", "idle_watchdog", "judge_wait_main_run",
	"owner_alert_feed", "owner_alert_send", "gh_call_trace", "fast_lane",
}

var baselineOn = map[string]bool{"git_push": true, "stale_policy": true, "scout_enabled": true, "gate_fast_projects": true}

func liveKeys(entries []regEntry) []string {
	var out []string
	for _, e := range entries {
		if live(e) {
			out = append(out, keyOf(e))
		}
	}
	return out
}

// diff returns the sorted elements of a that are not in b.
func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func TestFlagsRegistryEveryConfigKeyIsRegistered(t *testing.T) {
	entries := readRegistry(t)
	if missing := diff(configKeys(), liveKeys(entries)); len(missing) > 0 {
		t.Fatalf("config key(s) without a registry entry: %s - add an entry to bin/flags.json (docs/RELEASING.md#feature-flags)", strings.Join(missing, ", "))
	}
	if extra := diff(liveKeys(entries), configKeys()); len(extra) > 0 {
		t.Fatalf("registry key(s) not in DEFAULT_CONFIG: %v; remove them or set state removed", extra)
	}
}

func TestFlagsRegistryDefaultsMatch(t *testing.T) {
	cfg := defaultConfig()
	for _, e := range readRegistry(t) {
		if live(e) {
			if d, _ := e.str("default"); d != cfg[keyOf(e)] {
				t.Errorf("%s: default %q differs from DEFAULT_CONFIG %q", keyOf(e), d, cfg[keyOf(e)])
			}
		}
	}
}

func TestFlagsRegistryEnumsAndRequiredFields(t *testing.T) {
	kinds := map[string]bool{"flag": true, "setting": true}
	states := map[string]bool{"off": true, "shadow": true, "on": true, "removed": true}
	for _, e := range readRegistry(t) {
		k, _ := e.str("kind")
		if !kinds[k] {
			t.Errorf("%s: kind %q", keyOf(e), k)
		}
		for _, f := range []string{"key", "default", "summary", "goal", "doc"} {
			if _, ok := e.str(f); !ok {
				t.Errorf("%s: %s is not a string", keyOf(e), f)
			}
		}
		if s, _ := e.str("summary"); s == "" {
			t.Errorf("%s: empty summary", keyOf(e))
		}
		if k == "flag" {
			if st, _ := e.str("state"); !states[st] {
				t.Errorf("%s: state %q", keyOf(e), st)
			}
		} else if _, present := e["state"]; present {
			t.Errorf("%s: settings carry no state", keyOf(e))
		}
	}
}

func TestFlagsRegistryOnFlagsHavePromotedIn(t *testing.T) {
	re := regexp.MustCompile(`^(baseline|[0-9]+\.[0-9]+\.[0-9]+)$`)
	for _, e := range readRegistry(t) {
		if k, _ := e.str("kind"); k == "flag" {
			if st, _ := e.str("state"); st == "on" {
				if p, _ := e.str("promoted_in"); !re.MatchString(p) {
					t.Errorf("%s: promoted_in %q", keyOf(e), p)
				}
			}
		}
	}
}

func TestFlagsRegistryRemovedKeysAreNotConfig(t *testing.T) {
	cfg := defaultConfig()
	for _, e := range readRegistry(t) {
		if st, _ := e.str("state"); st == "removed" {
			if _, in := cfg[keyOf(e)]; in {
				t.Errorf("%s is removed but still in DEFAULT_CONFIG", keyOf(e))
			}
		}
	}
}

func TestFlagsRegistrySortedWithoutDuplicates(t *testing.T) {
	var keys []string
	for _, e := range readRegistry(t) {
		keys = append(keys, keyOf(e))
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("registry keys are not sorted")
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("duplicate registry key %s", k)
		}
		seen[k] = true
	}
}

func TestFlagsRegistryExactFlagListAndStates(t *testing.T) {
	var flags []string
	by := map[string]regEntry{}
	for _, e := range readRegistry(t) {
		by[keyOf(e)] = e
		if k, _ := e.str("kind"); k == "flag" {
			flags = append(flags, keyOf(e))
		}
	}
	want := append([]string(nil), flagNames...)
	sort.Strings(flags)
	sort.Strings(want)
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("flag set differs: unexpected %v, missing %v", diff(flags, want), diff(want, flags))
	}
	for _, k := range want {
		st, _ := by[k].str("state")
		pr, _ := by[k].str("promoted_in")
		switch {
		case baselineOn[k]:
			if st != "on" || pr != "baseline" {
				t.Errorf("%s: (%s, %s), want (on, baseline)", k, st, pr)
			}
		case k == "orphan_reap_mode", k == "idle_watchdog":
			if st != "shadow" {
				t.Errorf("%s: state %s, want shadow", k, st)
			}
		default:
			if st != "off" {
				t.Errorf("%s: state %s, want off", k, st)
			}
		}
	}
}

func TestFlagsRegistryCoderCIResumeMaxIsARegisteredSetting(t *testing.T) {
	var e regEntry
	for _, x := range readRegistry(t) {
		if keyOf(x) == "coder_ci_resume_max" {
			e = x
		}
	}
	if e == nil {
		t.Fatal("coder_ci_resume_max is not registered")
	}
	k, _ := e.str("kind")
	d, _ := e.str("default")
	doc, _ := e.str("doc")
	if k != "setting" || d != "2" || doc != "docs/lead-wake.md" {
		t.Fatalf("got (%s, %s, %s)", k, d, doc)
	}
	if defaultConfig()["coder_ci_resume_max"] != "2" {
		t.Fatal("DEFAULT_CONFIG coder_ci_resume_max is not 2")
	}
}

func TestFlagsRegistryUnregisteredKeyIsReported(t *testing.T) {
	// C4: the coverage check names the key. A fake config key with no registry entry shows up as missing.
	keys := append(configKeys(), "zz_fake_key")
	if missing := diff(keys, liveKeys(readRegistry(t))); !reflect.DeepEqual(missing, []string{"zz_fake_key"}) {
		t.Fatalf("missing = %v, want [zz_fake_key]", missing)
	}
}

// ---- the command ---------------------------------------------------------------------------------------------------

func snapshot(t *testing.T, e *ft.Env) map[string][][]string {
	t.Helper()
	m := map[string][][]string{}
	for _, tb := range []string{"config", "agents", "events", "messages"} {
		m[tb] = e.SQL("SELECT * FROM " + tb)
	}
	return m
}

func TestFlagsCommandBlockConditionsRejectsOn(t *testing.T) {
	e := ft.New(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.FailWith(ft.Opts{Agent: "zz-test-director"}, 2, "config", "block_conditions", "on")
}

func TestFlagsCommandDefaultListsFlagsOnly(t *testing.T) {
	e := ft.New(t)
	before := snapshot(t, e)
	r := e.OKWith(ft.Opts{Agent: "owner"}, "flags")
	lines := strings.Split(strings.TrimRight(r.Stdout, "\n"), "\n")
	if !reflect.DeepEqual(strings.Fields(lines[0]), []string{"KEY", "STATE", "DEFAULT", "CURRENT", "GOAL", "DOC"}) {
		t.Fatalf("header = %q", lines[0])
	}
	var got, row []string
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		got = append(got, f[0])
		if f[0] == "git_push" {
			row = f
		}
	}
	want := append([]string(nil), flagNames...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listed flags differ: unexpected %v, missing %v", diff(got, want), diff(want, got))
	}
	// the harness turns git_push off on the temp home
	if len(row) < 4 || !reflect.DeepEqual(row[:4], []string{"git_push", "on", "1", "0"}) {
		t.Fatalf("git_push row = %v", row)
	}
	if !reflect.DeepEqual(before, snapshot(t, e)) {
		t.Fatal("`factory flags` changed the DB")
	}
}

func TestFlagsCommandAnyRoleMayRunIt(t *testing.T) {
	e := ft.New(t)
	e.OKWith(ft.Opts{Agent: "coder-x"}, "flags")
}

func TestFlagsCommandAllListsEveryEntry(t *testing.T) {
	e := ft.New(t)
	lines := strings.Split(strings.TrimRight(e.OKWith(ft.Opts{Agent: "owner"}, "flags", "--all").Stdout, "\n"), "\n")
	if got, want := len(lines)-1, len(readRegistry(t)); got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "serve_port ") {
			if st := strings.Fields(l)[1]; st != "-" {
				t.Fatalf("serve_port state = %q, want -", st)
			}
			return
		}
	}
	t.Fatal("no serve_port row")
}

func TestFlagsCommandJSONHasCurrent(t *testing.T) {
	e := ft.New(t)
	var data []map[string]any
	if err := json.Unmarshal([]byte(e.OKWith(ft.Opts{Agent: "owner"}, "flags", "--json").Stdout), &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != len(readRegistry(t)) {
		t.Fatalf("entries = %d, want %d", len(data), len(readRegistry(t)))
	}
	cur := map[string]any{}
	for _, d := range data {
		cur[d["key"].(string)] = d["current"]
	}
	if cur["serve_port"] != defaultConfig()["serve_port"] || cur["git_push"] != "0" {
		t.Fatalf("current serve_port=%v git_push=%v", cur["serve_port"], cur["git_push"])
	}
}

func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "__pycache__" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		info, err := d.Info()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, in); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

func TestFlagsCommandBadRegistryExits2(t *testing.T) {
	e := ft.New(t)
	e.NeedPython() // the Go CLI hands an unreadable registry to bin/factory, which words the error
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "bin", "factory")); err != nil {
		t.Skipf("the repo has no bin/factory (%v): the registry error is worded by Python", err)
	}
	// a copy of bin/ with an invalid registry, and a factory-go next to it: it reads flags.json next to its entry point
	root := filepath.Join(e.Home, "repocopy")
	for _, d := range []string{"bin", "web", "scout"} {
		if _, err := os.Stat(filepath.Join(repo, d)); err == nil {
			copyTree(t, filepath.Join(repo, d), filepath.Join(root, d))
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "flags.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	goCopy := filepath.Join(root, "bin", "factory-go")
	b, err := os.ReadFile(e.GoBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goCopy, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goCopy, "flags")
	cmd.Env = e.Environ("owner", nil)
	cmd.Dir = e.Home
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("exit = %v, want 2\nstderr: %s", err, se.String())
	}
	if !strings.HasPrefix(se.String(), "factory flags: cannot read bin/flags.json:") {
		t.Fatalf("stderr = %q", se.String())
	}
}

func FuzzParseRegistry(f *testing.F) {
	for _, s := range []string{`{"keys":[{"key":"a","kind":"flag","state":"on"}]}`, `{not json`, `{"keys":null}`, `[]`, ``, `{"keys":[1,"x",{"key":3}]}`} {
		f.Add([]byte(s))
	}
	if b, err := os.ReadFile("testdata/flags.json"); err == nil {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		entries, _ := parseRegistry(b) // the error is the answer for bad input; only a panic fails
		for _, e := range entries {
			keyOf(e)
			live(e)
		}
	})
}
