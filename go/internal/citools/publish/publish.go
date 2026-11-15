// Package publish is the Go port of ci/publish_repo.py: apply the public-repository metadata
// (ci/publish_metadata.json) to the NEW public repository, by name.
//
//	ci-publish-repo --repo OWNER/NAME [--apply] [--gh PATH] [--private-repo OWNER/NAME]
//
// The default is a DRY RUN: it prints every `gh api` call it would make, with its JSON body, and starts no process
// that talks to GitHub. --apply runs the calls with --gh (default "gh"), in order, and stops at the first failure.
// Exit 0 = done, 1 = a call failed, 2 = refusal.
//
// Refused before any call, in both modes: --repo that is not OWNER/NAME; --repo equal to a repo in the config key
// frozen_repos of a live factory home or to --private-repo; metadata that still names such a slug or that the
// scrub scanner flags. With --apply the repository must already be public and not a fork. See docs/PUBLISHING.md.
package publish

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"factory/go/internal/citools/freshexport"
	_ "modernc.org/sqlite"
)

const social = "Upload docs/images/social-preview.png by hand: Settings > General > Social preview."

type refused struct{ msg string }

func (r refused) Error() string { return r.msg }

func refuse(format string, a ...any) error { return refused{fmt.Sprintf(format, a...)} }

// Meta is the content of ci/publish_metadata.json.
type Meta struct {
	Description    string
	Homepage       string
	Topics         []string
	RequiredChecks []string
}

// kv/obj keep the key order of a JSON object, like a Python dict.
type kv struct {
	k string
	v any
}
type obj []kv

// Call is one `gh api` call: method, path and an optional ordered body.
type Call struct {
	Method, Path string
	Body         any // nil, obj
}

// dump renders v like Python's json.dumps (", " and ": " separators, ASCII only).
func dump(v any) string {
	var b strings.Builder
	writeJSON(&b, v)
	return b.String()
}

func writeJSON(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		b.WriteString(asciiString(x))
	case []string:
		b.WriteByte('[')
		for i, s := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(asciiString(s))
		}
		b.WriteByte(']')
	case obj:
		b.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(asciiString(e.k))
			b.WriteString(": ")
			writeJSON(b, e.v)
		}
		b.WriteByte('}')
	default:
		raw, _ := json.Marshal(x)
		b.Write(raw)
	}
}

func asciiString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	raw := strings.TrimSuffix(buf.String(), "\n")
	var out strings.Builder
	for _, r := range raw {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&out, "\\u%04x\\u%04x", 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&out, "\\u%04x", r)
		}
	}
	return out.String()
}

func parseSlug(text, flagName string) (string, string, error) {
	owner, name, err := freshexport.ParsePublicRepo(text)
	if err != nil {
		return "", "", refuse("%s %s is not OWNER/NAME (letters, digits, '-', '.', '_'; exactly one '/'). Example: --repo volekerb/hivemill.", flagName, pyRepr(text))
	}
	if strings.HasSuffix(strings.ToLower(name), ".git") {
		name = name[:len(name)-4] // the origin slugs are compared without .git
		if name == "" || name == "." || name == ".." {
			return "", "", refuse("%s %s is not OWNER/NAME. Example: --repo volekerb/hivemill.", flagName, pyRepr(text))
		}
	}
	return owner, name, nil
}

// frozenRepoSlugs are the lower-cased OWNER/NAME of every repo in the config key frozen_repos of a live factory
// home's DB (read only).
func frozenRepoSlugs() (map[string]bool, error) {
	slugs := map[string]bool{}
	var homes []string
	for h := range freshexport.LiveHomes() {
		homes = append(homes, h)
	}
	sort.Strings(homes)
	for _, home := range homes {
		db := filepath.Join(home, "db", "factory.db")
		if st, err := os.Stat(db); err != nil || !st.Mode().IsRegular() {
			continue
		}
		value, err := readFrozen(db)
		if err != nil {
			return nil, refuse("cannot read config frozen_repos from %s: %v. Fix the database, or pass --private-repo OWNER/NAME and retry once the database is readable.", db, err)
		}
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				o, n, err := parseSlug(item, "config frozen_repos")
				if err != nil {
					return nil, err
				}
				slugs[strings.ToLower(o+"/"+n)] = true
			}
		}
	}
	return slugs, nil
}

func readFrozen(db string) (string, error) {
	con, err := sql.Open("sqlite", "file:"+db+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return "", err
	}
	defer con.Close()
	var v sql.NullString
	err = con.QueryRow("SELECT value FROM config WHERE key='frozen_repos'").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v.String, err
}

var metaKeys = []string{"description", "homepage", "topics", "required_checks"}

