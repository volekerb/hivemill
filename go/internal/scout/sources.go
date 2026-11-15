package scout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Port of scout/sources.py: the five sources, dedupe, star snapshots and install-risk (plan G-009 §4, §5, §7.1).
//
// Every GitHub call goes through Gh.API (`gh api` only, 30 s timeout). Nothing fetched is executed or written to
// disk; READMEs and descriptions are untrusted text that only ends up in bound SQL parameters. A failing source is
// caught and reported in the summary; the others still run.

const (
	GhTimeout           = 30 * time.Second
	HNTimeout           = 15 * time.Second
	HNDefaultURL        = "https://hn.algolia.com/api/v1/search_by_date"
	HNMinPoints         = 20
	HNFirstLookbackDays = 3
	MaxSearchCalls      = 10
	NewCap              = 15 // new candidates per run from search + awesome-lists + hn
	MaxChecks           = 40 // unknown repos whose tree is inspected per run (calls budget)
	DroppedMax          = 2000
	MarketplaceRepo     = "anthropics/claude-plugins-official"
	SkillsRepo          = "anthropics/skills"
	ReadmeMax           = 4096
	maxHNBody           = 16 << 20 // Python reads without a limit; a hostile server must not exhaust memory
)

var (
	HNQueries = []string{"claude code skill", "claude skills", "claude code plugin"}
	Topics    = []string{"claude-code", "claude-skills", "agent-skills", "claude-code-plugin", "claude-code-skills", "claude-code-subagents"}
	Awesome   = []string{"hesreallyhim/awesome-claude-code", "ComposioHQ/awesome-claude-skills", "travisvn/awesome-claude-skills",
		"VoltAgent/awesome-claude-code-subagents", "zhuyansen/awesome-claude-video-skills"}
	Raw     = []string{"-H", "Accept: application/vnd.github.raw"}
	Sources = []string{"marketplace", "skills", "search", "awesome", "hn"}

	repoRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	pathRe    = regexp.MustCompile(`^[A-Za-z0-9_.@+/ -]+$`)
	linkRe    = regexp.MustCompile(`(?i)https?://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)
	sha40Re   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha7Re    = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	skillsRe  = regexp.MustCompile(`^skills/[^/]+/SKILL\.md$`)
	notOwners = set("sponsors", "topics", "orgs", "features", "about", "settings", "marketplace", "apps", "login", "join",
		"pricing", "collections", "trending", "explore", "enterprise", "security", "readme", "site",
		"customer-stories", "user-attachments", "issues", "pulls", "notifications", "search")
)

// GhError is a gh call that failed, timed out or returned something unusable.
type GhError struct{ Msg string }

func (e *GhError) Error() string { return e.Msg }

func ghErr(format string, a ...any) error { return &GhError{fmt.Sprintf(format, a...)} }

// RunFunc runs `gh api <args>` and returns its stdout, stderr and exit code. It is the process boundary: the
// default starts gh, tests replace it with canned output. A returned error means the process could not run.
type RunFunc func(ctx context.Context, args []string) (stdout, stderr []byte, exit int, err error)

func execGh(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "gh", append([]string{"api"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// Gh is the one door to GitHub: `gh api <args>` with a timeout. It counts calls for the summary.
type Gh struct {
	Timeout     time.Duration
	Calls       int
	SearchCalls int
	Run         RunFunc // nil = run the real gh
}

// NewGh returns a Gh with the production timeout.
func NewGh() *Gh { return &Gh{Timeout: GhTimeout} }

// API runs `gh api` and returns stdout. search marks a call against the search-API cap.
func (g *Gh) API(search bool, args ...string) (string, error) {
	if search {
		if g.SearchCalls >= MaxSearchCalls {
			return "", ghErr("search call cap (%d) reached", MaxSearchCalls)
		}
		g.SearchCalls++
	}
	g.Calls++
	what := "?"
	if len(args) > 0 {
		what = args[0]
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && strings.Contains(a, "/") {
			what = a
			break
		}
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = GhTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	run := g.Run
	if run == nil {
		run = execGh
	}
	stdout, stderr, code, err := run(ctx, args)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", ghErr("gh api %s: timed out after %d s", what, int(timeout.Seconds()))
	case errors.Is(err, exec.ErrNotFound):
		return "", ghErr("gh not found on PATH: install the GitHub CLI")
	case err != nil:
		return "", ghErr("gh api %s: %v", what, err)
	case code != 0:
		first := "no output"
		if lines := strings.Split(strings.TrimSpace(string(stderr)), "\n"); lines[0] != "" {
			first = lines[0]
		}
		return "", ghErr("gh api %s: exit %d: %s", what, code, runePrefix(strings.TrimSpace(first), 160))
	case !utf8.Valid(stdout): // never decode with replacement: a garbled file must not be scanned as readable
		return "", ghErr("gh api %s: non-UTF-8 output", what)
	}
	return string(stdout), nil
}

// JSON runs `gh api` and decodes the JSON answer (numbers stay json.Number).
func (g *Gh) JSON(search bool, args ...string) (any, error) {
	out, err := g.API(search, args...)
	if err != nil {
		return nil, err
	}
	v, ok := parseJSONNum(out)
	if !ok {
		return nil, ghErr("gh api %s: response is not JSON", args[0])
	}
	return v, nil
}

func parseJSONNum(text string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false // trailing data, as json.loads refuses
	}
	return v, true
}

// GhKey is gh_key: "gh:<repo>[/<path>]".
func GhKey(repo, path string) string {
	if path != "" {
		return "gh:" + repo + "/" + strings.Trim(path, "/")
	}
	return "gh:" + repo
}

func clip(text string) string {
	b := []byte(text)
	if len(b) > ReadmeMax {
		b = b[:ReadmeMax]
		for !utf8.Valid(b) {
			b = b[:len(b)-1] // drop the partial trailing rune (decode "ignore")
		}
	}
	return string(b)
}

// ReadmeExcerpt returns up to ReadmeMax bytes of the README (or SKILL.md) of a candidate at its head sha.
// Exactly one of text and errText is empty.
func ReadmeExcerpt(g *Gh, repo, path, sha string) (text, errText string) {
	if !repoRe.MatchString(repo) || !sha7Re.MatchString(sha) {
		return "", "no repo or head sha recorded"
	}
	base := "repos/" + repo + "/contents/"
	var ends []string
	if path != "" && pathRe.MatchString(path) {
		p := strings.Trim(path, "/")
		ends = []string{p + "/README.md", p + "/SKILL.md"}
	}
	for _, end := range ends {
		if out, err := g.API(false, append([]string{base + end + "?ref=" + sha}, Raw...)...); err == nil {
			return clip(out), ""
		}
	}
	out, err := g.API(false, append([]string{"repos/" + repo + "/readme?ref=" + sha}, Raw...)...)
	if err != nil {
		return "", err.Error()
	}
	return clip(out), ""
}

// orderedStrings is an insertion-ordered string map (a Python dict of str to str).
type orderedStrings struct {
	keys []string
	m    map[string]string
}

func newOrdered() *orderedStrings { return &orderedStrings{m: map[string]string{}} }
func (o *orderedStrings) get(k string) (string, bool) {
	v, ok := o.m[k]
	return v, ok
}
func (o *orderedStrings) set(k, v string) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// Stat is the per-source counters of a run.
type Stat struct{ New, Updated int }

// Run is the state of one fetch: db, gh, clock, caches, counters.
type Run struct {
	DB      *sql.DB
	Gh      *Gh
	Now     time.Time
	TS      string
	HNURL   string
	HNCalls int
	Stats   map[string]*Stat
	Errors  []string

	newFound, checks int
	repo             map[string]map[string]any
	head             map[string]string
	tree             map[[2]string]treeResult
	dropped          *orderedStrings
	soft             map[string][]string
	softOrder        []string
	httpClient       *http.Client
}

type treeResult struct {
	entries   []Entry
	truncated bool
}

// NewRun builds the state of one fetch. hnURL "" means the Algolia default.
func NewRun(db *sql.DB, gh *Gh, now time.Time, hnURL string) *Run {
	if hnURL == "" {
		hnURL = HNDefaultURL
	}
	r := &Run{DB: db, Gh: gh, Now: now, TS: Stamp(now), HNURL: hnURL, Stats: map[string]*Stat{},
		repo: map[string]map[string]any{}, head: map[string]string{}, tree: map[[2]string]treeResult{},
		dropped: newOrdered(), soft: map[string][]string{}, httpClient: &http.Client{Timeout: HNTimeout}}
	for _, s := range Sources {
		r.Stats[s] = &Stat{}
	}
	return r
}

func asMap(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }

func strOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

func intOf(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		f, err := x.Float64()
		return int64(f), err == nil
	case float64:
		return int64(x), true
	}
	return 0, false
}

func (r *Run) repoInfo(repo string, item map[string]any) (map[string]any, error) {
	if info, ok := r.repo[repo]; ok {
		return info, nil
	}
	info := item
	if info == nil {
		v, err := r.Gh.JSON(false, "repos/"+repo)
		if err != nil {
			return nil, err
		}
		var ok bool
		if info, ok = asMap(v); !ok {
			return nil, ghErr("repos/%s: response is not an object", repo)
		}
	}
	r.repo[repo] = info
	return info, nil
}

func (r *Run) headSha(repo, branch string) (string, error) {
	if s, ok := r.head[repo]; ok {
		return s, nil
	}
	out, err := r.Gh.API(false, "repos/"+repo+"/commits/"+branch, "--jq", ".sha")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if !sha40Re.MatchString(sha) {
		return "", ghErr("repos/%s: head commit of %s is not a sha", repo, branch)
	}
	r.head[repo] = sha
	return sha, nil
}

func (r *Run) treeOf(repo, sha string) (treeResult, error) {
	if t, ok := r.tree[[2]string{repo, sha}]; ok {
		return t, nil
	}
	v, err := r.Gh.JSON(false, "repos/"+repo+"/git/trees/"+sha+"?recursive=1")
	if err != nil {
		return treeResult{}, err
	}
	data, _ := asMap(v)
	list, ok := data["tree"].([]any)
	if !ok {
		return treeResult{}, ghErr("repos/%s: tree response has no tree list", repo)
	}
	entries, err := ParseTree(list)
	if err != nil {
		return treeResult{}, ghErr("repos/%s: %v", repo, err)
	}
	trunc, _ := data["truncated"].(bool)
	t := treeResult{entries, trunc}
	r.tree[[2]string{repo, sha}] = t
	return t, nil
}

// ParseTree converts the `tree` list of a git/trees answer into entries. An element that is not an object, or has
// no string path, is refused: the listing is not trusted to be well formed.
func ParseTree(list []any) ([]Entry, error) {
	out := make([]Entry, 0, len(list))
	for i, el := range list {
		m, ok := asMap(el)
		if !ok {
			return nil, fmt.Errorf("tree entry %d is not an object", i)
		}
		p, ok := m["path"].(string)
		if !ok {
			return nil, fmt.Errorf("tree entry %d has no string path", i)
		}
		size, _ := intOf(m["size"])
		out = append(out, Entry{Path: p, Mode: strOf(m["mode"]), Type: strOf(m["type"]), Sha: strOf(m["sha"]), Size: size})
	}
	return out, nil
}

// known looks a key up; ok is false when the candidate is new.
func (r *Run) known(key string) (id, status string, ok bool, err error) {
	nk, err := NormalizeKey(key)
	if err != nil {
		return "", "", false, err
	}
	err = r.DB.QueryRow("SELECT id, status FROM scout_candidates WHERE key=?", nk).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return id, status, err == nil, err
}

func (r *Run) count(source string, created bool) {
	if created {
		r.Stats[source].New++
	} else {
		r.Stats[source].Updated++
	}
}

type prep struct {
	info      map[string]any
	sha       string
	tree      []Entry
	truncated bool
}

func (r *Run) prepare(repo string, item map[string]any) (prep, error) {
	info, err := r.repoInfo(repo, item)
	if err != nil {
		return prep{}, err
	}
	branch := strOf(info["default_branch"])
	if branch == "" {
		branch = "main"
	}
	sha, err := r.headSha(repo, branch)
	if err != nil {
		return prep{}, err
	}
	t, err := r.treeOf(repo, sha)
	if err != nil {
		return prep{}, err
	}
	return prep{info, sha, t.entries, t.truncated}, nil
}

func (r *Run) facts(repo, path string, p prep) Facts {
	prefix := strings.Trim(path, "/")
	read := func(e Entry) (string, error) {
		out, err := r.Gh.API(false, append([]string{"repos/" + repo + "/git/blobs/" + e.Sha}, Raw...)...)
		if err != nil {
			return "", &ReadError{err.Error()}
		}
		return out, nil
	}
	res := Classify(p.tree, prefix, read, p.truncated)
	branch := strOf(p.info["default_branch"])
	if branch == "" {
		branch = "main"
	}
	u := "https://github.com/" + repo
	f := Facts{Kind: S(res.Kind), Repo: S(repo), HeadSha: S(p.sha), InstallRisk: S(res.Risk), RiskDetail: S(JSONList(res.Detail))}
	if prefix != "" {
		u += "/tree/" + branch + "/" + prefix
		f.Path = S(prefix)
	}
	f.URL = S(u)
	if lic, ok := asMap(p.info["license"]); ok {
		if s := strOf(lic["spdx_id"]); s != "" {
			f.Licence = S(s)
		}
	}
	if s := strOf(p.info["pushed_at"]); s != "" {
		f.LastCommit = S(s)
	}
	if s := strOf(p.info["default_branch"]); s != "" {
		f.DefaultBranch = S(s)
	}
	if n, ok := intOf(p.info["stargazers_count"]); ok {
		f.Stars = I(n)
	}
	return f
}

// Outcomes of Consider.
const (
	New      = "new"
	Updated  = "updated"
	Dropped  = "dropped"
	Deferred = "deferred"
	Failed   = "failed"
)

// Consider handles one sighting of a GitHub candidate. A known key only gets its source merged, seen_count and
// (when the sighting carries a star count) a star snapshot, unless force. An unknown key is inspected: repo facts,
// head sha, one tree call, install risk. discovery sources must find a manifest and share the NewCap and
// MaxChecks budgets. Returns New, Updated, Dropped or Deferred (budget spent; not stored).
func (r *Run) Consider(source, repo, path string, item map[string]any, force, discovery bool) (string, error) {
	if !repoRe.MatchString(repo) || (path != "" && (strings.Contains(path, "..") || !pathRe.MatchString(path))) {
		return "", ghErr("refusing odd repo/path %s %s", pyRepr(repo), pyRepr(path))
	}
	key := GhKey(repo, path)
	_, _, row, err := r.known(key)
	if err != nil {
		return "", err
	}
	if row && !force {
		cid, _, err := UpsertCandidate(r.DB, key, source, r.TS, Facts{})
		if err != nil {
			return "", err
		}
		if n, ok := intOf(item["stargazers_count"]); ok {
			if _, err := SnapshotStars(r.DB, cid, n, r.TS); err != nil {
				return "", err
			}
		}
		r.count(source, false)
		return Updated, nil
	}
	pushed, hasPushed := item["pushed_at"]
	if discovery && !row {
		if r.newFound >= NewCap || r.checks >= MaxChecks {
			return Deferred, nil
		}
		if memo, ok := r.dropped.get(strings.ToLower(repo)); ok && (!hasPushed || pushed == nil || strOf(pushed) == memo) {
			return Dropped, nil
		}
		r.checks++
	}
	p, err := r.prepare(repo, item)
	if err != nil {
		return "", err
	}
	if discovery && !row && !HasManifest(p.tree, path) {
		r.dropped.set(strings.ToLower(repo), strOf(p.info["pushed_at"]))
		return Dropped, nil
	}
	f := r.facts(repo, path, p)
	cid, created, err := UpsertCandidate(r.DB, key, source, r.TS, f)
	if err != nil {
		return "", err
	}
	if f.Stars != nil {
		if _, err := SnapshotStars(r.DB, cid, *f.Stars, r.TS); err != nil {
			return "", err
		}
	}
	r.count(source, created)
	if created && discovery {
		r.newFound++
	}
	if created {
		return New, nil
	}
	return Updated, nil
}

// Visit is Consider that survives one bad candidate (deleted repo, empty repo, odd link): a GhError is recorded
// for the summary and the source goes on with the next one. Database errors still stop the source.
func (r *Run) Visit(source, repo, path string, item map[string]any, force, discovery bool) (string, error) {
	out, err := r.Consider(source, repo, path, item, force, discovery)
	var ge *GhError
	if errors.As(err, &ge) {
		label := repo
		if path != "" {
			label += "/" + path
		}
		if _, ok := r.soft[source]; !ok {
			r.softOrder = append(r.softOrder, source)
		}
		r.soft[source] = append(r.soft[source], label+": "+ge.Msg)
		return Failed, nil
	}
	return out, err
}

func (r *Run) cursorJSON(source string) map[string]any {
	raw, ok, err := GetCursor(r.DB, source)
	if err != nil || !ok || raw == "" {
		return map[string]any{}
	}
	v, ok := parseJSONNum(raw)
	if m, isMap := asMap(v); ok && isMap {
		return m
	}
	return map[string]any{}
}

func (r *Run) setCursor(source string, data map[string]any) error {
	return SetCursor(r.DB, source, Dumps(data, 0, true), r.TS)
}

// -- a. anthropics/claude-plugins-official ----------------------------------------------------

func hash16(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// numberRepr renders a json.Number the way json.dumps prints the Python int or float it decodes to.
func numberRepr(n json.Number) string {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		return s
	}
	f, err := n.Float64()
	if err != nil {
		return s
	}
	out := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(out, ".eEn") {
		out += ".0"
	}
	return out
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		return rawJSON(numberRepr(x))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeNumbers(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeNumbers(e)
		}
		return out
	}
	return v
}

func fingerprint(name string, source any) string {
	return hash16(Dumps([]any{name, normalizeNumbers(source)}, 0, true))
}

func githubRepo(u string) string {
	m := linkRe.FindStringSubmatchIndex(u)
	if m == nil || m[0] != 0 {
		return ""
	}
	owner, repo := u[m[2]:m[3]], u[m[4]:m[5]]
	return owner + "/" + strings.TrimSuffix(repo, ".git")
}

// EntryTarget is entry_target: (repo, path) for a marketplace entry that is a GitHub repo or a path in the
// marketplace repo; ok false for anything else.
func EntryTarget(source any) (repo, path string, ok bool) {
	switch s := source.(type) {
	case string:
		p := pyTrim(s)
		if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || p == "." || p == "" {
			for strings.HasPrefix(p, "./") {
				p = p[2:]
			}
			p = strings.Trim(p, "/")
			if p == "." {
				return "", "", false
			}
			for _, seg := range strings.Split(p, "/") {
				if seg == ".." {
					return "", "", false
				}
			}
			return MarketplaceRepo, p, true
		}
	case map[string]any:
		repo := ""
		if rs := strOf(s["repo"]); repoRe.MatchString(rs) {
			repo = rs
		}
		if repo == "" {
			repo = githubRepo(strOf(s["url"]))
		}
		if repo != "" {
			sub := ""
			if p, isStr := s["path"].(string); isStr {
				sub = strings.Trim(p, "/")
			}
			return repo, sub, true
		}
	}
	return "", "", false
}

func (r *Run) headOf(repo, ref string) (string, error) {
	out, err := r.Gh.API(false, "repos/"+repo+"/commits/"+ref, "--jq", ".sha")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if !sha40Re.MatchString(sha) {
		return "", ghErr("%s: head commit is not a sha", repo)
	}
	return sha, nil
}

func srcMarketplace(r *Run) error {
	cur := r.cursorJSON("plugins-official")
	sha, err := r.headOf(MarketplaceRepo, "main")
	if err != nil {
		return err
	}
	if s, _ := cur["sha"].(string); sha == s {
		return nil
	}
	v, err := r.Gh.JSON(false, append([]string{"repos/" + MarketplaceRepo + "/contents/.claude-plugin/marketplace.json?ref=" + sha}, Raw...)...)
	if err != nil {
		return err
	}
	data, _ := asMap(v)
	plugins, ok := data["plugins"].([]any)
	if !ok {
		return ghErr("marketplace.json has no plugins list")
	}
	type entry struct {
		source any
		fp     string
	}
	entries := map[string]entry{}
	for _, p := range plugins {
		if m, ok := asMap(p); ok {
			if name, ok := m["name"].(string); ok {
				entries[name] = entry{m["source"], fingerprint(name, m["source"])}
			}
		}
	}
	before, _ := asMap(cur["entries"])
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		e := entries[name]
		if fp, _ := before[name].(string); fp == e.fp {
			continue
		}
		if repo, path, ok := EntryTarget(e.source); ok {
			_, force := before[name]
			if _, err := r.Visit("marketplace", repo, path, nil, force, false); err != nil {
				return err
			}
			continue
		}
		var u string
		if m, ok := asMap(e.source); ok {
			u = strOf(m["url"])
		}
		f := Facts{Kind: S("plugin"), InstallRisk: S("unknown")}
		if strings.HasPrefix(u, "http") {
			f.URL = S(u)
		}
		_, created, err := UpsertCandidate(r.DB, "mkt:claude-plugins-official/"+name, "marketplace", r.TS, f)
		if err != nil {
			return err
		}
		r.count("marketplace", created)
	}
	if len(r.soft["marketplace"]) > 0 {
		return nil // keep the old cursor: the failed entries are retried next run
	}
	old, _ := asMap(cur["hashes"])
	oldKeys := sortedKeys(old) // the cursor is stored with sorted keys, so file order is sorted order
	if len(oldKeys) > 4 {
		oldKeys = oldKeys[len(oldKeys)-4:]
	}
	hashes := map[string]any{}
	for _, k := range oldKeys {
		hashes[k] = old[k]
	}
	pairs := make([]any, 0, len(names))
	fps := map[string]any{}
	for _, n := range names {
		pairs = append(pairs, []any{n, entries[n].fp})
		fps[n] = entries[n].fp
	}
	hashes[sha] = hash16(pyDumps(pairs))
	return r.setCursor("plugins-official", map[string]any{"sha": sha, "entries": fps, "hashes": hashes})
}

// -- b. anthropics/skills ------------------------------------------------------------------------

func srcSkills(r *Run) error {
	cur := r.cursorJSON("anthropics-skills")
	sha, err := r.headOf(SkillsRepo, "main")
	if err != nil {
		return err
	}
	if s, _ := cur["sha"].(string); sha == s {
		return nil
	}
	t, err := r.treeOf(SkillsRepo, sha)
	if err != nil {
		return err
	}
	r.head[SkillsRepo] = sha // same commit: the facts for each new skill reuse this tree
	seen := map[string]bool{}
	var names []string
	for _, e := range t.entries {
		if skillsRe.MatchString(e.Path) && e.Type != "tree" {
			if n := strings.Split(e.Path, "/")[1]; !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	for _, name := range names {
		_, _, known, err := r.known(GhKey(SkillsRepo, "skills/"+name))
		if err != nil {
			return err
		}
		if !known {
			if _, err := r.Visit("skills", SkillsRepo, "skills/"+name, nil, false, false); err != nil {
				return err
			}
		}
	}
	if len(r.soft["skills"]) > 0 {
		return nil
	}
	return r.setCursor("anthropics-skills", map[string]any{"sha": sha})
}

// -- c. GitHub repository search ---------------------------------------------------------------------

// SearchQueries is search_queries: the topic searches of the day, in fixed order.
func SearchQueries(now time.Time) []string {
	d14 := now.AddDate(0, 0, -14).Format("2006-01-02")
	d2 := now.AddDate(0, 0, -2).Format("2006-01-02")
	var out []string
	for _, flt := range []string{"created:>=" + d14 + " stars:>=25", "pushed:>=" + d2 + " stars:>=200"} {
		for _, t := range Topics {
			out = append(out, "topic:"+t+" "+flt)
		}
	}
	return out
}

func srcSearch(r *Run) error {
	cur := r.cursorJSON("gh-search")
	queries := SearchQueries(r.Now)
	var off int
	if n, ok := intOf(cur["qoff"]); ok {
		off = int(n)
	}
	off = ((off % len(queries)) + len(queries)) % len(queries)
	count := min(MaxSearchCalls, len(queries))
	type item struct {
		m     map[string]any
		stars int64
	}
	seen := map[string]bool{}
	var found []item
	for i := 0; i < count; i++ {
		q := queries[(off+i)%len(queries)]
		v, err := r.Gh.JSON(true, "-X", "GET", "search/repositories", "-f", "q="+q, "-f", "sort=stars", "-f", "per_page=30")
		if err != nil {
			return err
		}
		data, ok := asMap(v)
		if !ok {
			return ghErr("search response is not an object")
		}
		items, _ := data["items"].([]any)
		for _, el := range items {
			m, ok := asMap(el)
			if !ok {
				continue
			}
			full := strOf(m["full_name"])
			if repoRe.MatchString(full) && !seen[strings.ToLower(full)] {
				seen[strings.ToLower(full)] = true
				stars, _ := intOf(m["stargazers_count"])
				found = append(found, item{m, stars})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].stars > found[j].stars })
	for _, it := range found {
		if _, err := r.Visit("search", strOf(it.m["full_name"]), "", it.m, false, true); err != nil {
			return err
		}
	}
	return r.setCursor("gh-search", map[string]any{"qoff": (off + count) % len(queries)})
}

// -- d. awesome lists ---------------------------------------------------------------------------------

// ReadmeLinks is readme_links: lower-cased owner/repo pairs linked from a README, in order, without the list's own repo.
func ReadmeLinks(text, own string) []string {
	var out []string
	for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
		owner, repo := m[1], strings.TrimRight(m[2], ".")
		repo = strings.TrimSuffix(repo, ".git")
		full := strings.ToLower(owner + "/" + repo)
		if notOwners[strings.ToLower(owner)] || full == strings.ToLower(own) || repo == "" || contains(out, full) {
			continue
		}
		out = append(out, full)
	}
	return out
}

func srcAwesome(r *Run) error {
	var errs []string
	for _, lst := range Awesome {
		if err := awesomeOne(r, lst); err != nil {
			errs = append(errs, lst+": "+err.Error()) // one list failing does not stop the others
		}
	}
	if len(errs) > 0 {
		return ghErr("%s", strings.Join(errs, "; "))
	}
	return nil
}

func awesomeOne(r *Run, lst string) error {
	name := "awesome:" + strings.ToLower(lst)
	cur := r.cursorJSON(name)
	sha, err := r.headOf(lst, "HEAD")
	if err != nil {
		return err
	}
	curSha, _ := cur["sha"].(string)
	if sha == curSha {
		return nil
	}
	text, err := r.Gh.API(false, append([]string{"repos/" + lst + "/readme?ref=" + sha}, Raw...)...)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	done := map[string]bool{}
	if links, ok := cur["links"].([]any); ok {
		for _, l := range links {
			if s, ok := l.(string); ok {
				seen[s], done[s] = true, true
			}
		}
	}
	deferred := false
	for _, link := range ReadmeLinks(text, lst) {
		if seen[link] {
			continue
		}
		out, err := r.Visit("awesome", link, "", nil, false, true)
		if err != nil {
			return err
		}
		if out == Deferred {
			deferred = true
		} else {
			done[link] = true
		}
	}
	// links left over by the budget stay "added": keep the old sha so the next run reads the README again
	var keep any = sha
	if deferred {
		keep = cur["sha"] // nil when there was none
	}
	links := make([]string, 0, len(done))
	for l := range done {
		links = append(links, l)
	}
	sort.Strings(links)
	return r.setCursor(name, map[string]any{"sha": keep, "links": links})
}

// -- e. Hacker News (Algolia) ----------------------------------------------------------------------------

// hnQueryURL is the request URL of one query: the base URL plus the urlencoded params (comma kept, like safe=",").
func hnQueryURL(base, q string, since int64) string {
	params := [][2]string{{"query", q}, {"tags", "story"}, {"numericFilters", fmt.Sprintf("created_at_i>%d,points>=%d", since, HNMinPoints)}}
	var parts []string
	for _, p := range params {
		parts = append(parts, url.QueryEscape(p[0])+"="+strings.ReplaceAll(url.QueryEscape(p[1]), "%2C", ","))
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + strings.Join(parts, "&")
}

func (r *Run) hnGet(q string, since int64) (map[string]any, error) {
	full := hnQueryURL(r.HNURL, q, since)
	u, err := url.Parse(full)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "file") {
		return nil, fmt.Errorf("FACTORY_HN_URL must be an http(s) or file URL, got %s", pyRepr(r.HNURL))
	}
	r.HNCalls++
	var body []byte
	if u.Scheme == "file" {
		name := u.Path
		if u.RawQuery != "" {
			name += "?" + u.RawQuery // urlopen keeps the query in the file name too
		}
		if body, err = os.ReadFile(name); err != nil {
			return nil, ghErr("hacker news: %v", err)
		}
	} else {
		resp, err := r.httpClient.Get(full)
		if err != nil {
			return nil, ghErr("hacker news: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, ghErr("hacker news: HTTP Error %d: %s", resp.StatusCode, resp.Status)
		}
		if body, err = io.ReadAll(io.LimitReader(resp.Body, maxHNBody)); err != nil {
			return nil, ghErr("hacker news: %v", err)
		}
	}
	v, ok := parseJSONNum(decodeReplace(body))
	if !ok {
		return nil, fmt.Errorf("hacker news: response is not JSON")
	}
	m, ok := asMap(v)
	if !ok {
		return nil, ghErr("hacker news: response is not an object")
	}
	return m, nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func srcHN(r *Run) error {
	cur, _, err := GetCursor(r.DB, "hn")
	if err != nil {
		return err
	}
	since := r.Now.Unix() - HNFirstLookbackDays*86400
	if isDigits(cur) {
		if n, err := strconv.ParseInt(cur, 10, 64); err == nil {
			since = n
		}
	}
	newest := since
	var repos []string
	byLower := map[string]bool{}
	for _, q := range HNQueries {
		data, err := r.hnGet(q, since)
		if err != nil {
			return err
		}
		hits, _ := data["hits"].([]any)
		for _, h := range hits {
			hit, ok := asMap(h)
			if !ok {
				continue
			}
			if n, ok := intOf(hit["created_at_i"]); ok && n > newest {
				newest = n
			}
			if repo := githubRepo(strOf(hit["url"])); repo != "" && repoRe.MatchString(repo) && !byLower[strings.ToLower(repo)] {
				byLower[strings.ToLower(repo)] = true
				repos = append(repos, repo)
			}
		}
	}
	for _, repo := range repos {
		if _, err := r.Visit("hn", repo, "", nil, false, true); err != nil {
			return err
		}
	}
	return SetCursor(r.DB, "hn", strconv.FormatInt(newest, 10), r.TS)
}

// -- driver -------------------------------------------------------------------------------------------------

type runner struct {
	name string
	fn   func(*Run) error
}

var runners = []runner{{"marketplace", srcMarketplace}, {"skills", srcSkills}, {"search", srcSearch}, {"awesome", srcAwesome}, {"hn", srcHN}}

// Fetch runs all sources in order and returns the Run (stats, errors, call counts). Each candidate is written as
// it is processed; a failing source leaves its cursor untouched so the next run retries it.
func Fetch(db *sql.DB, now time.Time, gh *Gh, hnURL string) (*Run, error) {
	if gh == nil {
		gh = NewGh()
	}
	r := NewRun(db, gh, now, hnURL)
	memo := r.cursorJSON("dropped")
	for _, k := range sortedKeys(memo) {
		if s, ok := memo[k].(string); ok {
			r.dropped.set(k, s)
		}
	}
	for _, rn := range runners {
		if err := rn.fn(r); err != nil {
			r.Errors = append(r.Errors, rn.name+": "+runePrefix(strings.Join(pyFields(err.Error()), " "), 200))
		}
	}
	for _, name := range r.softOrder {
		msgs := r.soft[name]
		r.Errors = append(r.Errors, fmt.Sprintf("%s: %d candidate(s) failed, first: %s", name, len(msgs),
			runePrefix(strings.Join(pyFields(msgs[0]), " "), 160)))
	}
	keys := r.dropped.keys
	if len(keys) > DroppedMax {
		keys = keys[len(keys)-DroppedMax:]
	}
	keep := map[string]any{}
	for _, k := range keys {
		keep[k] = r.dropped.m[k]
	}
	return r, r.setCursor("dropped", keep)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Summary is summary(): the one-line fetch report.
func Summary(r *Run) string {
	var n, u int
	stats := map[string]any{}
	for s, st := range r.Stats {
		n, u = n+st.New, u+st.Updated
		stats[s] = map[string]any{"new": st.New, "updated": st.Updated}
	}
	return fmt.Sprintf("fetch: new %d, updated %d, per-source %s, gh calls %d (search %d), hn calls %d, errors %s",
		n, u, Dumps(stats, 0, true), r.Gh.Calls, r.Gh.SearchCalls, r.HNCalls, pyDumps(r.Errors))
}
