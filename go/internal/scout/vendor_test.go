package scout

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type vendorScenario struct {
	Name      string                    `json:"name"`
	Cand      map[string]string         `json:"cand"`
	Responses map[string]map[string]any `json:"responses"`
	OK        *struct {
		Dir    string `json:"dir"`
		Plugin string `json:"plugin"`
		Files  map[string]struct {
			Sha256 string `json:"sha256"`
			Mode   string `json:"mode"`
		} `json:"files"`
		VendorJSON   string  `json:"vendor_json"`
		Reused       bool    `json:"reused"`
		Reclassified *string `json:"reclassified"`
		SecondReused bool    `json:"second_reused"`
		SecondPlugin string  `json:"second_plugin"`
		PartialLeft  bool    `json:"partial_left"`
	} `json:"ok"`
	Err *struct {
		Msg   string  `json:"msg"`
		Code  int     `json:"code"`
		Fresh *string `json:"fresh"`
	} `json:"err"`
	RootExistsAfter bool `json:"root_exists_after"`
}

type vendorGolden struct {
	Scenarios []vendorScenario `json:"scenarios"`
	Local     struct {
		ContentSha   string            `json:"content_sha"`
		SrcFiles     map[string]string `json:"src_files"`
		Cand         map[string]string `json:"cand"`
		Dir          string            `json:"dir"`
		VendorJSON   string            `json:"vendor_json"`
		ReusedSecond bool              `json:"reused_second"`
		Cleared      string            `json:"cleared"`
	} `json:"local"`
}

func vcand(m map[string]string) VCand {
	return VCand{ID: m["id"], Repo: m["repo"], Path: m["path"], HeadSha: m["head_sha"], Licence: m["licence"], InstallRisk: m["install_risk"], Status: m["status"]}
}

func serveResponses(resp map[string]map[string]any) *Gh {
	return &Gh{Timeout: time.Second, Run: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
		r, ok := resp[args[0]]
		if !ok {
			return nil, []byte("Not Found"), 1, nil
		}
		b, _ := json.Marshal(r)
		return b, nil, 0, nil
	}}
}

func loadVendorGolden(t *testing.T) vendorGolden {
	var g vendorGolden
	readJSON(t, "vendor_golden.json", &g)
	if len(g.Scenarios) < 20 {
		t.Fatal("vendor goldens missing")
	}
	return g
}

