// Package lanecheck answers "do these branches really conflict" with git's own merge machinery
// (`git merge-tree --write-tree`), per pair of refs and per pair of open PR heads. It is read-only: no working tree
// is touched, nothing is committed or pushed, and the private refs it fetches are deleted before it returns.
// Every git call goes through gitx (hooks, fsmonitor, prompts and the system config off).
package lanecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
)

// Exit codes: the contract docs/lanes.md states and callers quote.
const (
	ExitClean    = 0
	ExitError    = 2
	ExitConflict = 3
)

// RefPrefix is where PR heads are fetched; each run uses its own RefPrefix/<pid>/ namespace.
const RefPrefix = "refs/lane-check/"

const (
	gitTimeout   = 120 * time.Second
	fetchTimeout = 300 * time.Second
	ghTimeout    = 120 * time.Second
	prLimit      = 1000
)

// PR is one open pull request head from `gh pr list`.
type PR struct {
	Number int    `json:"number"`
	Branch string `json:"headRefName"`
	OID    string `json:"headRefOid"`
}

var (
	oidRe  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	repoRe = regexp.MustCompile(`^([A-Za-z0-9.-]+/)?[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)
)

// ParsePRList decodes the JSON of `gh pr list --json number,headRefName,headRefOid`, sorted by number. A row
// without a positive number or a full hex head oid, or a number listed twice, is an error: the caller cannot decide.
func ParsePRList(data []byte) ([]PR, error) {
	var prs []PR
	if err := json.Unmarshal(data, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: bad JSON: %w", err)
	}
	seen := map[int]bool{}
	for _, p := range prs {
		if p.Number <= 0 {
			return nil, fmt.Errorf("gh pr list: row without a positive PR number (%d)", p.Number)
		}
		if !oidRe.MatchString(p.OID) {
			return nil, fmt.Errorf("gh pr list: PR %d has no valid headRefOid (%q)", p.Number, p.OID)
		}
		if seen[p.Number] {
			return nil, fmt.Errorf("gh pr list: PR %d listed twice", p.Number)
		}
		seen[p.Number] = true
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })
	return prs, nil
}

// Checker runs the checks in one repository directory.
type Checker struct {
	Dir    string       // any directory inside the repository (a worktree is fine)
	Git    gitx.Options // hardened git options; Timeout is set per call
	Base   string       // ref the "touched files" of a side are measured against (three-dot diff)
	Remote string       // git remote the PR heads are fetched from
	Repo   string       // owner/name for gh -R; "" = gh's default for Dir
	Stderr io.Writer
}

func (c *Checker) git(timeout time.Duration, input string, args ...string) (gitx.Result, error) {
	o := c.Git
	o.Timeout, o.Input = timeout, input
	r, err := gitx.Run(c.Dir, o, args...)
	if err != nil {
		return r, fmt.Errorf("git %s: %w", args[0], err)
	}
	return r, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// enterRoot points Dir at the top of the working tree: git merge-tree prints conflicted paths relative to the
// directory it runs in, and every path this tool prints or compares is relative to the repository root. A bare
// repository has no top level and already answers root-relative, so Dir stays.
func (c *Checker) enterRoot() error {
	r, err := c.git(gitTimeout, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if top := strings.TrimRight(r.Stdout, "\n"); r.ExitCode == 0 && top != "" {
		c.Dir = top
	}
	return nil
}

// Resolve turns a ref into a commit oid. The error names the ref.
func (c *Checker) Resolve(ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("bad ref %q: a ref may not be empty or start with '-'", ref)
	}
	r, err := c.git(gitTimeout, "", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(r.Stdout)
	if r.ExitCode != 0 || !oidRe.MatchString(oid) {
		msg := firstLine(r.Stderr)
		if msg == "" {
			msg = "not a commit in this repository (git rev-parse --verify failed)"
		}
		return "", fmt.Errorf("bad ref %q: %s", ref, msg)
	}
	return oid, nil
}

func splitNUL(s string) []string {
	var out []string
	for _, f := range strings.Split(s, "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func sortedSet(in []string) []string {
	m := map[string]bool{}
	for _, s := range in {
		m[s] = true
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Conflicts merges a and b in memory (merge base found by git) and returns the conflicted paths, sorted;
// empty means git can merge the two. Any other git outcome is an error, never "clean".
func (c *Checker) Conflicts(a, b string) ([]string, error) {
	r, err := c.git(gitTimeout, "", "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", a, b)
	if err != nil {
		return nil, err
	}
	switch r.ExitCode {
	case 0:
		return nil, nil
	case 1:
		fields := splitNUL(r.Stdout)
		if len(fields) < 2 {
			return nil, fmt.Errorf("git merge-tree %s %s: exit 1 without a conflicted file list", a, b)
		}
		return sortedSet(fields[1:]), nil
	}
	msg := firstLine(r.Stderr)
	if r.ExitCode == 129 || strings.Contains(r.Stderr, "unknown option") || strings.Contains(r.Stderr, "usage: git merge-tree") {
		msg += " (lane-check needs git >= 2.38 for `git merge-tree --write-tree`; upgrade git)"
	}
	return nil, fmt.Errorf("git merge-tree %s %s: exit %d: %s", a, b, r.ExitCode, msg)
}

// Touched lists the paths oid changes since its merge base with Base (what a PR of oid onto Base shows), sorted.
// A rename counts as both the old and the new path.
func (c *Checker) Touched(baseOID, oid string) ([]string, error) {
	r, err := c.git(gitTimeout, "", "diff", "--name-only", "--no-renames", "-z", baseOID+"..."+oid, "--")
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		return nil, fmt.Errorf("git diff %s...%s: exit %d: %s", c.Base, oid, r.ExitCode, firstLine(r.Stderr))
	}
	return sortedSet(splitNUL(r.Stdout)), nil
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range a {
		in[s] = true
	}
	var out []string
	for _, s := range b {
		if in[s] {
			out = append(out, s)
		}
	}
	return out
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

// quote keeps a path one token on the one-line outputs: plain paths print as they are, others Go-quoted.
func quote(p string) string {
	for _, r := range p {
		if r <= ' ' || r == '"' || r == '\\' || r == 0x7f {
			return strconv.Quote(p)
		}
	}
	return p
}

func joinPaths(ps []string) string {
	q := make([]string, len(ps))
	for i, p := range ps {
		q[i] = quote(p)
	}
	return strings.Join(q, " ")
}

// Pair checks two refs and prints the verdict line and the "both touch:" information line.
func (c *Checker) Pair(stdout io.Writer, refA, refB string) (int, error) {
	a, err := c.Resolve(refA)
	if err != nil {
		return ExitError, err
	}
	b, err := c.Resolve(refB)
	if err != nil {
		return ExitError, err
	}
	base, err := c.Resolve(c.Base)
	if err != nil {
		return ExitError, fmt.Errorf("--base: %w", err)
	}
	conf, err := c.Conflicts(a, b)
	if err != nil {
		return ExitError, err
	}
	ta, err := c.Touched(base, a)
	if err != nil {
		return ExitError, err
	}
	tb, err := c.Touched(base, b)
	if err != nil {
		return ExitError, err
	}
	both := "(none)"
	if s := intersect(ta, tb); len(s) > 0 {
		both = joinPaths(s)
	}
	if len(conf) == 0 {
		fmt.Fprintln(stdout, "CLEAN")
		fmt.Fprintln(stdout, "both touch: "+both)
		return ExitClean, nil
	}
	fmt.Fprintln(stdout, "CONFLICT: "+joinPaths(conf))
	fmt.Fprintln(stdout, "both touch: "+both)
	return ExitConflict, nil
}

// ListOpen is the one `gh pr list` call.
func (c *Checker) ListOpen() ([]PR, error) {
	args := []string{"pr", "list", "--state", "open", "--json", "number,headRefName,headRefOid", "--limit", strconv.Itoa(prLimit)}
	if c.Repo != "" {
		if !repoRe.MatchString(c.Repo) {
			return nil, fmt.Errorf("bad --repo %q: want owner/name", c.Repo)
		}
		args = append(args, "-R", c.Repo)
	}
	r, err := ghx.Run(ghx.GitEnv(c.Git), "", ghTimeout, args...)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w (is gh installed and on PATH?)", err)
	}
	if r.ExitCode != 0 {
		return nil, fmt.Errorf("gh pr list: exit %d: %s", r.ExitCode, firstLine(r.Stderr))
	}
	prs, err := ParsePRList([]byte(r.Stdout))
	if err != nil {
		return nil, err
	}
	if len(prs) >= prLimit {
		return nil, fmt.Errorf("gh pr list: %d or more open PRs; the listing may be cut off, so no answer is given", prLimit)
	}
	return prs, nil
}

// fetched is a PR head available locally under a private ref.
type fetched struct {
	PR
	touched []string
}

// fetchHeads fetches refs/pull/<n>/head of every PR into ns (one fetch) and checks each against the listed oid.
func (c *Checker) fetchHeads(ns string, prs []PR) error {
	if len(prs) == 0 {
		return nil
	}
	if c.Remote == "" || strings.HasPrefix(c.Remote, "-") {
		return fmt.Errorf("bad --remote %q", c.Remote)
	}
	args := []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", c.Remote}
	for _, p := range prs {
		args = append(args, fmt.Sprintf("+refs/pull/%d/head:%s%d", p.Number, ns, p.Number))
	}
	r, err := c.git(fetchTimeout, "", args...)
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("git fetch %s (PR heads): exit %d: %s", c.Remote, r.ExitCode, firstLine(r.Stderr))
	}
	for _, p := range prs {
		got, err := c.Resolve(ns + strconv.Itoa(p.Number))
		if err != nil {
			return err
		}
		if got != p.OID {
			return fmt.Errorf("PR %d head is %s on remote %s but gh listed %s: it was pushed during the check, or --repo and --remote name different repositories; run lane-check again", p.Number, got, c.Remote, p.OID)
		}
	}
	return nil
}

// cleanup deletes every ref under ns.
func (c *Checker) cleanup(ns string) error {
	r, err := c.git(gitTimeout, "", "for-each-ref", "--format=%(refname)", ns)
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("git for-each-ref %s: exit %d: %s", ns, r.ExitCode, firstLine(r.Stderr))
	}
	var in strings.Builder
	for _, ref := range strings.Fields(r.Stdout) {
		if strings.HasPrefix(ref, ns) {
			in.WriteString("delete " + ref + "\n")
		}
	}
	if in.Len() == 0 {
		return nil
	}
	d, err := c.git(gitTimeout, in.String(), "update-ref", "--stdin")
	if err != nil {
		return err
	}
	if d.ExitCode != 0 {
		return fmt.Errorf("git update-ref --stdin: exit %d: %s", d.ExitCode, firstLine(d.Stderr))
	}
	return nil
}

// withOpenHeads lists the open PRs, fetches their heads into a private namespace, runs fn and deletes the refs
// again, also when fn fails or the process is interrupted. A cleanup failure is an error naming the leftover refs.
func (c *Checker) withOpenHeads(fn func(base string, heads []fetched) (int, error)) (code int, err error) {
	base, err := c.Resolve(c.Base)
	if err != nil {
		return ExitError, fmt.Errorf("--base: %w", err)
	}
	prs, err := c.ListOpen()
	if err != nil {
		return ExitError, err
	}
	ns := fmt.Sprintf("%s%d/", RefPrefix, os.Getpid())
	stop := onInterrupt(func() {
		if cerr := c.cleanup(ns); cerr != nil {
			fmt.Fprintln(c.Stderr, "lane-check:", cerr)
		}
	})
	defer func() {
		stop()
		if cerr := c.cleanup(ns); cerr != nil {
			code = ExitError
			err = errors.Join(err, fmt.Errorf("private refs under %s were not deleted (%w); delete them with `git update-ref -d <ref>`", ns, cerr))
		}
	}()
	if err := c.fetchHeads(ns, prs); err != nil {
		return ExitError, err
	}
	heads := make([]fetched, 0, len(prs))
	for _, p := range prs {
		t, err := c.Touched(base, p.OID)
		if err != nil {
			return ExitError, fmt.Errorf("PR %d: %w", p.Number, err)
		}
		heads = append(heads, fetched{PR: p, touched: t})
	}
	return fn(base, heads)
}

// relevant says whether two sides are a pair to check: both touch file, or (file "") they share any file.
func relevant(a, b []string, file string) bool {
	if file != "" {
		return contains(a, file) && contains(b, file)
	}
	return len(intersect(a, b)) > 0
}

func (c *Checker) verdict(stdout io.Writer, left, right, a, b string) (bool, error) {
	conf, err := c.Conflicts(a, b)
	if err != nil {
		return false, fmt.Errorf("%s x %s: %w", left, right, err)
	}
	if len(conf) == 0 {
		fmt.Fprintf(stdout, "%s x %s CLEAN\n", left, right)
		return false, nil
	}
	fmt.Fprintf(stdout, "%s x %s CONFLICT %s\n", left, right, joinPaths(conf))
	return true, nil
}

func (c *Checker) summary(prs, pairs, conflicts int) int {
	fmt.Fprintf(c.Stderr, "lane-check: %d open PRs, %d pairs checked, %d conflict\n", prs, pairs, conflicts)
	if conflicts > 0 {
		return ExitConflict
	}
	return ExitClean
}

// staleNotes tells, on stderr, which sides of a conflicting pair already conflict with Base on their own: such a
// head conflicts with every head that contains the newer Base, so its pair verdicts say "refresh me", not "these
// two tasks collide". Information only; the exit code is not changed.
func (c *Checker) staleNotes(base string, names []string, oids map[string]string) error {
	for _, n := range names {
		conf, err := c.Conflicts(base, oids[n])
		if err != nil {
			return fmt.Errorf("%s x %s: %w", n, c.Base, err)
		}
		if len(conf) > 0 {
			fmt.Fprintf(c.Stderr, "lane-check: note: %s already conflicts with %s on its own (%s); refresh it, its pair conflicts may only repeat that\n", n, c.Base, joinPaths(conf))
		}
	}
	return nil
}

// Open checks every pair of open PR heads that both touch file (file "" = that share any changed file).
func (c *Checker) Open(stdout io.Writer, file string) (int, error) {
	return c.withOpenHeads(func(base string, heads []fetched) (int, error) {
		pairs, conflicts := 0, 0
		var hit []string
		oids := map[string]string{}
		for i := range heads {
			for j := i + 1; j < len(heads); j++ {
				a, b := heads[i], heads[j]
				if !relevant(a.touched, b.touched, file) {
					continue
				}
				bad, err := c.verdict(stdout, strconv.Itoa(a.Number), strconv.Itoa(b.Number), a.OID, b.OID)
				if err != nil {
					return ExitError, err
				}
				pairs++
				if bad {
					conflicts++
					for _, h := range []fetched{a, b} {
						n := strconv.Itoa(h.Number)
						if _, ok := oids[n]; !ok {
							hit = append(hit, n)
							oids[n] = h.OID
						}
					}
				}
			}
		}
		if err := c.staleNotes(base, hit, oids); err != nil {
			return ExitError, err
		}
		return c.summary(len(heads), pairs, conflicts), nil
	})
}

// Plan checks one ref against every open PR head it shares a changed file with (or that also touches file).
// A PR whose head is exactly ref (the task's own PR) is skipped.
func (c *Checker) Plan(stdout io.Writer, ref, file string) (int, error) {
	oid, err := c.Resolve(ref)
	if err != nil {
		return ExitError, err
	}
	return c.withOpenHeads(func(base string, heads []fetched) (int, error) {
		mine, err := c.Touched(base, oid)
		if err != nil {
			return ExitError, err
		}
		pairs, conflicts := 0, 0
		hit := []string{quote(ref)}
		oids := map[string]string{quote(ref): oid}
		for _, h := range heads {
			if h.OID == oid || !relevant(mine, h.touched, file) {
				continue
			}
			bad, err := c.verdict(stdout, quote(ref), strconv.Itoa(h.Number), oid, h.OID)
			if err != nil {
				return ExitError, err
			}
			pairs++
			if bad {
				conflicts++
				hit = append(hit, strconv.Itoa(h.Number))
				oids[strconv.Itoa(h.Number)] = h.OID
			}
		}
		if conflicts > 0 {
			if err := c.staleNotes(base, hit, oids); err != nil {
				return ExitError, err
			}
		}
		return c.summary(len(heads), pairs, conflicts), nil
	})
}
