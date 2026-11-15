package scout

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// Port of the vendoring half of scout/vendor.py (plan G-009 §7.1-§7.3): copy ONLY allow-listed files of a
// prompt-only candidate, at its pinned sha, into the skilleval root; re-check what was written; pre-screen it
// for exfiltration hints.
//
// Everything comes from `gh api` (git/trees and git/blobs at the pinned sha). There is no clone, no download tool,
// and nothing fetched is executed. AssertPromptOnlyTree and IsEvalAllowed are shared with the spawn and queue
// code, which must re-check before a vendored copy is handed to an agent.
//
// Archive safety: the tree listing plays the part of the archive. An entry whose path is absolute, holds a ".."
// segment, a backslash or a NUL refuses the whole vendoring (planPaths); a symlink or submodule entry is never
// copied and, being flagged by Classify, refuses the vendoring too; every file is written below the staging
// directory with O_NOFOLLOW after a containment check, so nothing lands outside the target directory.

const (
	DefaultRoot   = "~/.cache/factory-skilleval"
	MaxFiles      = 200
	MaxBytes      = 2 * 1024 * 1024
	NeedsOwner    = "needs-owner"
	ExitRefused   = 2
	ExitPrescreen = 4
	maxHitsDetail = 20
)

var (
	notEvaluable = set(NeedsOwner, "dismissed")
	badNames     = set("hooks", "hooks.json", ".mcp.json")
	sha40        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	unsafeName   = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// VendorError is a refusal; Code is the CLI exit status (2 refused, 4 pre-screen hits).
type VendorError struct {
	Msg   string
	Code  int
	Fresh *Result // the fresh classification when the stored install_risk was stale
}

func (e *VendorError) Error() string { return e.Msg }

func refuse(format string, a ...any) *VendorError {
	return &VendorError{Msg: fmt.Sprintf(format, a...), Code: ExitRefused}
}

// resolvePath is Path.resolve(strict=False): symlinks resolved for the part that exists.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolvePath(parent), filepath.Base(p))
}

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func isWithin(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ResolveRoot returns the skilleval root as a resolved path. It must lie outside ~/factory, ~/.claude and the
// factory home (neither inside them nor above them): the eval sandbox hides those, and vendored text must not
// mix with them.
func ResolveRoot(value, factoryHome string) (string, error) {
	value = pyTrim(value)
	if value == "" {
		value = DefaultRoot
	}
	p := expandUser(value)
	if !filepath.IsAbs(p) {
		return "", refuse("skilleval_root %s is not an absolute path; set it to e.g. %s", pyRepr(value), DefaultRoot)
	}
	root := resolvePath(p)
	forbidden := []string{expandUser("~/factory"), expandUser("~/.claude")}
	if factoryHome != "" {
		forbidden = append(forbidden, factoryHome)
	}
	for _, f := range forbidden {
		f = resolvePath(f)
		if root == f || isWithin(root, f) || isWithin(f, root) {
			return "", refuse("skilleval_root %s resolves to %s, which overlaps %s; choose a directory outside ~/factory and ~/.claude, e.g. %s",
				pyRepr(value), root, f, DefaultRoot)
		}
	}
	return root, nil
}

func readTextCap(p string, limit int) (string, error) {
	fh, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	var r io.Reader = fh
	if limit >= 0 {
		r = io.LimitReader(fh, int64(limit))
	}
	b, err := io.ReadAll(r)
	return decodeReplace(b), err
}

// TreeProblems lists every reason the tree under path is not a prompt-only plugin dir (empty = fine). Checks:
// symlinks and other non-regular files, exec bits, hooks dirs/hooks.json/.mcp.json, non-allow-listed extensions,
// shell lines in markdown, and plugin.json with hooks, mcpServers or commands that point at scripts.
func TreeProblems(path string) []string {
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return []string{path + ": not a directory"}
	}
	var bad []string
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: unreadable directory (%v)", relPosix(path, dir), err))
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		var dirs, files []os.DirEntry
		for _, e := range entries {
			if e.IsDir() { // DirEntry.IsDir is false for a symlink to a directory, as os.walk's isdir-with-followlinks=False
				dirs = append(dirs, e)
			} else {
				files = append(files, e)
			}
		}
		for _, e := range append(append([]os.DirEntry{}, dirs...), files...) {
			rel := relPosix(path, filepath.Join(dir, e.Name()))
			switch {
			case e.Type()&fs.ModeSymlink != 0:
				bad = append(bad, rel+": symlink")
			case badNames[strings.ToLower(e.Name())]:
				bad = append(bad, rel+": hooks/mcp file")
			}
		}
		for _, e := range files {
			full := filepath.Join(dir, e.Name())
			rel := relPosix(path, full)
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			fi, err := os.Lstat(full)
			if err != nil || !fi.Mode().IsRegular() {
				bad = append(bad, rel+": not a regular file")
				continue
			}
			if fi.Mode().Perm()&0o111 != 0 {
				bad = append(bad, rel+": executable bit")
			}
			if !IsAllowed(rel) {
				bad = append(bad, rel+": not an allow-listed file type")
				continue
			}
			low := strings.ToLower(e.Name())
			switch {
			case low == "plugin.json":
				bad = append(bad, pluginJSONProblems(full, rel)...)
			case strings.HasSuffix(low, ".md") && fi.Size() > ReadCap:
				bad = append(bad, fmt.Sprintf("%s: markdown over the %d-byte read cap (cannot be checked)", rel, ReadCap))
			case strings.HasSuffix(low, ".md"):
				if text, err := readTextCap(full, ReadCap); err == nil && text != "" && ShellBang.MatchString(text) {
					bad = append(bad, rel+": shell command in markdown")
				}
			}
		}
		for _, e := range dirs {
			walk(filepath.Join(dir, e.Name()))
		}
	}
	walk(path)
	return bad
}

