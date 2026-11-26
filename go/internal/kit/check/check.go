// Package check validates a Hivemill Kit repo (ADR 0007 section G): kit/skills.json, every registered plugin and the
// copyable template under kit/template. It only reads files; it never runs a shim or a binary.
package check

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"factory/go/internal/kit"
)

// Finding is one problem found in the repo; Path is slash-separated and relative to the repo root.
type Finding struct {
	Path    string
	Problem string
}

// String renders the finding as "kit-check: <path>: <problem>".
func (f Finding) String() string { return "kit-check: " + f.Path + ": " + f.Problem }

// Entry is one skill in kit/skills.json. A kit skill has a plugin and a binary in this repo. An external skill has
// repo set instead: its files stay in their own repository, pinned to a commit.
type Entry struct {
	Name        string `json:"name"`
	Level       string `json:"level"`
	Plugin      string `json:"plugin"`
	Binary      string `json:"binary"`
	Description string `json:"description"`
	UseWhen     string `json:"use_when"`
	Repo        string `json:"repo"`
	Commit      string `json:"commit"`
	Path        string `json:"path"`
	License     string `json:"license"`
	Install     string `json:"install"`
}

// Registry is kit/skills.json.
type Registry struct {
	SchemaVersion int     `json:"schemaVersion"`
	Skills        []Entry `json:"skills"`
}

// MaxRegistrySchema is the newest kit/skills.json schemaVersion this binary understands.
const MaxRegistrySchema = 1

const (
	registryPath    = "kit/skills.json"
	marketplacePath = ".claude-plugin/marketplace.json"
	thirdPartyPath  = "kit/skills/THIRD_PARTY.md"
	templateDir     = "kit/template"
	templateName    = "example"
)

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	pinRe    = regexp.MustCompile(`(?m)^SHA_(LINUX|DARWIN)_(AMD64|ARM64)="([0-9a-f]{64})"\s*$`)
	repoRe   = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	versRe   = regexp.MustCompile(`(?m)^VERSION="([^"]*)"\s*$`)
	levels   = map[string]bool{"SOLO": true, "HOMELAB": true, "FULL": true}
	wantPins = []string{"SHA_LINUX_AMD64", "SHA_LINUX_ARM64", "SHA_DARWIN_AMD64", "SHA_DARWIN_ARM64"}
)

// Result is the outcome of Run.
type Result struct {
	Findings []Finding
	Skills   int
}

// Run validates the repo at root and returns the findings, sorted by path then problem.
func Run(root string) Result {
	c := &collector{}
	version, err := readVersion(root)
	if err != nil {
		c.add("VERSION", err.Error())
	}
	reg, ok := loadRegistry(root, c)
	if ok {
		market := loadMarketplace(root, c)
		seen := map[string]bool{}
		for _, e := range reg.Skills {
			if !validEntry(e, seen, c) {
				continue
			}
			if e.Repo != "" {
				checkThirdParty(root, e, c)
				continue
			}
			checkMarketplace(market, e, c)
			if st, err := os.Stat(filepath.Join(root, "go", "cmd", e.Binary)); err != nil || !st.IsDir() {
				c.add("go/cmd/"+e.Binary, "missing: the binary's main package directory go/cmd/"+e.Binary+"/")
			}
			checkPlugin(root, e.Plugin, e.Name, version, c)
		}
	}
	checkTemplate(root, version, c)
	sort.SliceStable(c.out, func(i, j int) bool {
		if c.out[i].Path != c.out[j].Path {
			return c.out[i].Path < c.out[j].Path
		}
		return c.out[i].Problem < c.out[j].Problem
	})
	return Result{Findings: c.out, Skills: len(reg.Skills)}
}

type collector struct{ out []Finding }

func (c *collector) add(path, problem string) {
	c.out = append(c.out, Finding{Path: filepath.ToSlash(path), Problem: problem})
}

func readVersion(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", fmt.Errorf("cannot read the VERSION file: %v", err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("the VERSION file is empty")
	}
	return v, nil
}

func loadRegistry(root string, c *collector) (Registry, bool) {
	var reg Registry
	if err := kit.LoadConfig(filepath.Join(root, registryPath), MaxRegistrySchema, &reg); err != nil {
		c.add(registryPath, err.Error())
		return reg, false
	}
	return reg, true
}