// LoadMetadata reads and validates the metadata file.
func LoadMetadata(path string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(path)
	if err != nil {
		return m, refuse("cannot read metadata %s: %v. Check --metadata.", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return m, refuse("cannot read metadata %s: %v. Check --metadata.", path, err)
	}
	for _, k := range metaKeys {
		if _, ok := raw[k]; !ok {
			return m, refuse("metadata %s must be an object with the keys %s.", path, strings.Join(metaKeys, ", "))
		}
	}
	bad := refuse("metadata %s: description and homepage are strings, topics and required_checks non-empty lists of strings.", path)
	if json.Unmarshal(raw["description"], &m.Description) != nil || json.Unmarshal(raw["homepage"], &m.Homepage) != nil ||
		json.Unmarshal(raw["topics"], &m.Topics) != nil || json.Unmarshal(raw["required_checks"], &m.RequiredChecks) != nil ||
		len(m.Topics) == 0 || len(m.RequiredChecks) == 0 {
		return m, bad
	}
	return m, nil
}

// Render substitutes the slug tokens of the metadata.
func Render(m Meta, owner, name string) Meta {
	tokenRepo, tokenOwner := freshexport.TokenRepo, freshexport.TokenOwner
	sub := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, tokenRepo, owner+"/"+name), tokenOwner, owner)
	}
	out := Meta{Description: sub(m.Description), Homepage: sub(m.Homepage)}
	for _, t := range m.Topics {
		out.Topics = append(out.Topics, sub(t))
	}
	for _, c := range m.RequiredChecks {
		out.RequiredChecks = append(out.RequiredChecks, sub(c))
	}
	return out
}

func (m Meta) ordered() obj {
	return obj{{"description", m.Description}, {"homepage", m.Homepage}, {"topics", m.Topics}, {"required_checks", m.RequiredChecks}}
}

// scrubCheck runs the scanner on a temp copy of the rendered metadata; a hit (or a scanner failure) is a refusal.
func scrubCheck(ciDir string, m Meta) error {
	dir, err := os.MkdirTemp("", "publish-meta-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "publish_metadata.json")
	pretty := strings.ReplaceAll(dump(m.ordered()), ", \"", ",\n  \"")
	if err := os.WriteFile(f, []byte(pretty), 0o600); err != nil {
		return err
	}
	out, stderr, rc, err := freshexport.ScanPython(ciDir, false, "--path", f)
	if err != nil {
		return err
	}
	switch rc {
	case 0:
		return nil
	case 1:
		return refuse("the rendered metadata has scrub-scan hits (masked below); remove them from the metadata:\n%s", strings.TrimSpace(out+stderr))
	}
	tail := strings.TrimSpace(stderr)
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	return refuse("ci/scrub_scan.py failed (exit %d): %s. Fix the scanner error and rerun.", rc, tail)
}

// Plan is the six calls, in order. The URL above each names the REST page it was checked against.
func Plan(repo string, m Meta) []Call {
	r := "repos/" + repo
	return []Call{
		// https://docs.github.com/en/rest/repos/repos#get-a-repository ("private", "fork")
		{"GET", r, nil},
		// https://docs.github.com/en/rest/repos/repos#update-a-repository
		{"PATCH", r, obj{{"description", m.Description}, {"homepage", m.Homepage}, {"has_issues", true},
			{"has_discussions", true}, {"has_wiki", false}, {"has_projects", false}}},
		// https://docs.github.com/en/rest/repos/repos#replace-all-repository-topics
		{"PUT", r + "/topics", obj{{"names", m.Topics}}},
		// https://docs.github.com/en/rest/repos/repos#enable-vulnerability-alerts
		{"PUT", r + "/vulnerability-alerts", nil},
		// https://docs.github.com/en/rest/repos/repos#enable-private-vulnerability-reporting-for-a-repository
		{"PUT", r + "/private-vulnerability-reporting", nil},
		// https://docs.github.com/en/rest/branches/branch-protection#update-branch-protection
		// R-a (owner rule, T-0638): PR required with 0 approvals, enforce_admins ON so the owner's own login cannot push to main either
		{"PUT", r + "/branches/main/protection", obj{
			{"required_status_checks", obj{{"strict", true}, {"contexts", m.RequiredChecks}}},
			{"enforce_admins", true},
			{"required_pull_request_reviews", obj{{"required_approving_review_count", 0}}},
			{"restrictions", nil},
			{"allow_force_pushes", false}, {"allow_deletions", false}}},
	}
}

func show(w io.Writer, c Call) {
	fmt.Fprintf(w, "gh api -X %s %s\n", c.Method, c.Path)
	if c.Body != nil {
		fmt.Fprintf(w, "  body: %s\n", dump(c.Body))
	}
}

func lastN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func runCall(gh string, c Call) (stdout, stderr string, rc int) {
	argv := []string{"api", "-X", c.Method, c.Path}
	input := ""
	if c.Body != nil {
		argv = append(argv, "--input", "-")
		input = dump(c.Body)
	}
	cmd := exec.Command(gh, argv...)
	cmd.Stdin = strings.NewReader(input)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return o.String(), e.String(), 0
	case errors.As(err, &ee):
		return o.String(), e.String(), ee.ExitCode()
	}
	return "", fmt.Sprintf("cannot run %s: %v", gh, err), 127
}

