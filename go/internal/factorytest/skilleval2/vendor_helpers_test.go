package skilleval2

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	sc "factory/go/internal/scout"
)

// Port of the fixtures of tests/test_skilleval_vendor.py: the Tree gh fixture, VendorBase and the small helpers.
// The CLI cases drive `factory skilleval vendor`, which the Go CLI answers itself for the director, lead and owner; a
// worker caller (coder, qa, reviewer, pm, scout, evaluator) is delegated to Python (callerIsWorker in cmd_skilleval.go),
// so the cases that act as a worker call Env.NeedPython and skip where python3 is absent.

var vendorSHA = strings.Repeat("a", 40)

const (
	vendorRepoName = "zz-owner/zz-repo"
	vendorSkillMD  = "---\nname: foo\ndescription: Does foo.\n---\nWrite clear prose.\n"
)

type vFile struct {
	mode    string
	content []byte
}

func vf(content string) vFile { return vFile{"100644", []byte(content)} }

type vFiles map[string]vFile

func vmerge(base vFiles, extra vFiles) vFiles {
	out := vFiles{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func vendorJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("vendor fixture: %v", err))
	}
	return string(b)
}

var vendorExecTree = vFiles{
	"SKILL.md": vf(vendorSkillMD), "x.sh": vf("echo hi\n"), "x.py": vf("print(1)\n"), "x.js": vf("1\n"), "x.cjs": vf("1\n"),
	"x.mjs": vf("1\n"), "x.ts": vf("1\n"), "blob.bin": {"100644", []byte{0, 1, 2, 0xff}}, "README.md": {"100755", []byte("# readme\n")},
	"link": {"120000", []byte("x.sh")}, "hooks/hooks.json": vf("{}"), ".mcp.json": vf("{}"),
	"commands/run.md":            vf("Do it\n!`rm -rf x`\n"),
	".claude-plugin/plugin.json": vf(vendorJSON(map[string]any{"name": "n", "hooks": map[string]any{}, "mcpServers": map[string]any{}})),
}

var vendorPluginTree = vFiles{
	"other/evil.sh":                        vf("echo outside\n"),
	"plugins/p/.claude-plugin/plugin.json": vf(vendorJSON(map[string]any{"name": "demo", "description": "d"})),
	"plugins/p/skills/foo/SKILL.md":        vf(vendorSkillMD),
	"plugins/p/skills/foo/reference.md":    vf("# Reference\nMore prose.\n"),
	"plugins/p/skills/foo/data.csv":        vf("a,b\n1,2\n"),
	"plugins/p/LICENSE":                    vf("MIT License\n"),
}

func vendorBlobSha(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// vTree is the gh api fixture of one repo tree (Python class Tree).
type vTree struct {
	rules    []ft.StubRule
	blobShas []string
	entries  []sc.Entry
}

func newVTree(files vFiles, sizeLie int64) *vTree {
	t := &vTree{}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var entries []map[string]any
	for _, p := range paths {
		f := files[p]
		bsha := vendorBlobSha(f.content)
		typ := "blob"
		if f.mode == "160000" {
			typ = "commit"
		}
		size := int64(len(f.content))
		if sizeLie >= 0 {
			size = sizeLie
		}
		entries = append(entries, map[string]any{"path": p, "mode": f.mode, "type": typ, "sha": bsha, "size": size})
		t.entries = append(t.entries, sc.Entry{Path: p, Mode: f.mode, Type: typ, Sha: bsha, Size: size})
		t.blobShas = append(t.blobShas, bsha)
		t.rules = append(t.rules, ft.StubRule{
			Match: []string{"repos/" + vendorRepoName + "/git/blobs/" + bsha},
			Out:   vendorJSON(map[string]any{"content": base64.StdEncoding.EncodeToString(f.content), "encoding": "base64"}),
		})
	}
	t.rules = append(t.rules, ft.StubRule{
		Match: []string{"repos/" + vendorRepoName + "/git/trees/" + vendorSHA},
		Out:   vendorJSON(map[string]any{"tree": entries, "truncated": false}),
	})
	return t
}

// vWorld is VendorBase: a harness home with the agents, the skilleval_root config row and the gh fixture.
type vWorld struct {
	t    *testing.T
	e    *ft.Env
	root string
}

func newVWorld(t *testing.T) *vWorld {
	t.Helper()
	e := ft.New(t)
	for _, r := range []string{"scout", "director", "lead", "coder", "qa", "reviewer", "pm", "evaluator"} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r})
	}
	w := &vWorld{t: t, e: e, root: filepath.Join(e.Root, "evalroot")}
	if r := w.run("owner", "config", "skilleval_root", w.root); r.Exit != 0 {
		t.Fatalf("config skilleval_root: exit %d: %s", r.Exit, r.Stderr)
	}
	return w
}

func (w *vWorld) run(agent string, args ...string) ft.Result {
	w.t.Helper()
	return w.e.RunWith(ft.Opts{Agent: agent}, args...)
}