func validEntry(e Entry, seen map[string]bool, c *collector) bool {
	ok := true
	at := registryPath
	bad := func(format string, a ...any) {
		c.add(at, fmt.Sprintf("skill %q: ", e.Name)+fmt.Sprintf(format, a...))
		ok = false
	}
	if !nameRe.MatchString(e.Name) {
		bad("name must be lowercase letters, digits and dashes, start with a letter, at most 31 chars")
		return false
	}
	if seen[e.Name] {
		bad("duplicate name")
		return false
	}
	seen[e.Name] = true
	if e.Description == "" || len([]rune(e.Description)) > 200 {
		bad("description must be 1 to 200 chars")
	}
	if len([]rune(e.UseWhen)) > 200 {
		bad("use_when must be at most 200 chars")
	}
	if e.Repo != "" {
		validExternal(e, bad)
		return ok
	}
	if e.Commit != "" || e.Path != "" || e.License != "" || e.Install != "" {
		bad("commit, path, license and install need repo (an external skill)")
	}
	if !levels[e.Level] {
		bad("level must be SOLO, HOMELAB or FULL")
	}
	if e.Plugin != "plugins/kit-"+e.Name {
		bad("plugin must be plugins/kit-%s", e.Name)
	}
	if e.Binary != "hm-"+e.Name {
		bad("binary must be hm-%s", e.Name)
	}
	return ok
}

func validExternal(e Entry, bad func(string, ...any)) {
	if !repoRe.MatchString(e.Repo) {
		bad("repo must be https://github.com/OWNER/REPO")
	}
	if !commitRe.MatchString(e.Commit) {
		bad("commit must be a full 40-character commit SHA")
	}
	if !filepath.IsLocal(e.Path) {
		bad("path must be a relative directory inside the repository")
	}
	if e.License == "" {
		bad("license must name the upstream licence, or \"none\"")
	}
	if e.Install == "" || !strings.Contains(e.Install, e.Commit) ||
		(len(e.Commit) >= 12 && !strings.Contains(e.Install, e.Name+"@"+e.Commit[:12])) {
		bad("install must be a command that names the pinned commit")
	}
	if e.UseWhen == "" {
		bad("use_when is required")
	}
	if e.Level != "" || e.Plugin != "" || e.Binary != "" {
		bad("an external skill has no level, plugin or binary")
	}
}

// checkThirdParty wants the licence note of an external skill to name it and its pin.
func checkThirdParty(root string, e Entry, c *collector) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(thirdPartyPath)))
	if err != nil {
		c.add(thirdPartyPath, "cannot read: "+err.Error())
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, e.Name) && strings.Contains(line, e.Commit) {
			return
		}
	}
	c.add(thirdPartyPath, fmt.Sprintf("must name external skill %q and its pinned commit on one line", e.Name))
}

func loadMarketplace(root string, c *collector) []map[string]any {
	b, err := os.ReadFile(filepath.Join(root, marketplacePath))
	if err != nil {
		c.add(marketplacePath, "cannot read: "+err.Error())
		return nil
	}
	var m struct {
		Plugins []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		c.add(marketplacePath, "invalid JSON: "+err.Error())
		return nil
	}
	return m.Plugins
}

func checkMarketplace(plugins []map[string]any, e Entry, c *collector) {
	for _, p := range plugins {
		if p["name"] == "hivemill-"+e.Name && p["source"] == "./plugins/kit-"+e.Name {
			return
		}
	}
	c.add(marketplacePath, fmt.Sprintf("no entry with name \"hivemill-%s\" and source \"./plugins/kit-%s\"", e.Name, e.Name))
}