func apply(gh string, calls []Call, stdout, stderr io.Writer) int {
	var done []string
	for i, c := range calls {
		label := c.Method + " " + c.Path
		out, errText, rc := runCall(gh, c)
		if rc != 0 {
			fmt.Fprintf(stderr, "publish_repo: FAILED %s (exit %d): %s\n", label, rc, lastN(errText, 300))
			list := "none"
			if len(done) > 0 {
				list = strings.Join(done, ", ")
			}
			fmt.Fprintf(stderr, "publish_repo: calls already made: %s. Fix the cause and rerun; every call is idempotent.\n", list)
			return 1
		}
		if i == 0 {
			var info map[string]any
			pv, ok1 := any(nil), false
			fv, ok2 := any(nil), false
			if json.Unmarshal([]byte(out), &info) == nil {
				pv, ok1 = info["private"]
				fv, ok2 = info["fork"]
			}
			if !ok1 || !ok2 {
				fmt.Fprintf(stderr, "publish_repo: cannot read private/fork from the answer of %s; no change was made.\n", label)
				return 1
			}
			if pv != false || fv != false { // Python: `is not False`
				fmt.Fprintln(stderr, "publish_repo: refused: the repository is private or a fork. Make the repository public first; "+
					"branch protection is only for the public repository. No change was made.")
				return 2
			}
		}
		done = append(done, label)
		fmt.Fprintf(stdout, "ok: %s\n", label)
	}
	return 0
}

// containsSlug reports whether low holds slug on a boundary: not preceded or followed by [a-z0-9._-].
func containsSlug(low, slug string) bool {
	isTok := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
	}
	for pos := 0; ; {
		i := strings.Index(low[pos:], slug)
		if i < 0 {
			return false
		}
		s, e := pos+i, pos+i+len(slug)
		if (s == 0 || !isTok(low[s-1])) && (e == len(low) || !isTok(low[e])) {
			return true
		}
		pos = s + 1
	}
}

type options struct {
	repo, gh, privateRepo, metadata, ciDir string
	apply                                  bool
}

func run(o options, stdout, stderr io.Writer) (int, error) {
	owner, name, err := parseSlug(o.repo, "--repo")
	if err != nil {
		return 0, err
	}
	repo := owner + "/" + name
	forbidden, err := frozenRepoSlugs()
	if err != nil {
		return 0, err
	}
	if o.privateRepo != "" {
		po, pn, err := parseSlug(o.privateRepo, "--private-repo")
		if err != nil {
			return 0, err
		}
		forbidden[strings.ToLower(po+"/"+pn)] = true
	}
	if forbidden[strings.ToLower(repo)] {
		return 0, refuse("--repo %s is the private repository. Never run this against the private repository; its main must stay unprotected.", repo)
	}
	ciDir := o.ciDir
	if ciDir == "" {
		if ciDir, err = freshexport.FindCIDir("."); err != nil {
			return 0, err
		}
	}
	metaPath := o.metadata
	if metaPath == "" {
		metaPath = filepath.Join(ciDir, "publish_metadata.json")
	}
	raw, err := LoadMetadata(metaPath)
	if err != nil {
		return 0, err
	}
	low := strings.ToLower(dump(raw.ordered())) // before the tokens are replaced, so the public slug itself cannot match
	var slugs []string
	for s := range forbidden {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if containsSlug(low, slug) {
			return 0, refuse("the metadata still contains the private repository slug; remove it from the metadata.")
		}
	}
	meta := Render(raw, owner, name)
	if err := scrubCheck(ciDir, meta); err != nil {
		return 0, err
	}
	calls := Plan(repo, meta)
	if !o.apply {
		fmt.Fprintf(stdout, "publish_repo: DRY RUN for %s; nothing is called. Add --apply to make these calls.\n", repo)
		for _, c := range calls {
			show(stdout, c)
		}
		fmt.Fprintln(stdout, social)
		return 0, nil
	}
	rc := apply(o.gh, calls, stdout, stderr)
	if rc == 0 {
		fmt.Fprintln(stdout, social)
	}
	return rc, nil
}

// Main runs the tool and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("publish_repo.py", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var o options
	fl.StringVar(&o.repo, "repo", "", "the PUBLIC repository, OWNER/NAME")
	fl.BoolVar(&o.apply, "apply", false, "make the calls (default: print them only)")
	fl.StringVar(&o.gh, "gh", "gh", "gh executable (default: gh); tests pass a stub")
	fl.StringVar(&o.privateRepo, "private-repo", "", "the private repository OWNER/NAME, refused as --repo")
	fl.StringVar(&o.metadata, "metadata", "", "metadata file (default ci/publish_metadata.json)")
	fl.StringVar(&o.ciDir, "ci-dir", "", "directory holding scrub_scan.py and publish_metadata.json (default: found from the working directory)")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if o.repo == "" || fl.NArg() > 0 {
		fmt.Fprintln(stderr, "publish_repo.py: error: the following arguments are required: --repo")
		return 2
	}
	rc, err := run(o, stdout, stderr)
	if err != nil {
		var r refused
		if errors.As(err, &r) {
			fmt.Fprintf(stderr, "publish_repo: refused: %s\n", r.msg)
		} else {
			fmt.Fprintf(stderr, "publish_repo: refused: %v\n", err)
		}
		return 2
	}
	return rc
}

// pyRepr quotes like Python's repr for the plain strings of a slug.
func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}
