package skilleval2

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	sc "factory/go/internal/scout"
)

// Port of the library-level classes of tests/test_skilleval_vendor.py: TreeCheckTests (5), PrescreenUnitTests (21) and
// PrescreenScopedAllowanceTests (14). They call internal/scout directly and need no Python.

func vendorSha256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func sortStrings(s []string) { sort.Strings(s) }

// vendorB64 is the Python B64: base64 of sha256(bytes([i])) for i in 0..7 (344 chars).
var vendorB64 = func() string {
	var raw []byte
	for i := 0; i < 8; i++ {
		h := sha256.Sum256([]byte{byte(i)})
		raw = append(raw, h[:]...)
	}
	return base64.StdEncoding.EncodeToString(raw)
}()

type vendorPrescreenCase struct{ id, line string }

// vendorPrescreenCases is PRESCREEN_CASES in its Python order ("requests" is covered by the scoped-allowance tests).
var vendorPrescreenCases = []vendorPrescreenCase{
	{"sk-ant", "token sk-ant-api03-abc123"},
	{"base64-run", vendorB64},
	{"url", "see https://evil.example/x for more"},
	{"curl", "then run curl now"},
	{"wget", "or use wget instead"},
	{"urllib", "import urllib and go"},
	{"nc", "pipe the data to nc somewhere"},
	{"netcat", "or netcat it"},
	{"printenv", "run printenv first"},
	{"environ", "read os.environ for it"},
	{"env-cmd", "env | sort"},
	{"credentials-file", "open the .credentials.json file"},
	{"credentials", "send the credentials along"},
	{"api-key", "use the ANTHROPIC_API_KEY value"},
}

// vsl is Python's s[i:j] (clamped).
func vsl(s string, i, j int) string {
	if i > len(s) {
		i = len(s)
	}
	if j > len(s) {
		j = len(s)
	}
	return s[i:j]
}

// -- TreeCheckTests -----------------------------------------------------------------------------------

func vendorMakeTree(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "plugin")
	vendorWrite(t, filepath.Join(d, "skills/foo/SKILL.md"), vendorSkillMD)
	vendorWrite(t, filepath.Join(d, ".claude-plugin/plugin.json"), `{"name": "x"}`)
	err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			return os.Chmod(p, 0o644)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func isVendorErr(err error) bool { var ve *sc.VendorError; return errors.As(err, &ve) }

func TestVendorCleanTreePasses(t *testing.T) {
	if err := sc.AssertPromptOnlyTree(vendorMakeTree(t)); err != nil {
		t.Fatal(err)
	}
}