func relPosix(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

// splitExt is os.path.splitext(p)[1].
func splitExt(p string) string {
	base := p[strings.LastIndex(p, "/")+1:]
	lead := 0
	for lead < len(base) && base[lead] == '.' {
		lead++
	}
	if i := strings.LastIndex(base[lead:], "."); i >= 0 {
		return base[lead+i:]
	}
	return ""
}

func pluginJSONProblems(full, rel string) []string {
	text, err := readTextCap(full, -1)
	if err != nil {
		return []string{rel + ": plugin.json cannot be read"}
	}
	data, ok := parseJSON(strings.TrimLeft(text, "\ufeff"))
	if !ok {
		return []string{rel + ": plugin.json is not valid JSON"}
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return []string{rel + ": plugin.json is not an object"}
	}
	var out []string
	for _, k := range []string{"hooks", "mcpServers"} {
		if _, has := obj[k]; has {
			out = append(out, fmt.Sprintf("%s: plugin.json has %s", rel, k))
		}
	}
	for _, k := range []string{"commands", "agents", "skills"} {
		var refs []any
		switch v := obj[k].(type) {
		case map[string]any:
			for _, name := range sortedKeys(v) {
				refs = append(refs, v[name])
			}
		case string:
			refs = []any{v}
		case []any:
			refs = v
		}
		for _, r := range refs {
			if m, isMap := r.(map[string]any); isMap {
				r = m["source"]
			}
			if s, isStr := r.(string); isStr {
				if ext := strings.ToLower(splitExt(strings.TrimRight(s, "/"))); ext != "" && ext != ".md" {
					out = append(out, fmt.Sprintf("%s: plugin.json %s points at %s", rel, k, pyRepr(s)))
				}
			}
		}
	}
	return out
}

// AssertPromptOnlyTree returns a VendorError (exit 2) naming the offenders unless the tree is prompt-only.
func AssertPromptOnlyTree(path string) error {
	bad := TreeProblems(path)
	if len(bad) == 0 {
		return nil
	}
	shown := strings.Join(bad[:min(5, len(bad))], "; ")
	if len(bad) > 5 {
		shown += fmt.Sprintf("; and %d more", len(bad)-5)
	}
	return refuse("%s is not prompt-only: %s", path, shown)
}

// -- vendored copies -------------------------------------------------------------------------------

// Skipped is one entry of the tree that was not copied.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Meta is VENDOR.json.
type Meta struct {
	Candidate     string            `json:"candidate"`
	Repo          *string           `json:"repo"`
	Path          string            `json:"path"`
	Sha           string            `json:"sha"`
	Licence       *string           `json:"licence"`
	Plugin        string            `json:"plugin"`
	Wrapped       bool              `json:"wrapped"`
	IsLocal       bool              `json:"local"`
	Copied        []string          `json:"copied"`
	Skipped       []Skipped         `json:"skipped"`
	Files         map[string]string `json:"files"`
	PrescreenHits []Hit             `json:"prescreen_hits"`
}

func (m Meta) ordered() OMap {
	str := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	skipped := make([]any, 0, len(m.Skipped))
	for _, s := range m.Skipped {
		skipped = append(skipped, OMap{{"path", s.Path}, {"reason", s.Reason}})
	}
	files := OMap{}
	for _, rel := range sortedStrings(m.Files) {
		files = append(files, KV{rel, m.Files[rel]})
	}
	o := OMap{{"candidate", m.Candidate}, {"repo", str(m.Repo)}, {"path", m.Path}, {"sha", m.Sha}, {"licence", str(m.Licence)},
		{"plugin", m.Plugin}, {"wrapped", m.Wrapped}}
	if m.IsLocal {
		o = append(o, KV{"local", true})
	}
	return append(o, KV{"copied", m.Copied}, KV{"skipped", skipped}, KV{"files", files}, KV{"prescreen_hits", hitsJSON(m.PrescreenHits)})
}

func sortedStrings(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func hitsJSON(hits []Hit) []any {
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, OMap{{"file", h.File}, {"line", h.Line}, {"pattern", h.Pattern}})
	}
	return out
}