func (w *vWorld) exec(q string, args ...any) {
	w.t.Helper()
	d := w.e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

func (w *vWorld) setRoot(v string) {
	w.exec("INSERT INTO config(key,value) VALUES('skilleval_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", v)
}

func (w *vWorld) seed(cid, path, risk, status string) {
	w.t.Helper()
	var p any
	if path != "" {
		p = path
	}
	key := strings.TrimRight("gh:"+vendorRepoName+"/"+path, "/") + cid
	w.exec("INSERT INTO scout_candidates(id,key,repo,path,head_sha,licence,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES(?,?,?,?,?,?,?,?,'[]','t','t')", cid, key, vendorRepoName, p, vendorSHA, "MIT", risk, status)
}

type vOpt struct {
	cid, risk, status string
	sizeLie           int64
}

func vopt(o ...vOpt) vOpt {
	r := vOpt{cid: "C-0001", risk: "prompt-only", status: "new", sizeLie: -1}
	if len(o) > 0 {
		r = o[0]
		if r.cid == "" {
			r.cid = "C-0001"
		}
		if r.risk == "" {
			r.risk = "prompt-only"
		}
		if r.status == "" {
			r.status = "new"
		}
	}
	return r
}

// tree serves files as the repo tree through gh and seeds the candidate row.
func (w *vWorld) tree(files vFiles, path string, o ...vOpt) *vTree {
	w.t.Helper()
	op := vopt(o...)
	t := newVTree(files, op.sizeLie)
	w.e.Script("gh", t.rules...)
	w.seed(op.cid, path, op.risk, op.status)
	return t
}

// vendor runs `skilleval vendor <cid>` and requires exit code expect.
func (w *vWorld) vendor(cid, agent string, expect int) ft.Result {
	w.t.Helper()
	if cid == "" {
		cid = "C-0001"
	}
	if agent == "" {
		agent = "owner"
	}
	return w.expect(expect, agent, "skilleval", "vendor", cid)
}

func (w *vWorld) expect(code int, agent string, args ...string) ft.Result {
	w.t.Helper()
	r := w.run(agent, args...)
	if r.Exit != code {
		w.t.Fatalf("%v as %s: exit %d, want %d\nstdout: %s\nstderr: %s", args, agent, r.Exit, code, r.Stdout, r.Stderr)
	}
	return r
}

func (w *vWorld) clearCandidates() { w.exec("DELETE FROM scout_candidates") }

func (w *vWorld) cand(cid string) map[string]string {
	w.t.Helper()
	if cid == "" {
		cid = "C-0001"
	}
	d := w.e.RawDB()
	defer d.Close()
	rows, err := d.Query("SELECT * FROM scout_candidates WHERE id=?", cid)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		w.t.Fatal(err)
	}
	if !rows.Next() {
		w.t.Fatalf("no candidate %s", cid)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		w.t.Fatal(err)
	}
	out := map[string]string{}
	for i, c := range cols {
		switch v := vals[i].(type) {
		case nil:
			out[c] = ""
		case []byte:
			out[c] = string(v)
		default:
			out[c] = fmt.Sprint(v)
		}
	}
	return out
}

func (w *vWorld) vcand(cid string) sc.VCand {
	c := w.cand(cid)
	return sc.VCand{ID: c["id"], Repo: c["repo"], Path: c["path"], HeadSha: c["head_sha"], Licence: c["licence"], InstallRisk: c["install_risk"], Status: c["status"]}
}

func (w *vWorld) apiCalls() []string {
	var out []string
	for _, c := range w.e.Calls("gh") {
		if len(c.Argv) > 0 && c.Argv[0] == "api" {
			out = append(out, strings.Join(c.Argv, " "))
		}
	}
	return out
}

func (w *vWorld) vendorDir() string { return filepath.Join(w.root, "vendor", "C-0001@"+vendorSHA) }

func vendorFilesUnder(t testing.TB, d string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			rel, rerr := filepath.Rel(d, p)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", d, err)
	}
	sort.Strings(out)
	return out
}

func vendorExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func vendorReadMeta(t testing.TB, d string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d, "VENDOR.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("VENDOR.json: %v", err)
	}
	return m
}

func vendorWriteMeta(t testing.TB, d string, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "VENDOR.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func vendorWrite(t testing.TB, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func vendorHas(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func vendorEq(t testing.TB, what string, got, want any) {
	t.Helper()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("%s = %#v, want %#v", what, got, want)
	}
}

func vendorStrList(v any) []string {
	l, _ := v.([]any)
	out := []string{}
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// Python's (?<![\w-])main(?![\w-])|HEAD over an ASCII gh call line.
var vendorMainRe = regexp.MustCompile(`(^|[^A-Za-z0-9_-])main($|[^A-Za-z0-9_-])`)

func vendorCallPinned(call string, blobShas []string) bool {
	if strings.Contains(call, "git/trees/"+vendorSHA) {
		return true
	}
	for _, b := range blobShas {
		if strings.Contains(call, "git/blobs/"+b) {
			return true
		}
	}
	return false
}
