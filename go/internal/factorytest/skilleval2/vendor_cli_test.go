package skilleval2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	sc "factory/go/internal/scout"
)

// Port of the VendorBase classes of tests/test_skilleval_vendor.py: PermissionAndRootTests (5), ExecutableExclusionTests
// (4), PromptOnlyVendorTests (5), CapTests (3), VendoredCopyPlantTests (1), PrescreenVendorTests (4), RescreenTests
// (15) and EvalAllowedTests (1). The CLI cases skip where python3 is absent (see vendor_helpers_test.go).

func TestVendorRoles(t *testing.T) {
	w := newVWorld(t)
	w.e.NeedPython() // worker callers are refused by Python
	for _, r := range []string{"coder", "qa", "reviewer", "pm", "scout", "evaluator"} {
		w.expect(3, "zz-test-"+r, "skilleval", "vendor", "C-0001")
	}
	vendorEq(t, "api calls", len(w.apiCalls()), 0)
	if vendorExists(w.root) {
		t.Fatalf("%s exists after refused roles", w.root)
	}
	for _, who := range []string{"zz-test-director", "zz-test-lead", "owner"} {
		w.clearCandidates()
		if err := os.RemoveAll(w.root); err != nil {
			t.Fatal(err)
		}
		w.tree(vendorPluginTree, "plugins/p")
		w.vendor("", who, 0)
		if !vendorExists(filepath.Join(w.vendorDir(), "VENDOR.json")) {
			t.Fatalf("%s: no VENDOR.json", who)
		}
	}
}