// TestVendorGolden: vendoring writes the same files, VENDOR.json bytes and refusals as scout/vendor.py.
func TestVendorGolden(t *testing.T) {
	g := loadVendorGolden(t)
	for _, sc := range g.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			gh := serveResponses(sc.Responses)
			res, err := Vendor(gh, vcand(sc.Cand), root)
			if sc.Err != nil {
				var ve *VendorError
				if err == nil || !asVE(err, &ve) {
					t.Fatalf("want a VendorError %q, got %v", sc.Err.Msg, err)
				}
				if ve.Msg != sc.Err.Msg || ve.Code != sc.Err.Code {
					t.Errorf("error:\n got %q (%d)\nwant %q (%d)", ve.Msg, ve.Code, sc.Err.Msg, sc.Err.Code)
				}
				if (ve.Fresh == nil) != (sc.Err.Fresh == nil) || (ve.Fresh != nil && ve.Fresh.Risk != *sc.Err.Fresh) {
					t.Errorf("fresh %v want %v", ve.Fresh, sc.Err.Fresh)
				}
				if _, serr := os.Stat(root); (serr == nil) != sc.RootExistsAfter {
					t.Errorf("root exists after refusal = %v, want %v: a refusal must leave nothing behind", serr == nil, sc.RootExistsAfter)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := sc.OK
			if rel, _ := filepath.Rel(root, res.Dir); rel != want.Dir || res.Plugin != want.Plugin || res.Reused != want.Reused {
				t.Errorf("dir %q plugin %q reused %v; want %q %q %v", rel, res.Plugin, res.Reused, want.Dir, want.Plugin, want.Reused)
			}
			if (res.Reclassified == nil) != (want.Reclassified == nil) {
				t.Errorf("reclassified %v want %v", res.Reclassified, want.Reclassified)
			}
			got := map[string]string{}
			filepath.WalkDir(res.Dir, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(res.Dir, p)
					fi, _ := os.Stat(p)
					sum, _ := sha256File(p)
					got[rel] = sum + " " + "0o" + strings.TrimPrefix(strings.TrimPrefix(fi.Mode().Perm().String(), "-"), "")
				}
				return nil
			})
			for rel, w := range want.Files {
				p := filepath.Join(res.Dir, rel)
				sum, err := sha256File(p)
				if err != nil || sum != w.Sha256 {
					t.Errorf("file %s: sha %s err %v, want %s", rel, sum, err, w.Sha256)
				}
				if fi, err := os.Stat(p); err == nil && w.Mode != "0o644" {
					t.Errorf("python wrote %s with %s; the port expects 0o644", rel, w.Mode)
				} else if err == nil && fi.Mode().Perm() != 0o644 {
					t.Errorf("file %s mode %v, want 0644", rel, fi.Mode().Perm())
				}
			}
			if len(got) != len(want.Files) {
				t.Errorf("%d files written, want %d", len(got), len(want.Files))
			}
			raw, _ := os.ReadFile(filepath.Join(res.Dir, "VENDOR.json"))
			if string(raw) != want.VendorJSON {
				t.Errorf("VENDOR.json differs:\n got %s\nwant %s", raw, want.VendorJSON)
			}
			again, err := Vendor(gh, vcand(sc.Cand), root)
			if err != nil || !again.Reused || again.Plugin != want.SecondPlugin || !want.SecondReused {
				t.Errorf("second vendor: %+v %v", again, err)
			}
			if _, serr := os.Stat(res.Dir + ".partial"); serr == nil || want.PartialLeft {
				t.Error("staging directory left behind")
			}
		})
	}
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func asVE(err error, out **VendorError) bool {
	ve, ok := err.(*VendorError)
	*out = ve
	return ok
}

// TestVendorRefusesEscape: tree entries with an absolute path, "../", a backslash or an empty segment refuse the
// whole vendoring; a symlink entry is refused too; nothing is written outside the target dir.
func TestVendorRefusesEscape(t *testing.T) {
	skill := "---\nname: x\ndescription: d\n---\nbody\n"
	type entry struct {
		path, mode string
	}
	cases := map[string]entry{
		"absolute path":   {"/etc/evil.md", "100644"},
		"dot-dot":         {"../evil.md", "100644"},
		"nested dot-dot":  {"a/../../evil.md", "100644"},
		"backslash":       {`a\..\evil.md`, "100644"},
		"empty segment":   {"a//evil.md", "100644"},
		"symlink outside": {"link.md", "120000"},
		"submodule":       {"mod", "160000"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			outer := t.TempDir()
			root := filepath.Join(outer, "root")
			sentinel := filepath.Join(outer, "sentinel")
			if err := os.Mkdir(sentinel, 0o755); err != nil {
				t.Fatal(err)
			}
			resp := map[string]map[string]any{}
			body := skill
			bsha := gitBlobSha([]byte(body))
			sha := strings.Repeat("a", 40)
			evilSha := gitBlobSha([]byte("evil"))
			typ := "blob"
			if c.mode == "160000" {
				typ = "commit"
			}
			resp["repos/o/r/git/trees/"+sha+"?recursive=1"] = map[string]any{"truncated": false, "tree": []any{
				map[string]any{"path": "SKILL.md", "mode": "100644", "type": "blob", "sha": bsha, "size": len(body)},
				map[string]any{"path": c.path, "mode": c.mode, "type": typ, "sha": evilSha, "size": 4},
			}}
			enc := func(s string) string { return strings.TrimSpace(base64Std(s)) }
			resp["repos/o/r/git/blobs/"+bsha] = map[string]any{"encoding": "base64", "content": enc(body)}
			resp["repos/o/r/git/blobs/"+evilSha] = map[string]any{"encoding": "base64", "content": enc("evil")}
			_, err := Vendor(serveResponses(resp), VCand{ID: "C-0001", Repo: "o/r", HeadSha: sha, InstallRisk: "prompt-only", Status: "new"}, root)
			var ve *VendorError
			if err == nil || !asVE(err, &ve) || ve.Code != ExitRefused {
				t.Fatalf("want a refusal (exit 2), got %v", err)
			}
			if _, serr := os.Stat(root); serr == nil {
				t.Errorf("refused vendoring left %s behind", root)
			}
			entries, _ := os.ReadDir(sentinel)
			if len(entries) != 0 {
				t.Errorf("something was written beside the target: %v", entries)
			}
			if _, serr := os.Stat(filepath.Join(outer, "evil.md")); serr == nil {
				t.Error("a file escaped the target directory")
			}
		})
	}
}