// checkPlugin validates one plugin directory (relative to root). Pins of 64 zeros count as pinned: the shim refuses
// to run unpinned, and the release process writes the real values.
func checkPlugin(root, plugin, name, version string, c *collector) {
	rel := func(p string) string { return plugin + "/" + p }
	dir := filepath.Join(root, filepath.FromSlash(plugin))

	pj := rel(".claude-plugin/plugin.json")
	var plug struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if readJSON(root, pj, &plug, c) {
		if plug.Name != "hivemill-"+name {
			c.add(pj, fmt.Sprintf("name must be %q, got %q", "hivemill-"+name, plug.Name))
		}
		if version != "" && plug.Version != version {
			c.add(pj, fmt.Sprintf("version %q must equal the VERSION file (%s)", plug.Version, version))
		}
	}

	sk := rel("skills/" + name + "/SKILL.md")
	if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sk))); err != nil {
		c.add(sk, "missing")
	} else if problem := frontMatterProblem(string(b), name); problem != "" {
		c.add(sk, problem)
	}

	ex := rel(name + ".example.json")
	var exDoc map[string]any
	if readJSON(root, ex, &exDoc, c) {
		if v, ok := exDoc["schemaVersion"].(float64); !ok || v < 1 || v != float64(int(v)) {
			c.add(ex, "needs an integer schemaVersion >= 1")
		}
	}

	checkShim(root, rel("bin/hm-"+name), version, c)

	cases, _ := filepath.Glob(filepath.Join(dir, "eval", "cases", "*.json"))
	if len(cases) < 3 {
		c.add(rel("eval/cases"), fmt.Sprintf("needs at least 3 .json cases, found %d", len(cases)))
	}

	scanSecrets(root, plugin, name, c)
}

func readJSON(root, rel string, v any, c *collector) bool {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		c.add(rel, "missing")
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		c.add(rel, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func frontMatterProblem(text, name string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "must start with a --- front matter block"
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "front matter block is not closed with ---"
	}
	fields := map[string]string{}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	if fields["name"] == "" || fields["description"] == "" {
		return "front matter needs non-empty name and description"
	}
	if fields["name"] != name {
		return fmt.Sprintf("front matter name must be %q, got %q", name, fields["name"])
	}
	return ""
}

func checkShim(root, rel, version string, c *collector) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		c.add(rel, "missing")
		return
	}
	text := string(b)
	if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && fi.Mode()&0o111 == 0 {
		c.add(rel, "not executable; run chmod +x")
	}
	if m := versRe.FindStringSubmatch(text); m == nil {
		c.add(rel, "no VERSION=\"...\" line")
	} else if version != "" && m[1] != version {
		c.add(rel, fmt.Sprintf("VERSION %q must equal the VERSION file (%s)", m[1], version))
	}
	have := map[string]bool{}
	for _, m := range pinRe.FindAllStringSubmatch(text, -1) {
		have["SHA_"+m[1]+"_"+m[2]] = true
	}
	var missing []string
	for _, p := range wantPins {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		c.add(rel, "pins missing or not 64 lowercase hex: "+strings.Join(missing, ", "))
	}
	if !strings.Contains(text, "https://") || strings.Contains(text, "http://") {
		c.add(rel, "download base must be https only (found no https:// or a plain http://)")
	}
}

// checkTemplate copies kit/template into a temp repo as plugins/kit-example with NAME replaced by "example" and
// @VERSION@ by the VERSION file, then runs the per-plugin checks on the copy.
func checkTemplate(root, version string, c *collector) {
	src := filepath.Join(root, filepath.FromSlash(templateDir))
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		c.add(templateDir, "missing")
		return
	}
	tmp, err := os.MkdirTemp("", "kit-check-template-")
	if err != nil {
		c.add(templateDir, "cannot create a temp dir: "+err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	repl := strings.NewReplacer("NAME", templateName, "@VERSION@", version)
	dst := filepath.Join(tmp, "plugins", "kit-"+templateName)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		r, _ := filepath.Rel(src, p)
		if r == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, repl.Replace(r))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if fi, ierr := d.Info(); ierr == nil {
			perm = fi.Mode().Perm()
		}
		return os.WriteFile(target, []byte(repl.Replace(string(b))), perm)
	})
	if err != nil {
		c.add(templateDir, "cannot copy the template: "+err.Error())
		return
	}
	var inner collector
	checkPlugin(tmp, "plugins/kit-"+templateName, templateName, version, &inner)
	for _, f := range inner.out {
		c.add(templatePath(f.Path), f.Problem)
	}
}

// templatePath maps a path inside the substituted copy back to its place in kit/template.
func templatePath(p string) string {
	p = strings.TrimPrefix(p, "plugins/kit-"+templateName)
	segs := strings.Split(p, "/")
	for i, s := range segs {
		switch s {
		case templateName:
			segs[i] = "NAME"
		case templateName + "." + templateName + ".json":
			segs[i] = "NAME." + templateName + ".json"
		case "hm-" + templateName:
			segs[i] = "hm-NAME"
		}
	}
	return templateDir + strings.Join(segs, "/")
}