func TestVendorDefaultRootIsCacheDir(t *testing.T) {
	w := newVWorld(t)
	w.exec("DELETE FROM config WHERE key='skilleval_root'")
	vendorHas(t, "config", w.expect(0, "owner", "config").Stdout, "skilleval_root = ~/.cache/factory-skilleval")
	home := t.TempDir()
	t.Setenv("HOME", home)
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolved, ".cache", "factory-skilleval")
	for _, in := range []string{"", "~/.cache/factory-skilleval"} {
		got, err := sc.ResolveRoot(in, "")
		if err != nil || got != want {
			t.Fatalf("ResolveRoot(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestVendorDefaultAppliesThroughTheCLI(t *testing.T) {
	w := newVWorld(t)
	w.exec("DELETE FROM config WHERE key='skilleval_root'")
	w.tree(vendorPluginTree, "plugins/p")
	w.vendor("", "", 0)
	d := filepath.Join(w.e.UserHome, ".cache", "factory-skilleval", "vendor", "C-0001@"+vendorSHA)
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		t.Fatalf("%s is not a directory: %v", d, err)
	}
}

func TestVendorRootInsideFactoryOrClaudeRefused(t *testing.T) {
	w := newVWorld(t)
	for _, bad := range []string{"~/factory/evals", "~/.claude/evals", "~/factory", "~", filepath.Join(w.e.Home, "x"), "relative/dir"} {
		if r := w.run("owner", "config", "skilleval_root", bad); r.Exit != 2 {
			t.Fatalf("config skilleval_root %s: exit %d, want 2: %s", bad, r.Exit, r.Stderr)
		}
		w.setRoot(bad)
		w.clearCandidates()
		w.tree(vendorPluginTree, "plugins/p")
		r := w.vendor("", "", 2)
		vendorHas(t, bad+" stderr", r.Stderr, "skilleval_root")
		vendorEq(t, bad+" api calls", len(w.apiCalls()), 0)
	}
	for _, d := range []string{"factory", ".claude"} {
		if vendorExists(filepath.Join(w.e.UserHome, d)) {
			t.Fatalf("%s was created under the user home", d)
		}
	}
}

func TestVendorSymlinkIntoFactoryRefused(t *testing.T) {
	w := newVWorld(t)
	link := filepath.Join(w.e.Root, "sneaky")
	if err := os.Symlink(filepath.Join(w.e.UserHome, ".claude"), link); err != nil {
		t.Fatal(err)
	}
	if r := w.run("owner", "config", "skilleval_root", filepath.Join(link, "evals")); r.Exit != 2 {
		t.Fatalf("exit %d, want 2: %s", r.Exit, r.Stderr)
	}
}

// -- ExecutableExclusionTests -------------------------------------------------------------------------

func TestVendorExecutableFixtureIsNotPromptOnlyAndNothingLands(t *testing.T) {
	w := newVWorld(t)
	tr := w.tree(vendorExecTree, "")
	var entries []sc.Entry
	for p, f := range vendorExecTree {
		entries = append(entries, sc.Entry{Path: p, Mode: f.mode, Type: "blob", Sha: "x"})
	}
	res := sc.Classify(entries, "", func(sc.Entry) (string, error) { return "", nil }, false)
	if res.Risk == "prompt-only" {
		t.Fatal("exec fixture classified prompt-only")
	}
	for _, flag := range []string{"scripts", "hooks", "mcp", "symlinks"} {
		vendorHas(t, "risk", res.Risk, flag)
	}
	r := w.vendor("", "", 2)
	vendorHas(t, "stderr", r.Stderr, "fresh install risk")
	if vendorExists(w.root) {
		t.Fatal("nothing may be written")
	}
	if w.cand("")["install_risk"] == "prompt-only" {
		t.Fatal("the stale DB value is not replaced")
	}
	for _, call := range w.apiCalls() {
		ok := strings.Contains(call, "git/trees/"+vendorSHA)
		for _, b := range tr.blobShas {
			ok = ok || strings.Contains(call, b)
		}
		if !ok {
			t.Fatalf("unpinned call %q", call)
		}
	}
}

func TestVendorDBRiskNotPromptOnlyIsRefusedWithoutCalls(t *testing.T) {
	w := newVWorld(t)
	w.tree(vendorPluginTree, "plugins/p", vOpt{risk: "scripts"})
	r := w.vendor("", "", 2)
	vendorHas(t, "stderr", r.Stderr, "not prompt-only")
	vendorEq(t, "api calls", len(w.apiCalls()), 0)
	if vendorExists(w.root) {
		t.Fatal("root exists")
	}
}

func TestVendorStaleDBRiskPromptOnlyButFreshTreeHasAScript(t *testing.T) {
	w := newVWorld(t)
	w.tree(vmerge(vendorPluginTree, vFiles{"plugins/p/skills/foo/run.sh": vf("echo hi\n")}), "plugins/p")
	vendorEq(t, "stored risk", w.cand("")["install_risk"], "prompt-only")
	r := w.vendor("", "", 2)
	vendorHas(t, "stderr", r.Stderr, "scripts")
	if vendorExists(w.root) {
		t.Fatal("root exists")
	}
	vendorHas(t, "install_risk", w.cand("")["install_risk"], "scripts")
}

// TestVendorSkipRulesListEveryExcludedKindWithAReason: Python calls the private vendor._plan. Go's planPaths is
// unexported, so this drives the exported Vendor with the same entries. A tree that holds an executable-mode, symlink,
// submodule or script entry is refused by the fresh classification before planPaths runs, so the skip reasons
// ("executable mode", "symlink", "submodule", "not allow-listed") are not observable here; the ".." escape is.
// not ported: the {path: reason} skip map of test_skip_rules_list_every_excluded_kind_with_a_reason - planPaths is private and Classify refuses those trees first.
func TestVendorSkipRulesListEveryExcludedKindWithAReason(t *testing.T) {
	serve := func(files vFiles) *sc.Gh {
		tr := newVTree(files, -1)
		return &sc.Gh{Timeout: 5 * time.Second, Run: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
			for _, r := range tr.rules {
				if strings.Contains(args[0], r.Match[0]) {
					return []byte(r.Out), nil, 0, nil
				}
			}
			return nil, []byte("Not Found"), 1, nil
		}}
	}
	cand := sc.VCand{ID: "C-0001", Repo: vendorRepoName, HeadSha: vendorSHA, Licence: "MIT", InstallRisk: "prompt-only", Status: "new"}
	for name, files := range map[string]vFiles{
		"dotdot":     {"SKILL.md": vf(vendorSkillMD), "../x.md": vf("x\n")},
		"exec bit":   {"SKILL.md": vf(vendorSkillMD), "b.md": {"100755", []byte("b\n")}},
		"symlink":    {"SKILL.md": vf(vendorSkillMD), "c.md": {"120000", []byte("SKILL.md")}},
		"submodule":  {"SKILL.md": vf(vendorSkillMD), "d": {"160000", []byte("d")}},
		"script ext": {"SKILL.md": vf(vendorSkillMD), "e.py": vf("print(1)\n")},
	} {
		root := filepath.Join(t.TempDir(), "root")
		_, err := sc.Vendor(serve(files), cand, root)
		var ve *sc.VendorError
		if !errors.As(err, &ve) || ve.Code != 2 {
			t.Fatalf("%s: err = %v, want a VendorError with exit 2", name, err)
		}
		if vendorExists(root) {
			t.Fatalf("%s: root written", name)
		}
	}
}