// VendorDir is vendor_dir: <root>/vendor/<cid>@<sha>.
func VendorDir(root, cid, sha string) string { return filepath.Join(root, "vendor", cid+"@"+sha) }

func readMeta(d string) (Meta, error) {
	raw, err := os.ReadFile(filepath.Join(d, "VENDOR.json"))
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal([]byte(decodeReplace(raw)), &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// safeSegment reports whether name is one plain path segment (VENDOR.json is data: its "plugin" must not climb).
func safeSegment(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// Vendored returns VENDOR.json of a valid existing copy of C@sha; ok is false when there is none.
func Vendored(root, cid, sha string) (Meta, bool) {
	d := VendorDir(root, cid, sha)
	meta, err := readMeta(d)
	if err != nil || !safeSegment(meta.Plugin) {
		return Meta{}, false
	}
	plugin := filepath.Join(d, meta.Plugin)
	if AssertPromptOnlyTree(plugin) != nil {
		return Meta{}, false
	}
	var onDisk []string
	err = filepath.WalkDir(plugin, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !de.IsDir() {
			if fi, serr := os.Stat(p); serr == nil && fi.Mode().IsRegular() {
				onDisk = append(onDisk, relPosix(plugin, p))
			}
		}
		return nil
	})
	if err != nil {
		return Meta{}, false
	}
	want := append([]string{}, meta.Copied...)
	sort.Strings(onDisk)
	sort.Strings(want)
	if meta.Sha != sha || meta.Candidate != cid || len(onDisk) != len(want) {
		return Meta{}, false
	}
	for i := range want {
		if want[i] != onDisk[i] {
			return Meta{}, false
		}
	}
	return meta, true
}

func sha256File(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// CheckFiles refuses unless every kept file has a recorded sha256 in VENDOR.json and still matches it.
func CheckFiles(d string, meta Meta, cid string) error {
	redo := fmt.Sprintf("re-vendor the candidate (remove the kept copy, then `factory skilleval vendor %s`)", cid)
	if len(meta.Files) == 0 {
		return refuse("%s: VENDOR.json has no per-file sha256 (vendored before shas were recorded); %s", cid, redo)
	}
	plugin := filepath.Join(d, meta.Plugin)
	copied := append([]string{}, meta.Copied...)
	sort.Strings(copied)
	for _, rel := range copied {
		if _, ok := meta.Files[rel]; !ok {
			return refuse("%s: %s has no recorded sha256 in VENDOR.json; %s", cid, rel, redo)
		}
	}
	for _, rel := range sortedStrings(meta.Files) {
		if !safeRel(rel) {
			return refuse("%s: recorded file %s is not a relative path inside the copy; %s", cid, pyRepr(rel), redo)
		}
		got, err := sha256File(filepath.Join(plugin, filepath.FromSlash(rel)))
		if err != nil {
			return refuse("%s: recorded file %s is missing from the kept copy; %s", cid, rel, redo)
		}
		if got != meta.Files[rel] {
			return refuse("%s: %s differs from the sha256 recorded at vendor time (modified after vendoring); %s", cid, rel, redo)
		}
	}
	return nil
}

func safeRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\\x00") {
		return false
	}
	for _, s := range strings.Split(rel, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}

// VCand is the candidate row the vendoring functions read.
type VCand struct {
	ID, Repo, Path, HeadSha, Licence, InstallRisk, Status string
}

// Rescreen re-runs the current pre-screen on the kept copy of cand (same sha). It writes nothing: ClearHits
// records a clean result.
func Rescreen(root string, cand VCand) (Meta, []Hit, string, error) {
	d := VendorDir(root, cand.ID, cand.HeadSha)
	meta, ok := Vendored(root, cand.ID, cand.HeadSha)
	if !ok {
		if err := raiseMissingFile(d, cand); err != nil {
			return Meta{}, nil, "", err
		}
		return Meta{}, nil, "", refuse("%s: no valid vendored copy at %s; run `factory skilleval vendor` first", cand.ID, cand.HeadSha)
	}
	if err := CheckFiles(d, meta, cand.ID); err != nil {
		return Meta{}, nil, "", err
	}
	own := ""
	if meta.Repo != nil {
		own = *meta.Repo
	}
	hits, err := Prescreen(filepath.Join(d, meta.Plugin), own)
	return meta, hits, d, err
}

// raiseMissingFile: if VENDOR.json records files that are gone, name the first one.
func raiseMissingFile(d string, cand VCand) error {
	meta, err := readMeta(d)
	if err != nil || !safeSegment(meta.Plugin) {
		return nil
	}
	for _, rel := range sortedStrings(meta.Files) {
		if !safeRel(rel) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(d, meta.Plugin, filepath.FromSlash(rel))); err != nil || !fi.Mode().IsRegular() {
			return refuse("%s: recorded file %s is missing from the kept copy; re-vendor it with `factory skilleval vendor %s` (after removing the copy)",
				cand.ID, rel, cand.ID)
		}
	}
	return nil
}

// ClearHits records 0 pre-screen hits in VENDOR.json of the copy at d, keeping the key order of the file.
func ClearHits(d string) error {
	raw, err := os.ReadFile(filepath.Join(d, "VENDOR.json"))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(decodeReplace(raw)))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("VENDOR.json of %s is not a JSON object", d)
	}
	var out OMap
	replaced := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if key == "prescreen_hits" {
			v, replaced = []any{}, true
		}
		out = append(out, KV{key, normalizeNumbers(v)})
	}
	if !replaced {
		out = append(out, KV{"prescreen_hits", []any{}})
	}
	return writeFile(filepath.Join(d, "VENDOR.json"), []byte(Dumps(out, 2, false)+"\n"))
}