// writeInside must not follow a symlink planted below the target: the parent is resolved and must stay inside.
func TestWriteInsideRefusesSymlinkedParent(t *testing.T) {
	outer := t.TempDir()
	base := filepath.Join(outer, "base")
	outside := filepath.Join(outer, "outside")
	for _, d := range []string{base, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := writeInside(base, "link/evil.md", []byte("x")); err == nil {
		t.Fatal("wrote through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.md")); err == nil {
		t.Fatal("file landed outside the base")
	}
	// a symlink in the final position is refused by O_NOFOLLOW
	if err := os.Symlink(filepath.Join(outside, "target.md"), filepath.Join(base, "final.md")); err != nil {
		t.Fatal(err)
	}
	if err := writeInside(base, "final.md", []byte("x")); err == nil {
		t.Fatal("wrote through a symlinked file")
	}
	if err := writeInside(base, "ok/inner.md", []byte("fine")); err != nil {
		t.Fatalf("a plain path must work: %v", err)
	}
	for _, bad := range []string{"../x.md", "/abs.md", "a/../../x.md", ""} {
		if err := writeInside(base, bad, []byte("x")); err == nil {
			t.Errorf("writeInside accepted %q", bad)
		}
	}
}

func TestLocalVendorAndContentShaGolden(t *testing.T) {
	g := loadVendorGolden(t).Local
	src := filepath.Join(t.TempDir(), "src")
	for rel, text := range g.SrcFiles {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sha, err := ContentSha(src)
	if err != nil || sha != g.ContentSha {
		t.Fatalf("ContentSha %s %v, want %s (component-wise path order)", sha, err, g.ContentSha)
	}
	root := filepath.Join(t.TempDir(), "root")
	res, err := VendorLocal(vcand(g.Cand), root, src, "plug")
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := filepath.Rel(root, res.Dir); rel != g.Dir {
		t.Errorf("dir %s want %s", rel, g.Dir)
	}
	raw, _ := os.ReadFile(filepath.Join(res.Dir, "VENDOR.json"))
	if string(raw) != g.VendorJSON {
		t.Errorf("VENDOR.json:\n got %s\nwant %s", raw, g.VendorJSON)
	}
	if again, err := VendorLocal(vcand(g.Cand), root, src, "plug"); err != nil || !again.Reused || !g.ReusedSecond {
		t.Errorf("second: %+v %v", again, err)
	}
	if err := ClearHits(res.Dir); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(res.Dir, "VENDOR.json"))
	if string(raw) != g.Cleared {
		t.Errorf("ClearHits:\n got %s\nwant %s", raw, g.Cleared)
	}
}

func TestVendoredDetectsTamperingAndRescreen(t *testing.T) {
	g := loadVendorGolden(t)
	var sc vendorScenario
	for _, s := range g.Scenarios {
		if s.Name == "hits" {
			sc = s
		}
	}
	root := filepath.Join(t.TempDir(), "root")
	cand := vcand(sc.Cand)
	res, err := Vendor(serveResponses(sc.Responses), cand, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Meta.PrescreenHits) == 0 {
		t.Fatal("fixture should have pre-screen hits")
	}
	if IsEvalAllowed(cand, root) {
		t.Error("a copy with hits must not be eval-allowed")
	}
	meta, hits, _, err := Rescreen(root, cand)
	if err != nil || len(hits) != len(meta.PrescreenHits) {
		t.Fatalf("Rescreen: %v %d hits vs %d", err, len(hits), len(meta.PrescreenHits))
	}
	if err := ClearHits(res.Dir); err != nil {
		t.Fatal(err)
	}
	if !IsEvalAllowed(cand, root) {
		t.Error("after ClearHits the copy should be eval-allowed")
	}
	if IsEvalAllowed(VCand{ID: cand.ID, HeadSha: cand.HeadSha, InstallRisk: "prompt-only", Status: "needs-owner"}, root) ||
		IsEvalAllowed(VCand{InstallRisk: "scripts"}, "") || !IsEvalAllowed(VCand{InstallRisk: "prompt-only", Status: "new"}, "") {
		t.Error("IsEvalAllowed gate wrong")
	}
	// tamper with a kept file: the copy is no longer valid (its sha no longer matches)
	skill := filepath.Join(res.Dir, res.Plugin, "skills", "hits", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("expected %s: %v", skill, err)
	}
	if err := os.WriteFile(skill, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Rescreen(root, cand); err == nil || !strings.Contains(err.Error(), "differs from the sha256 recorded at vendor time") {
		t.Errorf("tampered copy: %v", err)
	}
	// an executable bit makes the copy invalid for the eval gate
	if err := os.Chmod(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := Vendored(root, cand.ID, cand.HeadSha); ok {
		t.Error("a copy with an executable file is still reported valid")
	}
}

func TestTreeProblems(t *testing.T) {
	d := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(d, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
	}
	write("ok/SKILL.md", "---\nname: x\n---\nfine\n", 0o644)
	write("exec.md", "x", 0o755)
	write("hooks/a.md", "x", 0o644)
	write("run.py", "x", 0o644)
	write("sh.md", "run !`ls` now\n", 0o644)
	write("big.md", strings.Repeat("a", ReadCap+1), 0o644)
	write("pj/plugin.json", `{"hooks": {}, "mcpServers": {}, "commands": ["./run.sh", "./ok.md", {"source": "x.py"}], "skills": {"a": "dir/"}}`, 0o644)
	write("bad/plugin.json", `{nope`, 0o644)
	write("arr/plugin.json", `[1]`, 0o644)
	os.Symlink("/etc/passwd", filepath.Join(d, "link.md"))
	got := TreeProblems(d)
	sort.Strings(got)
	want := []string{
		"arr/plugin.json: plugin.json is not an object",
		"bad/plugin.json: plugin.json is not valid JSON",
		"big.md: markdown over the 65536-byte read cap (cannot be checked)",
		"exec.md: executable bit",
		"hooks: hooks/mcp file",
		"link.md: symlink",
		"pj/plugin.json: plugin.json commands points at './run.sh'",
		"pj/plugin.json: plugin.json commands points at 'x.py'",
		"pj/plugin.json: plugin.json has hooks",
		"pj/plugin.json: plugin.json has mcpServers",
		"run.py: not an allow-listed file type",
		"sh.md: shell command in markdown",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems:\n got %q\nwant %q", got, want)
	}
	if p := TreeProblems(filepath.Join(d, "ok")); len(p) != 0 {
		t.Errorf("clean tree reported %v", p)
	}
	if p := TreeProblems(filepath.Join(d, "missing")); len(p) != 1 || !strings.HasSuffix(p[0], ": not a directory") {
		t.Errorf("missing dir: %v", p)
	}
	err := AssertPromptOnlyTree(d)
	var ve *VendorError
	if err == nil || !asVE(err, &ve) || !strings.Contains(ve.Msg, "is not prompt-only") || !strings.Contains(ve.Msg, "and 7 more") {
		t.Errorf("assert: %v", err)
	}
}

func TestResolveRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, ok := range []string{filepath.Join(home, ".cache", "x"), "~/.cache/factory-skilleval", ""} {
		if _, err := ResolveRoot(ok, filepath.Join(home, "fhome")); err != nil {
			t.Errorf("ResolveRoot(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"relative/dir", filepath.Join(home, "factory", "x"), filepath.Join(home, ".claude"), home, filepath.Join(home, "fhome", "sub"), filepath.Join(home, "fhome")} {
		if _, err := ResolveRoot(bad, filepath.Join(home, "fhome")); err == nil {
			t.Errorf("ResolveRoot(%q) accepted", bad)
		}
	}
}