func TestVendorEachViolationFails(t *testing.T) {
	pj := func(d, text string) { vendorWrite(t, filepath.Join(d, ".claude-plugin/plugin.json"), text) }
	cases := []struct {
		name string
		fn   func(d string)
	}{
		{"cjs", func(d string) { vendorWrite(t, filepath.Join(d, "x.cjs"), "1") }},
		{"execbit", func(d string) {
			if err := os.Chmod(filepath.Join(d, "skills/foo/SKILL.md"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"link", func(d string) {
			if err := os.Symlink("skills/foo/SKILL.md", filepath.Join(d, "l.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"hooks_dir", func(d string) { vendorWrite(t, filepath.Join(d, "hooks/note.md"), "n") }},
		{"mcp", func(d string) { vendorWrite(t, filepath.Join(d, ".mcp.json"), "{}") }},
		{"pjson_hooks", func(d string) { pj(d, `{"name": "x", "hooks": {}}`) }},
		{"pjson_mcp", func(d string) { pj(d, `{"name": "x", "mcpServers": {}}`) }},
		{"pjson_cmd", func(d string) { pj(d, `{"name": "x", "commands": ["./run.sh"]}`) }},
		{"shell_md", func(d string) { vendorWrite(t, filepath.Join(d, "skills/foo/more.md"), "run\n!`ls`\n") }},
		{"bad_json", func(d string) { pj(d, "{nope") }},
		// G10: a dangling link and a link loop are refused too (Python raises VendorError for both).
		{"dangling_link", func(d string) {
			if err := os.Symlink("no-such-target", filepath.Join(d, "dangling.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"link_loop", func(d string) {
			if err := os.Symlink("l", filepath.Join(d, "l")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		d := vendorMakeTree(t)
		c.fn(d)
		if err := sc.AssertPromptOnlyTree(d); !isVendorErr(err) {
			t.Fatalf("%s: err = %v, want a VendorError", c.name, err)
		}
	}
}

func TestVendorMarkdownOverTheReadCapFails(t *testing.T) {
	d := vendorMakeTree(t)
	big := filepath.Join(d, "skills/foo/big.md")
	vendorWrite(t, big, strings.Repeat("x", 65536))
	if err := sc.AssertPromptOnlyTree(d); err != nil {
		t.Fatal(err)
	}
	vendorWrite(t, big, strings.Repeat("x", 65537))
	err := sc.AssertPromptOnlyTree(d)
	if !isVendorErr(err) {
		t.Fatalf("err = %v, want a VendorError", err)
	}
	vendorHas(t, "error", err.Error(), "read cap")
}

func TestVendorMissingDirFails(t *testing.T) {
	if err := sc.AssertPromptOnlyTree("/nonexistent-zz-dir"); !isVendorErr(err) {
		t.Fatalf("err = %v, want a VendorError", err)
	}
}

func TestVendorPlantingIntoAVendoredCopyFailsTheCheck(t *testing.T) {
	d := vendorMakeTree(t)
	if err := sc.AssertPromptOnlyTree(d); err != nil {
		t.Fatal(err)
	}
	vendorWrite(t, filepath.Join(d, "skills/foo/x.cjs"), "require('child_process')")
	if err := sc.AssertPromptOnlyTree(d); !isVendorErr(err) {
		t.Fatalf("err = %v, want a VendorError", err)
	}
}

// -- PrescreenUnitTests -------------------------------------------------------------------------------

func vendorScan(t testing.TB, text string) []sc.Hit {
	t.Helper()
	return vendorScanFiles(t, map[string]string{"notes.md": text})
}

func vendorScanFiles(t testing.TB, files map[string]string) []sc.Hit {
	t.Helper()
	d := t.TempDir()
	for name, text := range files {
		vendorWrite(t, filepath.Join(d, name), text)
	}
	hits, err := sc.Prescreen(d, vendorRepoName)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func vendorIDs(t testing.TB, text string) []string {
	t.Helper()
	return vendorIDSet(vendorScan(t, text))
}

func vendorIDSet(hits []sc.Hit) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, h := range hits {
		if !seen[h.Pattern] {
			seen[h.Pattern] = true
			out = append(out, h.Pattern)
		}
	}
	sort.Strings(out)
	return out
}

func vendorWantID(t testing.TB, what, id string, ids []string) {
	t.Helper()
	for _, x := range ids {
		if x == id {
			return
		}
	}
	t.Fatalf("%s: patterns %v lack %q", what, ids, id)
}

func vendorWantNone(t testing.TB, what string, ids []string) {
	t.Helper()
	if len(ids) != 0 {
		t.Fatalf("%s: unexpected hits %v", what, ids)
	}
}

func TestVendorPrescreenEveryPatternHitsWithItsIDAndLine(t *testing.T) {
	for _, pc := range vendorPrescreenCases {
		hits := vendorScan(t, "first\nsecond\n"+pc.line+"\nfourth\n")
		found := false
		for _, h := range hits {
			if h == (sc.Hit{File: "notes.md", Line: 3, Pattern: pc.id}) {
				found = true
			}
			if h.Line != 3 {
				t.Fatalf("%s: hit on line %d: %v", pc.id, h.Line, hits)
			}
		}
		if !found {
			t.Fatalf("%s: no hit in %v", pc.id, hits)
		}
	}
}

func TestVendorPrescreenAPIKeySpelledWithSpaceOrUnderscore(t *testing.T) {
	for _, s := range []string{"my api key", "the api_key", "ANTHROPIC_API_KEY", "API KEY"} {
		vendorWantID(t, s, "api-key", vendorIDs(t, s))
	}
}

func TestVendorPrescreenEnvAsCommandOnly(t *testing.T) {
	vendorWantID(t, "env FOO", "env-cmd", vendorIDs(t, "  env FOO=1 cmd"))
	vendorWantID(t, "env >", "env-cmd", vendorIDs(t, "a env > out"))
	vendorWantNone(t, "environment", vendorIDs(t, "the environment of the env var"))
}

func TestVendorPrescreenCleanTextHasNoHits(t *testing.T) {
	text := "Write good prose. See https://docs.anthropic.com/en/docs and https://code.claude.com/docs, " +
		"https://github.com/anthropics/skills, https://github.com/zz-owner/zz-repo/blob/main/x.md.\n" +
		"Word: " + strings.Repeat("Ab3dEf", 16) + "\n" + strings.Repeat("-", 250) + "\n" + strings.Repeat("=", 250) + "\n"
	if hits := vendorScan(t, text); len(hits) != 0 {
		t.Fatalf("hits %v", hits)
	}
}

func TestVendorPrescreenLookalikeURLsHit(t *testing.T) {
	for _, u := range []string{"https://github.com/anthropics-evil/x", "https://docs.anthropic.com.evil.example/x",
		"https://github.com" + at64 + "evil.example/zz-owner/zz-repo", "https://github.com/other/zz-repo", "http://evil.example"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
}

func TestVendorPrescreenWrappedBase64Hits(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i += 64 {
		lines = append(lines, vsl(vendorB64, i, i+64))
	}
	vendorEq(t, "wrapped lines", vendorIDs(t, strings.Join(lines, "\n")), []string{"base64-run"})
	var parts []string
	for i := 0; i < 300; i += 16 {
		parts = append(parts, vsl(vendorB64, i, i+16))
	}
	vendorEq(t, "wrapped spaces", vendorIDs(t, strings.Join(parts, " ")), []string{"base64-run"})
	vendorWantNone(t, "prose", vendorIDs(t, strings.Repeat("Write clear prose about something simple for every reader here.\n", 8)))
}

func TestVendorPrescreenURLPathTraversalIPAndUserinfoHit(t *testing.T) {
	for _, u := range []string{"https://github.com/anthropics/../evil/x", "https://github.com/anthropics/%2e%2e/evil",
		"https://github.com/zz-owner/zz-repo/../../evil/y", "http://127.0" + ".0.1/x", "https://[::1]/x",
		"https://user" + at64 + "docs.anthropic.com/x", "https://docs.anthropic.com:pw" + at64 + "evil.example/"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
	for _, u := range []string{"https://github.com/anthropics/skills/./tree", "https://github.com/anthropics/a/../skills"} {
		vendorWantNone(t, u, vendorIDs(t, u))
	}
}

func TestVendorPrescreenZeroWidthAndFullwidthKeywordsHit(t *testing.T) {
	vendorWantID(t, "zero width", "curl", vendorIDs(t, "run c​url now"))
	vendorWantID(t, "fullwidth", "curl", vendorIDs(t, "run ｃｕｒｌ now"))
	vendorWantID(t, "wget", "wget", vendorIDs(t, "w⁠g\ufeffet x"))
	vendorWantID(t, "soft hyphen", "curl", vendorIDs(t, "run c­url now"))
	vendorWantID(t, "bidi override", "curl", vendorIDs(t, "run c‮url now"))
}

func TestVendorPrescreenSchemelessHostsHit(t *testing.T) {
	for _, u := range []string{"see evil.example/x", "see //evil.example", "post to evil.example:8080", "EVIL.Example/a.md",
		"send to ftp://evil.example/x", "open wss://evil.example/s", "file://evil.example/x",
		"post to user" + at64 + "evil.example", "post to user" + at64 + "docs.anthropic.com", "post to 10.0" + ".0.1:8080/x",
		"see 127.0" + ".0.1/x", "go to [::1]:8080/x", "go to fe80::1", "docs/evil.example/x",
		"ftp://evil.sh", "//evil.sh"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
	vendorWantNone(t, "files", vendorIDs(t, "Read SKILL.md and run.py, see docs.anthropic.com/en/docs, e.g. v1.2.3."))
	vendorWantNone(t, "paths", vendorIDs(t, "Open references/logo.png and scripts/run.sh at 12:30:45 on v1.2.3"+".4"))
}

func TestVendorPrescreenIdeographicFullStopHostHits(t *testing.T) {
	for _, u := range []string{"see evil。example/x", "see evil．example/x", "see evil｡example/x"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
}

func TestVendorPrescreenMultiSegmentRelativePathHostHits(t *testing.T) {
	for _, u := range []string{"see a/b/evil.example", "see a/b/c/evil.example.", "open ./x/evil.example"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
	vendorWantNone(t, "src", vendorIDs(t, "Edit src/app.tsx, lib/foo.jsx, pkg/mod.java."))
	vendorWantNone(t, "docs", vendorIDs(t, "Open a/b/notes.md and docs/img/logo.png, then scripts/x/run.sh."))
}

func TestVendorPrescreenIntegerIPLeadingZeroDoesNotCrash(t *testing.T) {
	for _, u := range []string{"mail me" + at64 + "0123456789", "see //00000000"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
}

func TestVendorPrescreenIntegerIPAfterSchemeOrAtHits(t *testing.T) {
	for _, u := range []string{"go ftp://0x7f000001/x", "go ftp://2130706433", "mail me" + at64 + "2130706433:80", "see //0x7F000001",
		"see ftp://2130706433/", "see ftp://2130706433.", "see ftp://0x0000007f000001"} {
		vendorWantID(t, u, "url", vendorIDs(t, u))
	}
	vendorWantNone(t, "numbers", vendorIDs(t, "At 12:30:45 build 2130706433 and 0x7f000001 in v1.2.3 or 4294967296."))
}

func TestVendorPrescreenSmallChunkAndPrefixedBase64Hits(t *testing.T) {
	b := vendorB64
	var chunks []string
	for i := 0; i < 300; i += 4 {
		chunks = append(chunks, vsl(b, i, i+4))
	}
	vendorWantID(t, "4-char chunks", "base64-run", vendorIDs(t, strings.Join(chunks, " ")))
	join := func(sep string, n, step int, f func(i int) string) string {
		var out []string
		for i := 0; i < n; i += step {
			out = append(out, f(i))
		}
		return strings.Join(out, sep)
	}
	vendorWantID(t, "> prefix", "base64-run", vendorIDs(t, join("\n", 300, 40, func(i int) string { return "> " + vsl(b, i, i+40) })))
	vendorWantID(t, "quoted +", "base64-run", vendorIDs(t, join(" +\n", 300, 5, func(i int) string { return `"` + vsl(b, i, i+5) + `"` })))
	vendorWantID(t, "# prefix", "base64-run", vendorIDs(t, join("\n", 300, 6, func(i int) string { return "# " + vsl(b, i, i+6) })))
	var one []string
	for _, r := range vsl(b, 0, 300) {
		one = append(one, string(r))
	}
	vendorWantID(t, "1-char chunks", "base64-run", vendorIDs(t, strings.Join(one, " ")))
	var numbered []string
	for i := 0; i < 6; i++ {
		numbered = append(numbered, fmt.Sprintf("%d. %s", i, vsl(b, i*50, i*50+50)))
	}
	vendorWantID(t, "numbered", "base64-run", vendorIDs(t, strings.Join(numbered, "\n")))
	vendorWantID(t, "data: ... \\", "base64-run", vendorIDs(t, join("\n", 300, 50, func(i int) string { return "data: " + vsl(b, i, i+50) + " \\" })))
	vendorWantNone(t, "quoted prose", vendorIDs(t, "> "+strings.Repeat("Write clear prose about it. ", 20)))
	var steps []string
	for i := 1; i < 40; i++ {
		steps = append(steps, fmt.Sprintf("> step %d of the plan is to be done", i))
	}
	vendorWantNone(t, "steps", vendorIDs(t, strings.Join(steps, "\n")))
}

// TestVendorPrescreenVariableWidthChunksHit: Python draws the chunk widths from random.Random(seed); Go cannot
// reproduce that stream, so it draws from a seeded math/rand/v2 PCG (same ranges 3-7 and 20-60, seeds 0-4).
func TestVendorPrescreenVariableWidthChunksHit(t *testing.T) {
	for seed := uint64(0); seed < 5; seed++ {
		rnd := rand.New(rand.NewPCG(seed, seed))
		var chunks []string
		for i := 0; i < 300; {
			w := 3 + rnd.IntN(5)
			chunks = append(chunks, vsl(vendorB64, i, i+w))
			i += w
		}
		var lines [][]string
		for j := 0; j < len(chunks); j += 8 {
			lines = append(lines, chunks[j:min(j+8, len(chunks))])
		}
		var plain, prefixed []string
		for _, l := range lines {
			plain = append(plain, strings.Join(l, " "))
			prefixed = append(prefixed, "> "+strings.Join(l, " "))
		}
		for _, text := range []string{strings.Join(chunks, " "), strings.Join(plain, "\n"), strings.Join(prefixed, "\n")} {
			vendorWantID(t, fmt.Sprintf("seed %d %.60q", seed, text), "base64-run", vendorIDs(t, text))
		}
		var wide []string
		for i := 0; i < 300; {
			w := 20 + rnd.IntN(41)
			wide = append(wide, vsl(vendorB64, i, i+w))
			i += w
		}
		for _, prefix := range []string{"> ", "# "} {
			var ls []string
			for _, c := range wide {
				ls = append(ls, prefix+c)
			}
			vendorWantID(t, fmt.Sprintf("seed %d prefix %q", seed, prefix), "base64-run", vendorIDs(t, strings.Join(ls, "\n")))
		}
	}
}

func TestVendorPrescreenSmallChunkBase64SurvivesAdjacentProse(t *testing.T) {
	prose := strings.Join([]string{"This skill helps you write better commit messages for your team and project.",
		"This skill helps you write better commit messages for your team and project.",
		"This skill helps you write better commit messages for your team and project."}, "\n")
	var bl, sm []string
	for i := 0; i < 320; i += 40 {
		bl = append(bl, "> "+vsl(vendorB64, i, i+40))
	}
	for i := 0; i < 320; i += 4 {
		sm = append(sm, vsl(vendorB64, i, i+4))
	}
	blob, small := strings.Join(bl, "\n"), strings.Join(sm, " ")
	for _, text := range []string{prose + "\n" + blob, blob + "\n" + prose, prose + "\n\n" + blob + "\n\n" + prose, prose + "\n" + small, small + "\n" + prose} {
		vendorWantID(t, fmt.Sprintf("%.60q", text), "base64-run", vendorIDs(t, text))
	}
}

func TestVendorPrescreenTimestampVersionAndCodeListsAreNotBase64(t *testing.T) {
	var history, table, code, colors []string
	for i := 0; i < 20; i++ {
		history = append(history, fmt.Sprintf("- 2026-10-%02dT22:%02d:00+00:00 agent-%d updated the task", i%28+1, i, i))
		table = append(table, fmt.Sprintf("| pkg%d | v1.%d.%d | 2026-01-0%d |", i, i, i*3, i%9+1))
		code = append(code, fmt.Sprintf("GH_TIMEOUT_%d = %d  # seconds, see HTTP/1.1 and CONNECT %d", i, i*30, i))
		colors = append(colors, fmt.Sprintf("%d,Name %d,#2563EB,#FFFFFF,#3B82F6,#000000,#EA580C,#F8FAFC,#1E293B", i, i))
	}
	for _, l := range [][]string{history, table, code, colors} {
		text := strings.Join(l, "\n")
		vendorWantNone(t, fmt.Sprintf("%.50q", text), vendorIDs(t, text))
	}
}

// TestVendorPrescreenTrackedRepoFilesHaveNoSmallChunkBase64: Python runs the private _small_chunk_blobs over
// `git ls-files`. Go has no access to that detector and the merge-gate tree has no .git, so this walks the repo for the
// same extensions and requires that the exported pre-screen reports no base64-run on any of them.
func TestVendorPrescreenTrackedRepoFilesHaveNoSmallChunkBase64(t *testing.T) {
	mod, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(mod)
	exts := map[string]bool{".md": true, ".py": true, ".csv": true, ".json": true, ".jsonl": true, ".html": true}
	var bad []string
	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if n := e.Name(); n == ".git" || n == "node_modules" || n == "__pycache__" || n == "worktrees" || (n == "go" && filepath.Dir(p) == root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() || !exts[filepath.Ext(p)] {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		for _, h := range sc.ScreenText(rel, string(b), vendorRepoName) {
			if h.Pattern == "base64-run" {
				bad = append(bad, fmt.Sprintf("%s:%d", rel, h.Line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("base64-run hits in repo files: %v", bad)
	}
}

func TestVendorPrescreenZipDomainIsNotAFileName(t *testing.T) {
	vendorWantID(t, "attacker.zip", "url", vendorIDs(t, "see attacker.zip"))
}

func TestVendorPrescreenLongSingleRunScansInLinearTime(t *testing.T) {
	for _, text := range []string{strings.Repeat("A", 200_000), strings.Repeat("A", 100_000) + " " + strings.Repeat("B", 100_000),
		strings.Repeat("a.", 100_000), strings.Repeat("ab ", 70_000), strings.Repeat("a/", 100_000), strings.Repeat("@12345678 ", 20_000)} {
		start := time.Now()
		vendorScan(t, text)
		// Python allows 1 s; a quadratic scan of these 200 kB inputs takes minutes, so 5 s keeps the check on a loaded runner.
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("%.3q...: scan took %v", text, el)
		}
	}
}

func TestVendorPrescreenBase64Threshold(t *testing.T) {
	pats := func(s string) []string {
		out := []string{}
		for _, h := range vendorScan(t, s) {
			out = append(out, h.Pattern)
		}
		return out
	}
	vendorEq(t, "199 chars", pats(vsl(vendorB64, 0, 199)), []string{})
	vendorEq(t, "200 chars", pats(vsl(vendorB64, 0, 200)), []string{"base64-run"})
}

// -- PrescreenScopedAllowanceTests --------------------------------------------------------------------

var at64 = string(rune(64))

func vat(local, host string) string { return local + at64 + host }

const vendorApache = "http://www.apache.org/licenses/LICENSE-2.0"

// vOrd is an ordered JSON object for the plugin.json fixtures (Python dict order).
type vOrd [][2]any

func vendorPyJSON(v any, ind string) string {
	switch x := v.(type) {
	case string:
		return vendorPyString(x)
	case vOrd:
		if len(x) == 0 {
			return "{}"
		}
		var parts []string
		for _, kv := range x {
			parts = append(parts, ind+"  "+vendorPyString(kv[0].(string))+": "+vendorPyJSON(kv[1], ind+"  "))
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n" + ind + "}"
	case []any:
		if len(x) == 0 {
			return "[]"
		}
		var parts []string
		for _, e := range x {
			parts = append(parts, ind+"  "+vendorPyJSON(e, ind+"  "))
		}
		return "[\n" + strings.Join(parts, ",\n") + "\n" + ind + "]"
	}
	panic(fmt.Sprintf("vendorPyJSON: %T", v))
}

// vendorPyString is json.dumps(ensure_ascii=True) of a string.
func vendorPyString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case r == 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// vpj is json.dumps({"name": "demo", **kw}, indent=2) + "\n".
func vpj(kw ...[2]any) string {
	return vendorPyJSON(append(vOrd{{"name", "demo"}}, kw...), "") + "\n"
}

func vurl(t testing.TB, what string, files map[string]string) {
	t.Helper()
	vendorWantID(t, what, "url", vendorIDSet(vendorScanFiles(t, files)))
}

func vclean(t testing.TB, what string, files map[string]string) {
	t.Helper()
	if hits := vendorScanFiles(t, files); len(hits) != 0 {
		t.Fatalf("%s: unexpected hits %v", what, hits)
	}
}

func TestVendorScopedC0078FixtureLinesAreClean(t *testing.T) {
	vclean(t, "c0078", map[string]string{
		".claude-plugin/plugin.json": vpj([2]any{"author", vOrd{{"name", "Anthropic"}, {"email", vat("support", "anthropic.com")}}}),
		"LICENSE":                    "Apache License\n   " + vendorApache[:strings.LastIndex(vendorApache, "/")] + "/\n   " + vendorApache + "\n",
		"agents/code-simplifier.md":  "Work without requiring explicit requests. Your goal is clarity.\n",
	})
}

func TestVendorScopedSupportFieldAndWwwFormAndNoticeFileAreClean(t *testing.T) {
	vclean(t, "support www", map[string]string{"plugin.json": vpj([2]any{"support", vat("help", "www.anthropic.com")})})
	vclean(t, "support obj", map[string]string{"plugin.json": vpj([2]any{"support", vOrd{{"email", vat("a", "docs.claude.com")}}})})
	vclean(t, "NOTICE.txt", map[string]string{"NOTICE.txt": "see " + vendorApache + "\n"})
}

func TestVendorScopedApacheURLOutsideLicenseStillHits(t *testing.T) {
	for _, name := range []string{"run.py", "notes.md", "plugin.json", "LICENSEX/other.py"} {
		vurl(t, name, map[string]string{name: "see " + vendorApache + "\n"})
	}
}

func TestVendorScopedLicenseLookalikeNamesStillHit(t *testing.T) {
	for _, name := range []string{"LICENSE.py", "LICENSE-x.sh", "LICENSEfoo", "NOTICES"} {
		vurl(t, name, map[string]string{name: "see " + vendorApache + "\n"})
	}
	for _, name := range []string{"LICENSE-APACHE", "license.txt", "NOTICE.md"} {
		vclean(t, name, map[string]string{name: "see " + vendorApache + "\n"})
	}
}

func TestVendorScopedOtherURLsInLicenseStillHit(t *testing.T) {
	for _, u := range []string{"http://www.apache.org/licenses/evil", "http://evil.example/licenses/LICENSE-2.0",
		"http://www.apache.org/licenses/LICENSE-2.0/x", "http://www.apache.org.evil.example/licenses/"} {
		vurl(t, u, map[string]string{"LICENSE": u + "\n"})
	}
}

func TestVendorScopedDocEmailElsewhereStillHits(t *testing.T) {
	addr := vat("support", "anthropic.com")
	for name, text := range map[string]string{
		"run.py": "MAIL = \"" + addr + "\"\n", "notes.md": "mail " + addr + "\n",
		"plugin.json": vpj([2]any{"homepage", addr}), "other.json": vpj([2]any{"author", vOrd{{"email", addr}}}),
		"x/plugin.json.txt": vpj([2]any{"author", vOrd{{"email", addr}}}),
	} {
		vurl(t, name, map[string]string{name: text})
	}
}

func TestVendorScopedPluginJSONEmailInOtherKeyOrTwiceHits(t *testing.T) {
	addr := vat("support", "anthropic.com")
	for i, kw := range [][][2]any{
		{{"contributors", []any{vOrd{{"email", addr}}}}},
		{{"author", vOrd{{"email", addr}}}, {"homepage", addr}},
		{{"author", vOrd{{"email", addr}, {"name", addr}}}},
		{{"author", addr}},
	} {
		vurl(t, fmt.Sprint("case ", i), map[string]string{"plugin.json": vpj(kw...)})
	}
}

func TestVendorScopedPluginJSONEscapedAuthorEmailCannotMoveAllowance(t *testing.T) {
	// author.email is written with a JSON escape, so the one literal address sits under mcpServers.x.email
	text := `{"author": {"email": "` + vat("support", `anthropic.com`) + `"},` + "\n" +
		` "mcpServers": {"x": {` + "\n  \"email\": \"" + vat("support", "anthropic.com") + "\"\n}}}\n"
	vurl(t, "escaped", map[string]string{"plugin.json": text})
}

func TestVendorScopedPluginJSONFullwidthQuoteCannotMoveAllowance(t *testing.T) {
	// U+FF02 inside a raw string folds (NFKC) to a real quote. Parsed folded, the address sits at author.email on
	// its own (exempt-looking) line; raw, it sits at mcpServers.email
	q, addr := "＂", vat("support", "anthropic.com")
	text := `{"mcpServers": {"a": "` + q + `}, ` + q + `author` + q + `: {` + q + `b` + q + `: ` + q + "\",\n" +
		"  \"email\": \"" + addr + "\"\n}}\n"
	vurl(t, "fullwidth quote", map[string]string{"plugin.json": text})
}

func TestVendorScopedPluginJSONAnyNonASCIIByteGivesNoAllowance(t *testing.T) {
	author := vpj([2]any{"author", vOrd{{"name", "Zoë"}, {"email", vat("support", "anthropic.com")}}})
	vurl(t, "author name", map[string]string{"plugin.json": author})
	text := strings.Replace(vpj([2]any{"author", vOrd{{"email", vat("support", "anthropic.com")}}}), `"demo"`, "\"démo\"", 1)
	vurl(t, "plugin name", map[string]string{"plugin.json": text})
}

func TestVendorScopedPluginJSONAuthorEmailOtherDomainHits(t *testing.T) {
	for _, h := range []string{"evil.example", "anthropic.com.evil.example", "notanthropic.com", "sub.anthropic.com"} {
		addr := vat("me", h)
		vurl(t, addr, map[string]string{"plugin.json": vpj([2]any{"author", vOrd{{"email", addr}}})})
	}
}

func TestVendorScopedUserinfoURLStillHits(t *testing.T) {
	for _, name := range []string{"plugin.json", "notes.md", "LICENSE"} {
		vurl(t, name, map[string]string{name: "see https://" + vat("user", "anthropic.com") + "/x\n"})
	}
}

func TestVendorScopedRequestsHitsOutsideProse(t *testing.T) {
	for _, c := range [][2]string{{"a.py", "requests.get(u)\n"}, {"b.py", "import requests\n# requests.\n"},
		{"c.py", "from requests import get\nrequests.\n"}, {"d.py", "x requests. y\n"},
		{"e.json", "{\"a\": \"requests.\"}\n"}, {"f.sh", "requests. x\n"}, {"README", "requests. x\n"}} {
		vendorWantID(t, c[0], "requests", vendorIDSet(vendorScanFiles(t, map[string]string{c[0]: c[1]})))
	}
}

func TestVendorScopedRequestsInProseIsCleanButOtherRulesStillFire(t *testing.T) {
	for _, name := range []string{"a.md", "A.MD", "notes.txt"} {
		vclean(t, name, map[string]string{name: "without explicit requests. Your goal\n"})
	}
	vendorEq(t, "ids", vendorIDSet(vendorScanFiles(t, map[string]string{"a.md": "requests. and curl\n"})), []string{"curl"})
}