// IsEvalAllowed is true only for a candidate that is exactly prompt-only and not held back (needs-owner,
// dismissed). With a root, the vendored copy at the candidate's head sha must also exist, be prompt-only and
// carry no hits.
func IsEvalAllowed(c VCand, root string) bool {
	if c.InstallRisk != "prompt-only" || notEvaluable[c.Status] {
		return false
	}
	if root == "" {
		return true
	}
	meta, ok := Vendored(root, c.ID, c.HeadSha)
	return ok && len(meta.PrescreenHits) == 0
}

// HitLines is hit_lines.
func HitLines(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = fmt.Sprintf("prescreen %s: %s:%d", h.Pattern, h.File, h.Line)
	}
	return out
}

// -- vendoring -------------------------------------------------------------------------------------

func safeName(s, def string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "-"), ".-")
	s = runePrefix(s, 64)
	if s == "" {
		return def
	}
	return s
}

func gitBlobSha(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// Blobs serves blob contents by blob sha through gh, decoded from base64 and checked against their git hash.
type Blobs struct {
	gh    *Gh
	repo  string
	cache map[string][]byte
}

func NewBlobs(gh *Gh, repo string) *Blobs {
	return &Blobs{gh: gh, repo: repo, cache: map[string][]byte{}}
}

var b64Strip = regexp.MustCompile(`[^A-Za-z0-9+/]+`)

func (b *Blobs) Get(e Entry) ([]byte, error) {
	if body, ok := b.cache[e.Sha]; ok {
		return body, nil
	}
	if !sha40.MatchString(e.Sha) {
		return nil, refuse("tree entry %s has no valid blob sha", pyRepr(e.Path))
	}
	v, err := b.gh.JSON(false, "repos/"+b.repo+"/git/blobs/"+e.Sha)
	if err != nil {
		return nil, refuse("cannot fetch blob of %s: %v", pyRepr(e.Path), err)
	}
	data, _ := asMap(v)
	enc, _ := data["encoding"].(string)
	content, isStr := data["content"].(string)
	if !isStr {
		return nil, refuse("cannot fetch blob of %s: no content", pyRepr(e.Path))
	}
	if enc != "base64" {
		return nil, refuse("blob of %s is not base64; refusing", pyRepr(e.Path))
	}
	body, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64Strip.ReplaceAllString(content, ""), "="))
	if err != nil {
		return nil, refuse("cannot fetch blob of %s: %v", pyRepr(e.Path), err)
	}
	if gitBlobSha(body) != e.Sha {
		return nil, refuse("blob of %s does not match its sha %s; refusing", pyRepr(e.Path), e.Sha)
	}
	b.cache[e.Sha] = body
	return body, nil
}