// -- PromptOnlyVendorTests ----------------------------------------------------------------------------

func TestVendorExactlyTheAllowListedFilesLandWithMode0644(t *testing.T) {
	w := newVWorld(t)
	tr := w.tree(vendorPluginTree, "plugins/p")
	out := w.vendor("", "", 0).Stdout
	d := w.vendorDir()
	vendorHas(t, "stdout", out, "vendored: 5 file(s), 0 skipped, 0 pre-screen hits")
	plugin := filepath.Join(d, "demo")
	want := []string{".claude-plugin/plugin.json", "LICENSE", "skills/foo/SKILL.md", "skills/foo/data.csv", "skills/foo/reference.md"}
	got := vendorFilesUnder(t, plugin)
	vendorEq(t, "files", got, want)
	for _, rel := range got {
		st, err := os.Stat(filepath.Join(plugin, rel))
		if err != nil || st.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode %v (%v), want 0644", rel, st.Mode().Perm(), err)
		}
	}
	b, err := os.ReadFile(filepath.Join(plugin, "skills/foo/SKILL.md"))
	if err != nil || string(b) != vendorSkillMD {
		t.Fatalf("SKILL.md = %q, %v", b, err)
	}
	meta := vendorReadMeta(t, d)
	vendorEq(t, "repo/path/sha/licence", []any{meta["repo"], meta["path"], meta["sha"], meta["licence"]}, []any{vendorRepoName, "plugins/p", vendorSHA, "MIT"})
	vendorEq(t, "copied", vendorStrList(meta["copied"]), want)
	vendorEq(t, "skipped/hits/wrapped", fmt.Sprint(meta["skipped"], meta["prescreen_hits"], meta["wrapped"]), "[] [] false")
	vendorEq(t, "status", w.cand("")["status"], "new")
	if err := sc.AssertPromptOnlyTree(plugin); err != nil {
		t.Fatal(err)
	}
	var rest []string
	for _, x := range vendorFilesUnder(t, w.root) {
		if !strings.Contains(x, "/demo/") {
			rest = append(rest, x)
		}
	}
	vendorEq(t, "files outside demo", rest, []string{"vendor/C-0001@" + vendorSHA + "/VENDOR.json"})
	if m, _ := filepath.Glob(filepath.Join(w.root, "vendor", "*.partial")); len(m) != 0 {
		t.Fatalf("partial dirs left: %v", m)
	}
	for _, call := range w.apiCalls() {
		if !vendorCallPinned(call, tr.blobShas) {
			t.Fatalf("unpinned call %q", call)
		}
		if vendorMainRe.MatchString(call) || strings.Contains(call, "HEAD") {
			t.Fatalf("call %q names main or HEAD", call)
		}
	}
	// Python greps scout/vendor.py for subprocess and git clone; the Go counterpart reads the real vendor.go.
	mod, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(mod, "internal", "scout", "vendor.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"os/exec", "git clone"} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("vendor.go contains %q", bad)
		}
	}
}