// Text is Blobs.text: the decoded blob for Classify; a failure is a ReadError (the check reports "unknown").
func (b *Blobs) Text(e Entry) (string, error) {
	body, err := b.Get(e)
	if err != nil {
		return "", &ReadError{err.Error()}
	}
	return decodeReplace(body), nil
}

// planPaths splits scoped tree items into the entries to copy and the skipped ones. An unsafe path (absolute, "..",
// "." or empty segment, backslash, NUL) refuses everything: nothing is written.
func planPaths(items []scoped) (plan []scoped, skipped []Skipped, err error) {
	for _, it := range items {
		rel, e := it.rel, it.e
		if e.Type == "tree" {
			continue
		}
		if !safeRel(rel) {
			return nil, nil, refuse("unsafe path in tree: %s", pyRepr(rel))
		}
		switch {
		case e.Mode == "120000" || e.Mode == "160000" || e.Type == "commit":
			reason := "submodule"
			if e.Mode == "120000" {
				reason = "symlink"
			}
			skipped = append(skipped, Skipped{rel, reason})
		case e.Mode == "100755":
			skipped = append(skipped, Skipped{rel, "executable mode"})
		case e.Type != "blob" || !IsAllowed(e.Path):
			skipped = append(skipped, Skipped{rel, "not allow-listed"})
		default:
			plan = append(plan, it)
		}
	}
	return plan, skipped, nil
}

func wrapDest(rel, skillName string, rootSkill bool) string {
	switch strings.ToLower(strings.SplitN(rel, "/", 2)[0]) {
	case "skills", "commands", "agents":
		return rel
	}
	if rootSkill {
		return "skills/" + skillName + "/" + rel
	}
	return "skills/" + rel
}

// writeFile writes body below its parent (created as needed) with O_NOFOLLOW and mode 0644.
func writeFile(p string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	fd, err := syscall.Open(p, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	fh := os.NewFile(uintptr(fd), p)
	if _, err := fh.Write(body); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	return os.Chmod(p, 0o644)
}

// writeInside writes dest below base; a destination that resolves outside base is refused.
func writeInside(base, dest string, body []byte) error {
	if !safeRel(dest) {
		return refuse("unsafe destination %s; refusing", pyRepr(dest))
	}
	p := filepath.Join(base, filepath.FromSlash(dest))
	if !isWithin(p, base) {
		return refuse("destination %s escapes %s; refusing", pyRepr(dest), base)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// MkdirAll follows symlinks: the resolved parent must still lie inside the resolved base.
	rb, err1 := filepath.EvalSymlinks(base)
	rp, err2 := filepath.EvalSymlinks(filepath.Dir(p))
	if err1 != nil || err2 != nil || (rp != rb && !isWithin(rp, rb)) {
		return refuse("destination %s resolves outside %s (a symlink in the way); refusing", pyRepr(dest), base)
	}
	return writeFile(p, body)
}

func prune(root string, madeRoot, madeVendor bool) {
	for _, step := range []struct {
		d    string
		made bool
	}{{filepath.Join(root, "vendor"), madeVendor}, {root, madeRoot}} {
		if step.made {
			if os.Remove(step.d) != nil {
				return
			}
		}
	}
}

// VendorResult is what Vendor and VendorLocal return.
type VendorResult struct {
	Dir          string
	Plugin       string
	Meta         Meta
	Reclassified *Result
	Reused       bool
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Vendor vendors candidate cand into root: only allow-listed blobs at the pinned sha, re-classified first, written
// to a staging directory, re-checked, pre-screened, then renamed into place. Any refusal leaves nothing behind.
func Vendor(gh *Gh, cand VCand, root string) (res *VendorResult, err error) {
	cid, repo, sha := cand.ID, cand.Repo, cand.HeadSha
	path := strings.Trim(cand.Path, "/")
	if !safeSegment(cid) {
		return nil, refuse("candidate id %s is not a plain name; refusing", pyRepr(cid))
	}
	if !repoRe.MatchString(repo) || !sha40.MatchString(sha) || (path != "" && !pathRe.MatchString(path)) {
		return nil, refuse("%s: needs a GitHub repo and a full head sha (have repo=%s, sha=%s); run `factory scout fetch` again", cid, pyRepr(repo), pyRepr(sha))
	}
	if cand.Status == NeedsOwner {
		return nil, &VendorError{Msg: fmt.Sprintf("%s is %s: pre-screen hits wait for the owner; nothing is vendored", cid, NeedsOwner), Code: ExitPrescreen}
	}
	if cand.InstallRisk != "prompt-only" {
		return nil, refuse("%s: install_risk is %s, not prompt-only; refusing", cid, pyRepr(cand.InstallRisk))
	}
	if done, ok := Vendored(root, cid, sha); ok { // the tree at a sha never changes, so a valid copy needs no refetch
		return &VendorResult{Dir: VendorDir(root, cid, sha), Plugin: done.Plugin, Meta: done, Reused: true}, nil
	}
	v, gerr := gh.JSON(false, "repos/"+repo+"/git/trees/"+sha+"?recursive=1")
	if gerr != nil {
		return nil, refuse("%s: cannot list the tree at %s: %v", cid, sha, gerr)
	}
	data, _ := asMap(v)
	list, ok := data["tree"].([]any)
	if !ok {
		return nil, refuse("%s: tree response at %s has no tree list", cid, sha)
	}
	tree, perr := ParseTree(list)
	if perr != nil {
		return nil, refuse("%s: tree response at %s is malformed: %v", cid, sha, perr)
	}
	trunc, _ := data["truncated"].(bool)
	blobs := NewBlobs(gh, repo)
	fresh := Classify(tree, path, blobs.Text, trunc)
	if fresh.Risk != "prompt-only" {
		detail := fresh.Detail[:min(5, len(fresh.Detail))]
		return nil, &VendorError{Msg: fmt.Sprintf("%s: fresh install risk at %s is %s (DB said prompt-only): %s; refusing",
			cid, sha[:12], fresh.Risk, strings.Join(detail, "; ")), Code: ExitRefused, Fresh: &fresh}
	}
	items := scopedEntries(tree, path)
	plan, skipped, err := planPaths(items)
	if err != nil {
		return nil, err
	}
	wrapped := true
	for _, it := range plan {
		if strings.ToLower(it.rel) == ".claude-plugin/plugin.json" {
			wrapped = false
		}
	}
	if len(plan) > MaxFiles {
		return nil, refuse("%s: %d files exceed the cap of %d; refusing", cid, len(plan), MaxFiles)
	}
	var planned int64
	for _, it := range plan {
		planned += it.e.Size
	}
	if planned > MaxBytes {
		return nil, refuse("%s: files exceed the %d byte cap; refusing", cid, MaxBytes)
	}

	last := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		last = path[i+1:]
	}
	if path == "" {
		last = strings.SplitN(repo, "/", 2)[1]
	}
	nameHint := safeName(last, "plugin")
	type dest struct {
		rel string
		e   Entry
	}
	var dests []dest
	var pluginName string
	if wrapped {
		pluginName = "cand-" + strings.ToLower(cid)
		rootSkill := false
		for _, it := range plan {
			rootSkill = rootSkill || strings.ToLower(it.rel) == "skill.md"
		}
		for _, it := range plan {
			dests = append(dests, dest{wrapDest(it.rel, nameHint, rootSkill), it.e})
		}
	} else {
		var pj Entry
		for _, it := range plan {
			if strings.ToLower(it.rel) == ".claude-plugin/plugin.json" {
				pj = it.e
				break
			}
		}
		pluginName = nameHint
		if text, terr := blobs.Text(pj); terr == nil {
			if pv, ok := parseJSON(strings.TrimLeft(text, "\ufeff")); ok {
				if obj, isObj := pv.(map[string]any); isObj {
					if n, isStr := obj["name"].(string); isStr {
						pluginName = safeName(n, nameHint)
					}
				}
			}
		}
		for _, it := range plan {
			dests = append(dests, dest{it.rel, it.e})
		}
	}

	madeRoot, madeVendor := !exists(root), !exists(filepath.Join(root, "vendor"))
	out := VendorDir(root, cid, sha)
	stage := out + ".partial"
	_ = os.RemoveAll(stage)
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stage)
			prune(root, madeRoot, madeVendor)
		}
	}()
	plugin := filepath.Join(stage, pluginName)
	var total int64
	var copied []string
	for _, d := range dests {
		body, berr := blobs.Get(d.e)
		if berr != nil {
			return nil, berr
		}
		if total += int64(len(body)); total > MaxBytes {
			return nil, refuse("%s: files exceed the %d byte cap; refusing", cid, MaxBytes)
		}
		if werr := writeInside(plugin, d.rel, body); werr != nil {
			return nil, asVendorErr(werr, cid)
		}
		copied = append(copied, d.rel)
	}
	if wrapped {
		gen := OMap{{"name", pluginName}, {"description", fmt.Sprintf("G-009 eval copy of %s@%s", repo, sha)}}
		if werr := writeInside(plugin, ".claude-plugin/plugin.json", []byte(Dumps(gen, 2, false)+"\n")); werr != nil {
			return nil, asVendorErr(werr, cid)
		}
		copied = append(copied, ".claude-plugin/plugin.json")
	}
	meta, merr := finishCopy(cid, plugin, stage, repo, copied, cand.Licence, sha, path, pluginName, wrapped, false, skipped)
	if merr != nil {
		return nil, merr
	}
	_ = os.RemoveAll(out)
	if rerr := os.Rename(stage, out); rerr != nil {
		return nil, refuse("%s: cannot move the staged copy into place: %v", cid, rerr)
	}
	return &VendorResult{Dir: out, Plugin: pluginName, Meta: meta, Reclassified: &fresh}, nil
}