func TestVendorLooseSkillIsWrapped(t *testing.T) {
	w := newVWorld(t)
	w.tree(vFiles{"SKILL.md": vf(vendorSkillMD), "notes.md": vf("# n\n"), "LICENSE": vf("MIT\n")}, "")
	w.vendor("", "", 0)
	d := w.vendorDir()
	meta := vendorReadMeta(t, d)
	vendorEq(t, "plugin/wrapped", []any{meta["plugin"], meta["wrapped"]}, []any{"cand-c-0001", true})
	plugin := filepath.Join(d, "cand-c-0001")
	vendorEq(t, "files", vendorFilesUnder(t, plugin),
		[]string{".claude-plugin/plugin.json", "skills/zz-repo/LICENSE", "skills/zz-repo/SKILL.md", "skills/zz-repo/notes.md"})
	b, err := os.ReadFile(filepath.Join(plugin, ".claude-plugin/plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gen map[string]string
	if err := json.Unmarshal(b, &gen); err != nil {
		t.Fatal(err)
	}
	vendorEq(t, "generated plugin.json", gen, map[string]string{"name": "cand-c-0001", "description": "G-009 eval copy of " + vendorRepoName + "@" + vendorSHA})
	if err := sc.AssertPromptOnlyTree(plugin); err != nil {
		t.Fatal(err)
	}
}

func TestVendorIdempotentSecondRunMakesNoCalls(t *testing.T) {
	w := newVWorld(t)
	w.tree(vendorPluginTree, "plugins/p")
	w.vendor("", "", 0)
	n := len(w.e.Calls("gh"))
	if n == 0 {
		t.Fatal("first run made no gh calls")
	}
	vendorHas(t, "stdout", w.vendor("", "", 0).Stdout, "already vendored")
	vendorEq(t, "gh calls", len(w.e.Calls("gh")), n)
}

func TestVendorDamagedCopyIsVendoredAgain(t *testing.T) {
	w := newVWorld(t)
	w.tree(vendorPluginTree, "plugins/p")
	w.vendor("", "", 0)
	n := len(w.e.Calls("gh"))
	if err := os.Remove(filepath.Join(w.vendorDir(), "demo", "LICENSE")); err != nil {
		t.Fatal(err)
	}
	w.vendor("", "", 0)
	if len(w.e.Calls("gh")) <= n {
		t.Fatal("no refetch after damage")
	}
	vendorHas(t, "files", strings.Join(vendorFilesUnder(t, filepath.Join(w.vendorDir(), "demo")), ","), "LICENSE")
}

func TestVendorBlobThatDoesNotMatchItsShaIsRefused(t *testing.T) {
	w := newVWorld(t)
	tr := newVTree(vendorPluginTree, -1)
	skill := vendorBlobSha([]byte(vendorSkillMD))
	for i, r := range tr.rules {
		if strings.Contains(r.Match[0], skill) {
			tr.rules[i].Out = vendorJSON(map[string]any{"content": base64.StdEncoding.EncodeToString([]byte("tampered")), "encoding": "base64"})
		}
	}
	w.e.Script("gh", tr.rules...)
	w.seed("C-0001", "plugins/p", "prompt-only", "new")
	vendorHas(t, "stderr", w.vendor("", "", 2).Stderr, "does not match its sha")
	if vendorExists(w.root) {
		t.Fatal("root exists")
	}
}

// -- CapTests -----------------------------------------------------------------------------------------

func TestVendor201FilesRefused200Accepted(t *testing.T) {
	w := newVWorld(t)
	many := vFiles{}
	for i := 0; i < 200; i++ {
		many[fmt.Sprintf("skills/foo/r%03d.md", i)] = vf("x\n")
	}
	w.tree(vmerge(vFiles{"SKILL.md": vf(vendorSkillMD)}, many), "")
	vendorHas(t, "stderr", w.vendor("", "", 2).Stderr, "cap of 200")
	if vendorExists(w.root) {
		t.Fatal("root exists")
	}
	w.clearCandidates()
	few := vFiles{"SKILL.md": vf(vendorSkillMD)}
	for i := 0; i < 199; i++ {
		few[fmt.Sprintf("skills/foo/r%03d.md", i)] = vf("x\n")
	}
	w.tree(few, "")
	w.vendor("", "", 0)
}

func TestVendorOver2MBRefused(t *testing.T) {
	w := newVWorld(t)
	w.tree(vFiles{"SKILL.md": vf(vendorSkillMD), "a.md": vf(strings.Repeat("a", 1_100_000)), "b.md": vf(strings.Repeat("b", 1_100_000))}, "")
	vendorHas(t, "stderr", w.vendor("", "", 2).Stderr, "byte cap")
	if vendorExists(w.root) {
		t.Fatal("root exists")
	}
}

func TestVendorUnderstatedTreeSizesAreCaughtWhileWritingAndThePartialCopyRemoved(t *testing.T) {
	w := newVWorld(t)
	w.tree(vFiles{"SKILL.md": vf(vendorSkillMD), "a.md": vf(strings.Repeat("a", 1_100_000)), "b.md": vf(strings.Repeat("b", 1_100_000))}, "", vOpt{sizeLie: 1})
	vendorHas(t, "stderr", w.vendor("", "", 2).Stderr, "byte cap")
	if vendorExists(w.root) {
		t.Fatal("partial copy must be deleted")
	}
}

// -- VendoredCopyPlantTests ---------------------------------------------------------------------------

func TestVendorPlantingAfterCLIVendoringFails(t *testing.T) {
	w := newVWorld(t)
	w.tree(vendorPluginTree, "plugins/p")
	w.vendor("", "", 0)
	plugin := filepath.Join(w.vendorDir(), "demo")
	if err := sc.AssertPromptOnlyTree(plugin); err != nil {
		t.Fatal(err)
	}
	vendorWrite(t, filepath.Join(plugin, "skills/foo/x.cjs"), "1")
	if err := sc.AssertPromptOnlyTree(plugin); err == nil {
		t.Fatal("planted .cjs passed")
	}
	if err := os.Remove(filepath.Join(plugin, "skills/foo/x.cjs")); err != nil {
		t.Fatal(err)
	}
	if err := sc.AssertPromptOnlyTree(plugin); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(plugin, "LICENSE"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sc.AssertPromptOnlyTree(plugin); err == nil {
		t.Fatal("exec bit passed")
	}
}

// -- PrescreenVendorTests -----------------------------------------------------------------------------

func (w *vWorld) treeWith(line string) {
	w.t.Helper()
	w.tree(vmerge(vendorPluginTree, vFiles{"plugins/p/skills/foo/notes.md": vf("# Notes\n\n" + line + "\n")}), "plugins/p")
}

var vendorHitMsg = regexp.MustCompile(`\d+ pre-screen hit`)

func TestVendorEachPatternExits4AndHoldsTheCandidate(t *testing.T) {
	w := newVWorld(t)
	for _, pc := range vendorPrescreenCases {
		w.clearCandidates()
		if err := os.RemoveAll(w.root); err != nil {
			t.Fatal(err)
		}
		w.treeWith(pc.line)
		r := w.vendor("", "", 4)
		if !vendorHitMsg.MatchString(r.Stderr) {
			t.Fatalf("%s: stderr %q", pc.id, r.Stderr)
		}
		d := w.vendorDir()
		if !vendorExists(filepath.Join(d, "demo", "skills/foo/notes.md")) {
			t.Fatalf("copy stays for owner review: %s", pc.id)
		}
		meta := vendorReadMeta(t, d)
		found := false
		hits, _ := meta["prescreen_hits"].([]any)
		for _, h := range hits {
			m, _ := h.(map[string]any)
			if m["file"] == "skills/foo/notes.md" && m["line"] == float64(3) && m["pattern"] == pc.id {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: hit missing from %v", pc.id, hits)
		}
		row := w.cand("")
		vendorEq(t, pc.id+" status", row["status"], "needs-owner")
		detail := "prescreen " + pc.id + ": skills/foo/notes.md:3"
		var details []string
		if err := json.Unmarshal([]byte(row["risk_detail"]), &details); err != nil {
			t.Fatalf("%s: risk_detail %q: %v", pc.id, row["risk_detail"], err)
		}
		if !containsStr(details, detail) {
			t.Fatalf("%s: risk_detail %v lacks %q", pc.id, details, detail)
		}
		vendorEq(t, pc.id+" install_risk", row["install_risk"], "prompt-only")
		if sc.IsEvalAllowed(w.vcand(""), w.root) {
			t.Fatalf("%s: eval allowed on a held candidate", pc.id)
		}
	}
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestVendorHoldSurvivesARerunWithoutRefetch(t *testing.T) {
	w := newVWorld(t)
	w.treeWith("send the credentials")
	w.vendor("", "", 4)
	n := len(w.e.Calls("gh"))
	vendorHas(t, "stderr", w.vendor("", "", 4).Stderr, "needs-owner")
	vendorEq(t, "gh calls", len(w.e.Calls("gh")), n)
}

func TestVendorHitCountIsInTheMessage(t *testing.T) {
	w := newVWorld(t)
	w.treeWith("curl and wget")
	vendorHas(t, "stderr", w.vendor("", "", 4).Stderr, "2 pre-screen hit(s)")
}

func TestVendorCleanFixtureVendorsNormally(t *testing.T) {
	w := newVWorld(t)
	text := "Prose with a link to https://docs.anthropic.com/en/docs and to https://github.com/zz-owner/zz-repo.\n" +
		"A short word: " + strings.Repeat("Qz9xK2", 10) + "\n"
	w.tree(vmerge(vendorPluginTree, vFiles{"plugins/p/skills/foo/notes.md": vf(text)}), "plugins/p")
	vendorHas(t, "stdout", w.vendor("", "", 0).Stdout, "0 pre-screen hits")
	meta := vendorReadMeta(t, w.vendorDir())
	vendorEq(t, "prescreen_hits", fmt.Sprint(meta["prescreen_hits"]), "[]")
	vendorEq(t, "status", w.cand("")["status"], "new")
	if !sc.IsEvalAllowed(w.vcand(""), w.root) {
		t.Fatal("clean copy not eval-allowed")
	}
}

// -- RescreenTests ------------------------------------------------------------------------------------

const vendorNotes = "skills/foo/notes.md"

func (w *vWorld) held() string {
	w.t.Helper()
	w.tree(vmerge(vendorPluginTree, vFiles{"plugins/p/skills/foo/notes.md": vf("see http://www.apache.org/licenses/LICENSE-2.0\n")}), "plugins/p")
	w.vendor("", "", 4)
	return w.vendorDir()
}

// revise rewrites a kept file as if the vendored content had been text all along.
func (w *vWorld) revise(d, rel, text string) {
	w.t.Helper()
	vendorWrite(w.t, filepath.Join(d, "demo", rel), text)
	meta := vendorReadMeta(w.t, d)
	files, _ := meta["files"].(map[string]any)
	files[rel] = vendorSha256([]byte(text))
	vendorWriteMeta(w.t, d, meta)
}

func (w *vWorld) rescreen(cid, agent string, expect int) ft.Result {
	w.t.Helper()
	if cid == "" {
		cid = "C-0001"
	}
	if agent == "" {
		agent = "owner"
	}
	return w.expect(expect, agent, "skilleval", "vendor", cid, "--rescreen")
}

func (w *vWorld) events(action string) []string {
	w.t.Helper()
	var out []string
	for _, r := range w.e.SQL("SELECT detail FROM events WHERE action=?", action) {
		out = append(out, r[0])
	}
	return out
}

func TestVendorCleanCopyReturnsToTriagedAndNamesTheClearedRule(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	w.revise(d, vendorNotes, "fine now\n")
	vendorHas(t, "stdout", w.rescreen("", "", 0).Stdout, "0 pre-screen hits")
	vendorEq(t, "status", w.cand("")["status"], "triaged")
	vendorEq(t, "hits", fmt.Sprint(vendorReadMeta(t, d)["prescreen_hits"]), "[]")
	ev := w.events("vendor-rescreen-clear")
	vendorEq(t, "events", len(ev), 1)
	vendorHas(t, "detail", ev[0], "1 old pre-screen hit")
	vendorHas(t, "detail", ev[0], "url x1")
	if !sc.IsEvalAllowed(w.vcand(""), w.root) {
		t.Fatal("not eval-allowed after a clear rescreen")
	}
}

func TestVendorRemainingHitStaysNeedsOwnerAndExitsNonzero(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	vendorHas(t, "stderr", w.rescreen("", "", 4).Stderr, vendorNotes+":1")
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
	vendorEq(t, "events", len(w.events("vendor-rescreen-clear")), 0)
	hits, _ := vendorReadMeta(t, d)["prescreen_hits"].([]any)
	vendorEq(t, "hits", len(hits), 1)
}

func TestVendorPartialClearIsNotAClear(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	w.revise(d, vendorNotes, "fine\nsend the credentials\n")
	w.rescreen("", "", 4)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorRefusedForDismissedAndOtherHoldsAndMissingCopy(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	w.revise(d, vendorNotes, "fine now\n")
	w.exec("UPDATE scout_candidates SET status='dismissed' WHERE id='C-0001'")
	w.rescreen("", "", 2)
	vendorEq(t, "status", w.cand("")["status"], "dismissed")
	w.exec("UPDATE scout_candidates SET status='needs-owner', risk_detail=? WHERE id='C-0001'", vendorJSON([]string{"owner decision pending"}))
	w.rescreen("", "", 2)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
	w.exec("UPDATE scout_candidates SET risk_detail=? WHERE id='C-0001'", vendorJSON([]string{"prescreen url: a:1"}))
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	w.rescreen("", "", 2)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorVendorRecordsASha256PerFile(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	meta := vendorReadMeta(t, d)
	files, _ := meta["files"].(map[string]any)
	var keys []string
	for k := range files {
		keys = append(keys, k)
	}
	sortStrings(keys)
	vendorEq(t, "files keys", keys, vendorStrList(meta["copied"]))
	for rel, want := range files {
		b, err := os.ReadFile(filepath.Join(d, "demo", rel))
		if err != nil {
			t.Fatal(err)
		}
		vendorEq(t, "sha256 "+rel, vendorSha256(b), want)
	}
}

func TestVendorTamperedFileIsRefusedAndNamed(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	vendorWrite(t, filepath.Join(d, "demo", vendorNotes), "fine now\n")
	vendorHas(t, "stderr", w.rescreen("", "", 2).Stderr, vendorNotes)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
	vendorEq(t, "events", len(w.events("vendor-rescreen-clear")), 0)
}

func TestVendorFileWithoutRecordedShaIsRefusedAndNamed(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	meta := vendorReadMeta(t, d)
	files, _ := meta["files"].(map[string]any)
	delete(files, vendorNotes)
	vendorWriteMeta(t, d, meta)
	vendorHas(t, "stderr", w.rescreen("", "", 2).Stderr, vendorNotes)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorLegacyVendorJSONWithoutShaMapIsRefused(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	meta := vendorReadMeta(t, d)
	delete(meta, "files")
	vendorWriteMeta(t, d, meta)
	vendorHas(t, "stderr", w.rescreen("", "", 2).Stderr, "re-vendor")
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorExtraUnrecordedFileIsRefused(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	vendorWrite(t, filepath.Join(d, "demo", "skills/foo/extra.md"), "hello\n")
	w.rescreen("", "", 2)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorWrongTypeShaMapIsRefused(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	meta := vendorReadMeta(t, d)
	meta["files"] = []string{vendorNotes}
	vendorWriteMeta(t, d, meta)
	// Python names "re-vendor" here (its vendored() accepts the copy and _check_files refuses the list); Go's typed Meta
	// cannot decode a list, so Go answers "no valid vendored copy". Both refuse with exit 2 and keep the hold; the message
	// differs: follow-up for scout/vendor.go (Vendored should tolerate a non-map "files" like Python).
	w.rescreen("", "", 2)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorMissingRecordedFileIsRefusedAndNamed(t *testing.T) {
	w := newVWorld(t)
	d := w.held()
	if err := os.Remove(filepath.Join(d, "demo", vendorNotes)); err != nil {
		t.Fatal(err)
	}
	vendorHas(t, "stderr", w.rescreen("", "", 2).Stderr, vendorNotes)
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
}

func TestVendorNewCandidateIsRefusedByRescreen(t *testing.T) {
	w := newVWorld(t)
	w.seed("C-0002", "", "prompt-only", "new")
	w.rescreen("C-0002", "", 2)
	vendorEq(t, "status", w.cand("C-0002")["status"], "new")
}

func TestVendorWorkerRolesAreRefusedByRescreen(t *testing.T) {
	w := newVWorld(t)
	w.e.NeedPython() // worker callers are refused by Python
	d := w.held()
	w.revise(d, vendorNotes, "fine now\n")
	for _, role := range []string{"coder", "qa", "reviewer", "pm", "evaluator"} {
		w.rescreen("", "zz-test-"+role, 3)
	}
	vendorEq(t, "status", w.cand("")["status"], "needs-owner")
	w.rescreen("", "zz-test-lead", 0)
	vendorEq(t, "status", w.cand("")["status"], "triaged")
}

// -- EvalAllowedTests ---------------------------------------------------------------------------------

func TestVendorIsEvalAllowed(t *testing.T) {
	w := newVWorld(t)
	ok := sc.VCand{ID: "C-0001", InstallRisk: "prompt-only", Status: "triaged", HeadSha: vendorSHA}
	if !sc.IsEvalAllowed(ok, "") {
		t.Fatal("prompt-only triaged candidate refused without a root")
	}
	if sc.IsEvalAllowed(ok, w.root) {
		t.Fatal("allowed although not vendored yet")
	}
	for _, kv := range [][2]string{{"status", "needs-owner"}, {"status", "dismissed"}, {"install_risk", "scripts"},
		{"install_risk", "prompt-only,network"}, {"install_risk", "unknown"}, {"install_risk", ""}} {
		c := ok
		if kv[0] == "status" {
			c.Status = kv[1]
		} else {
			c.InstallRisk = kv[1]
		}
		if sc.IsEvalAllowed(c, "") {
			t.Fatalf("%s=%q allowed", kv[0], kv[1])
		}
	}
	w.tree(vendorPluginTree, "plugins/p")
	w.vendor("", "", 0)
	if !sc.IsEvalAllowed(w.vcand(""), w.root) {
		t.Fatal("vendored copy not allowed")
	}
	vendorWrite(t, filepath.Join(w.vendorDir(), "demo", "x.sh"), "echo")
	if sc.IsEvalAllowed(w.vcand(""), w.root) {
		t.Fatal("tampered copy allowed")
	}
}