func asVendorErr(err error, cid string) error {
	var ve *VendorError
	if errors.As(err, &ve) {
		return err
	}
	return refuse("%s: cannot write the vendored copy: %v", cid, err)
}

// finishCopy re-checks what was written, pre-screens it and writes VENDOR.json into the stage.
func finishCopy(cid, plugin, stage, repo string, copied []string, licence, sha, path, pluginName string, wrapped, local bool, skipped []Skipped) (Meta, error) {
	if err := AssertPromptOnlyTree(plugin); err != nil {
		return Meta{}, err
	}
	hits, err := Prescreen(plugin, repo)
	if err != nil {
		return Meta{}, refuse("%s: cannot pre-screen the copy: %v", cid, err)
	}
	sorted := append([]string{}, copied...)
	sort.Strings(sorted)
	files := map[string]string{}
	for _, rel := range sorted {
		h, err := sha256File(filepath.Join(plugin, filepath.FromSlash(rel)))
		if err != nil {
			return Meta{}, refuse("%s: cannot hash %s: %v", cid, rel, err)
		}
		files[rel] = h
	}
	meta := Meta{Candidate: cid, Path: path, Sha: sha, Plugin: pluginName, Wrapped: wrapped, IsLocal: local,
		Copied: sorted, Skipped: skipped, Files: files, PrescreenHits: hits}
	if repo != "" {
		meta.Repo = &repo
	}
	if licence != "" {
		meta.Licence = &licence
	}
	if meta.Skipped == nil {
		meta.Skipped = []Skipped{}
	}
	if err := writeFile(filepath.Join(stage, "VENDOR.json"), []byte(Dumps(meta.ordered(), 2, false)+"\n")); err != nil {
		return Meta{}, refuse("%s: cannot write VENDOR.json: %v", cid, err)
	}
	return meta, nil
}

// ContentSha is content_sha: a 40-hex hash of the files under src (sorted relative paths + bytes), the vendor sha
// of a repo-owned local plugin, so any edit to it is a new sha and a new set of trials.
func ContentSha(src string) (string, error) {
	files, err := regularFiles(src)
	if err != nil {
		return "", err
	}
	h := sha1.New()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// regularFiles lists the regular files under src as relative slash paths, ordered like sorted(Path) in Python
// (component by component, not by the joined string).
func regularFiles(src string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if fi, serr := os.Stat(p); serr == nil && fi.Mode().IsRegular() {
			rels = append(rels, relPosix(src, p))
		}
		return nil
	})
	sort.Slice(rels, func(i, j int) bool {
		a, b := strings.Split(rels[i], "/"), strings.Split(rels[j], "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return rels, err
}

// VendorLocal vendors a repo-owned plugin dir src for candidate cand into root with the same rules as Vendor:
// 0644 files, AssertPromptOnlyTree and the pre-screen on the written copy, VENDOR.json.
func VendorLocal(cand VCand, root, src, pluginName string) (res *VendorResult, err error) {
	cid, sha := cand.ID, cand.HeadSha
	if !safeSegment(cid) {
		return nil, refuse("candidate id %s is not a plain name; refusing", pyRepr(cid))
	}
	if !sha40.MatchString(sha) {
		return nil, refuse("%s: needs a full head sha (have %s)", cid, pyRepr(sha))
	}
	if cand.Status == NeedsOwner {
		return nil, &VendorError{Msg: fmt.Sprintf("%s is %s: pre-screen hits wait for the owner; nothing is vendored", cid, NeedsOwner), Code: ExitPrescreen}
	}
	if cand.InstallRisk != "prompt-only" {
		return nil, refuse("%s: install_risk is %s, not prompt-only; refusing", cid, pyRepr(cand.InstallRisk))
	}
	if !safeSegment(pluginName) {
		return nil, refuse("%s: plugin name %s is not a plain directory name; refusing", cid, pyRepr(pluginName))
	}
	if done, ok := Vendored(root, cid, sha); ok {
		return &VendorResult{Dir: VendorDir(root, cid, sha), Plugin: done.Plugin, Meta: done, Reused: true}, nil
	}
	if err := AssertPromptOnlyTree(src); err != nil { // the source itself must already be clean
		return nil, err
	}
	files, ferr := regularFiles(src)
	if ferr != nil {
		return nil, refuse("%s: cannot list %s: %v", cid, src, ferr)
	}
	var size int64
	for _, rel := range files {
		if fi, serr := os.Stat(filepath.Join(src, filepath.FromSlash(rel))); serr == nil {
			size += fi.Size()
		}
	}
	if len(files) > MaxFiles || size > MaxBytes {
		return nil, refuse("%s: local plugin exceeds the file or byte cap; refusing", cid)
	}
	madeRoot, madeVendor := !exists(root), !exists(filepath.Join(root, "vendor"))
	out := VendorDir(root, cid, sha)
	stage := out + ".partial"
	_ = os.RemoveAll(stage)
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stage)
			prune(root, madeRoot, madeVendor)
		}
	}()
	plugin := filepath.Join(stage, pluginName)
	for _, rel := range files {
		body, rerr := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if rerr != nil {
			return nil, refuse("%s: cannot read %s: %v", cid, rel, rerr)
		}
		if werr := writeInside(plugin, rel, body); werr != nil {
			return nil, asVendorErr(werr, cid)
		}
	}
	meta, merr := finishCopy(cid, plugin, stage, "", files, cand.Licence, sha, "", pluginName, false, true, nil)
	if merr != nil {
		return nil, merr
	}
	_ = os.RemoveAll(out)
	if rerr := os.Rename(stage, out); rerr != nil {
		return nil, refuse("%s: cannot move the staged copy into place: %v", cid, rerr)
	}
	return &VendorResult{Dir: out, Plugin: pluginName, Meta: meta}, nil
}
