package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
)

// prcheck (G-048 T-1462): cmd_prcheck in bin/factory_cli.py (11618) and its six checks. fresh, identity, body and tier
// are answered here; public-text and the affected-tests run take the Python guard, the host test slots and
// ci/affected.py, so a repo under public_text_repos or a project that runs the tests locally hands over before any
// check runs. A project listed in gate_fast_projects (the factory's own) never runs them locally: CI green on the PR
// head stands in, and that path is answered here.

func init() {
	register("prcheck", "", handler{parse: parsePrcheck, run: runPrcheck})
}

type prcheckCmd struct{ pr string }

func parsePrcheck(rest []string) (any, bool) {
	_, vals, ok := msgArgs(rest, []string{"--pr"}, 0, 0)
	if !ok {
		return nil, false
	}
	return prcheckCmd{pr: vals["--pr"]}, true
}

const (
	noreplySuffix = "@users.noreply.github.com"
	webFlowEmail  = "noreply@github.com"
	fetchTimeout  = 180 * time.Second
)

// ghTimeout is the 120 s every gh call of cmd_pr, prcheck_body and gh_json has in Python; a var so a test can shorten it.
var ghTimeout = 120 * time.Second

// prcheckGit runs git in wt with the factory's hardening. Python's git_run has no timeout (fetch: 180 s) and a timeout
// or a start failure is an uncaught exception there, so those hand over.
type prcheckGit struct {
	wt   string
	opts gitx.Options
}

type gitOut struct {
	rc          int
	out, errOut string
}

func (g prcheckGit) run(timeout time.Duration, args ...string) (gitOut, error) {
	o := g.opts
	o.Timeout = timeout
	r, err := gitx.Run(g.wt, o, args...)
	if err != nil {
		return gitOut{}, errDelegate
	}
	if !utf8.ValidString(r.Stdout) || !utf8.ValidString(r.Stderr) {
		return gitOut{}, errDelegate // Python's text mode raises
	}
	return gitOut{r.ExitCode, pyNewlines(r.Stdout), pyNewlines(r.Stderr)}, nil
}

// out is _git_out: (rc, stdout with trailing newlines stripped).
func (g prcheckGit) outTrim(args ...string) (int, string, error) {
	r, err := g.run(0, args...)
	return r.rc, strings.TrimRight(r.out, "\n"), err
}

func prcheckFresh(g prcheckGit, base string) (bool, string, error) {
	ref := "origin/" + base
	f, err := g.run(fetchTimeout, "fetch", "origin", base)
	if err != nil {
		return false, "", err
	}
	if f.rc != 0 {
		return false, fmt.Sprintf("cannot fetch origin %s: %s; retry in a minute", base, pyTrunc(pyStrip(f.errOut), 200)), nil
	}
	anc, err := g.run(0, "merge-base", "--is-ancestor", ref, "HEAD")
	if err != nil {
		return false, "", err
	}
	if anc.rc == 0 {
		return true, "HEAD contains " + ref, nil
	}
	_, behind, err := g.outTrim("rev-list", "--count", "HEAD.."+ref)
	if err != nil {
		return false, "", err
	}
	m, err := g.run(0, "merge-tree", "--write-tree", "--name-only", ref, "HEAD")
	if err != nil {
		return false, "", err
	}
	switch m.rc {
	case 0:
		return true, fmt.Sprintf("behind %s by %s; a merge is clean (git fetch origin && git merge %s keeps it fresh)", ref, behind, ref), nil
	case 1:
		head, _, _ := strings.Cut(m.out, "\n\n")
		names := pySplitlines(head)
		if len(names) > 0 {
			names = names[1:] // tree id, then the conflicted paths (--name-only)
		}
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		files := make([]string, 0, len(set))
		for n := range set {
			files = append(files, n)
		}
		sort.Strings(files) // Python sorts by code point; UTF-8 byte order is the same
		if len(files) > 10 {
			files = files[:10]
		}
		list := strings.Join(files, ", ")
		if list == "" {
			list = "see git merge"
		}
		return false, fmt.Sprintf("merging %s conflicts (%s): run git merge %s, resolve, rerun the tests, push", ref, list, ref), nil
	}
	return false, fmt.Sprintf("cannot test a merge of %s: %s", ref, pyTrunc(pyStrip(m.errOut), 200)), nil
}

func emailProblems(author, committer string) []string {
	var bad []string
	if !strings.HasSuffix(strings.ToLower(pyStrip(author)), noreplySuffix) {
		bad = append(bad, "author")
	}
	c := strings.ToLower(pyStrip(committer))
	if c != webFlowEmail && !strings.HasSuffix(c, noreplySuffix) {
		bad = append(bad, "committer")
	}
	return bad
}

func prcheckIdentity(g prcheckGit, base string) (bool, string, error) {
	p, err := g.run(0, "log", "--format=%H%x09%ae%x09%ce", "origin/"+base+"..HEAD")
	if err != nil {
		return false, "", err
	}
	if p.rc != 0 {
		return false, "cannot read the commits: " + pyTrunc(pyStrip(p.errOut), 200), nil
	}
	n := 0
	var bad []string
	for _, ln := range pySplitlines(p.out) {
		if ln == "" {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) != 3 {
			return false, "", errDelegate // Python's unpacking raises
		}
		n++
		if pr := emailProblems(f[1], f[2]); len(pr) > 0 {
			bad = append(bad, pyTrunc(f[0], 9)+" "+strings.Join(pr, "+"))
		}
	}
	if n == 0 {
		return false, fmt.Sprintf("no commits beyond origin/%s: commit your work", base), nil
	}
	if len(bad) > 0 {
		if len(bad) > 5 {
			bad = bad[:5]
		}
		return false, fmt.Sprintf("non-noreply identity in %s: set the repo-local user.email to your %s address and rewrite "+
			"those commits (git rebase -r --exec 'git commit --amend --reset-author --no-edit')", strings.Join(bad, ", "), noreplySuffix), nil
	}
	return true, fmt.Sprintf("%d commit(s), author and committer noreply", n), nil
}

// prcheckTestCount is the number of `def test_` lines under tests/ at ref; -1 when git cannot say.
func prcheckTestCount(g prcheckGit, ref string) (int, error) {
	p, err := g.run(0, "grep", "-h", "-E", "^[[:space:]]+(async[[:space:]]+)?def[[:space:]]+test_", ref, "--", "tests")
	if err != nil {
		return 0, err
	}
	if p.rc != 0 && p.rc != 1 {
		return -1, nil
	}
	return len(pySplitlines(p.out)), nil
}

// prcheckBody is check 3: the PR body follows the template, shows a test command, and says `Tests removed:` when the
// count drops.
func prcheckBody(c *ctx, g prcheckGit, base, prURL string) (bool, string, error) {
	if prURL == "" {
		return false, "no PR for this task yet: open it (factory pr create --title T --body -) or pass --pr <url>, then rerun", nil
	}
	env := ghEnv(g.opts)
	r, err := ghx.Run(env, "", ghTimeout, "pr", "view", prURL, "--json", "body")
	if err != nil {
		return false, "", errDelegate // Python words an OSError or a timeout itself
	}
	var body *string
	errText := pyStrip(r.Stderr)
	if r.ExitCode == 0 {
		var v map[string]any
		if err := json.Unmarshal([]byte(r.Stdout), &v); err != nil {
			return false, "", errDelegate
		}
		raw, ok := v["body"]
		if !ok {
			return false, "", errDelegate // KeyError: Python prints its message
		}
		switch b := raw.(type) {
		case nil:
		case string:
			body = &b
		default:
			return false, "", errDelegate
		}
	}
	if body == nil {
		if errText == "" {
			errText = "no body returned"
		}
		return false, fmt.Sprintf("cannot read the body of %s (%s); retry in a minute", prURL, errText), nil
	}
	if prBodyNeedsPython(*body) {
		return false, "", errDelegate
	}
	headings, needPy := prTemplateHeadings(c.env.Home())
	if needPy {
		return false, "", errDelegate
	}
	problems := prBodyProblems(*body, headings)
	outside := prOutsideDetails(*body)
	for _, f := range findFences(outside) {
		if !f.closed {
			problems = append(problems, "a code fence outside <details> is never closed, so everything after it is code, not sections: "+
				"add the closing ``` (or ~~~) line")
			break
		}
	}
	if ev := evidenceProblem(evidenceSection(outside)); ev != "" {
		problems = append(problems, ev)
	}
	// count against the merge-base, so tests main gained since the branch point do not look like removals
	mbRC, mb, err := g.outTrim("merge-base", "HEAD", "origin/"+base)
	if err != nil {
		return false, "", err
	}
	nHead, err := prcheckTestCount(g, "HEAD")
	if err != nil {
		return false, "", err
	}
	nBase := -1
	if mbRC == 0 && mb != "" {
		if nBase, err = prcheckTestCount(g, mb); err != nil {
			return false, "", err
		}
	}
	switch {
	case nHead < 0 || nBase < 0:
		problems = append(problems, "cannot count the tests (git grep failed)")
	case nHead < nBase && !testsRemoved(outside):
		problems = append(problems, fmt.Sprintf("the test count drops (%d at the merge base with origin/%s, %d on HEAD): add a line "+
			"`Tests removed: <which and why>` outside <details>", nBase, base, nHead))
	}
	if len(problems) > 0 {
		for i, p := range problems {
			problems[i] = "      - " + p
		}
		return false, fmt.Sprintf("PR body of %s:\n%s", prURL, strings.Join(problems, "\n")), nil
	}
	return true, "PR body follows " + prTemplateFile, nil
}

var prURLRepoRE = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/pull/`)

// requiredChecksFor is required_checks_for: the project's required_checks of the task owning the PR, else of the
// project whose gh_repo is the PR's repo, else the global config.
func requiredChecksFor(c *ctx, prURL string) (string, error) {
	var val sql.NullString
	found := false
	if prURL != "" {
		err := c.db.QueryRow("SELECT p.required_checks FROM tasks t JOIN goals g ON g.id=t.goal_id "+
			"JOIN projects p ON p.id=g.project_id WHERE t.pr_url=?", prURL).Scan(&val)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, sql.ErrNoRows):
			return "", fmt.Errorf("read required checks of %s: %w", prURL, err)
		}
		if m := prURLRepoRE.FindStringSubmatch(prURL); !found && m != nil {
			err := c.db.QueryRow("SELECT required_checks FROM projects WHERE gh_repo=?", m[1]).Scan(&val)
			switch {
			case err == nil:
				found = true
			case !errors.Is(err, sql.ErrNoRows):
				return "", fmt.Errorf("read required checks of %s: %w", m[1], err)
			}
		}
	}
	if found && pyStrip(val.String) != "" {
		return pyStrip(val.String), nil
	}
	return cfgText(c, "required_checks")
}

// checkEntry is one statusCheckRollup element, the fields latest_checks and checks_ok read.
type checkEntry struct {
	raw   map[string]any
	ts    string
	idx   int
	ident string
}

func strField(m map[string]any, k string) (string, bool, bool) { // value, truthy, ok (string or absent/null)
	v, present := m[k]
	if !present || v == nil {
		return "", false, true
	}
	s, isStr := v.(string)
	if !isStr {
		return "", false, false
	}
	return s, s != "", true
}

// latestChecks is latest_checks: the newest rollup entry per check identity, in list order of the winners.
func latestChecks(rollup []any) ([]map[string]any, bool) {
	latest := map[string]checkEntry{}
	for i, e := range rollup {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		tn, _, ok1 := strField(m, "__typename")
		ctxv, ctxT, ok2 := strField(m, "context")
		name, nameT, ok3 := strField(m, "name")
		wf, _, ok4 := strField(m, "workflowName")
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, false
		}
		var ident string
		if tn == "StatusContext" || (ctxT && !nameT) {
			ident = "ctx\x00" + ctxv
			if _, present := m["context"]; !present || m["context"] == nil {
				ident = "ctx\x00<none>"
			}
		} else {
			ident = "run\x00" + wf + "\x00"
			if _, present := m["name"]; !present || m["name"] == nil {
				ident += "<none>"
			} else {
				ident += "s:" + name
			}
		}
		ts := ""
		for _, k := range []string{"startedAt", "completedAt", "createdAt"} {
			s, truthy, ok := strField(m, k)
			if !ok {
				return nil, false
			}
			if truthy {
				ts = s
				break
			}
		}
		if old, ok := latest[ident]; !ok || ts >= old.ts {
			latest[ident] = checkEntry{raw: m, ts: ts, idx: i, ident: ident}
		}
	}
	list := make([]checkEntry, 0, len(latest))
	for _, e := range latest {
		list = append(list, e)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].idx < list[b].idx })
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = e.raw
	}
	return out, true
}

// checksNotOK is checks_ok: "name=STATE" for every latest check that is not SUCCESS, SKIPPED or NEUTRAL.
func checksNotOK(rollup []any) ([]string, bool) {
	latest, ok := latestChecks(rollup)
	if !ok {
		return nil, false
	}
	var bad []string
	for _, c := range latest {
		first := func(keys ...string) (string, bool) {
			for _, k := range keys {
				if s, truthy, ok := strField(c, k); !ok {
					return "", false
				} else if truthy {
					return s, true
				}
			}
			return "", true
		}
		concl, ok := first("conclusion", "state")
		if !ok || !isASCII(concl) {
			return nil, false
		}
		concl = strings.ToUpper(concl)
		if concl == "SUCCESS" || concl == "SKIPPED" || concl == "NEUTRAL" {
			continue
		}
		name, ok := first("name", "context")
		if !ok || !isASCII(name) {
			return nil, false
		}
		if name == "" {
			name = "None"
		}
		show := concl
		if show == "" {
			s, truthy, ok := strField(c, "status")
			if !ok || !isASCII(s) {
				return nil, false
			}
			show = "None"
			if truthy {
				show = s
			}
		}
		bad = append(bad, name+"="+show)
	}
	return bad, true
}

const prStateFields = "number,state,headRefOid,headRefName,baseRefName,mergeable,isDraft,statusCheckRollup,url"

// ciGreenOnHead is ci_green_on_head: the PR is open, its head is head, and every check is green with the required
// ones present. A gh failure is a note, not an error; a failure Python words from its own exception hands over.
func ciGreenOnHead(c *ctx, head, prURL string) (bool, string, error) {
	r, err := ghx.Run(nil, "", ghTimeout, "pr", "view", prURL, "--json", prStateFields)
	var te *gitx.TimeoutError
	switch {
	case errors.As(err, &te):
		return false, "cannot read PR checks: " + te.Error(), nil
	case err != nil:
		return false, "", errDelegate
	}
	if r.ExitCode != 0 {
		lines := pySplitlines(pyStrip(r.Stderr))
		first := ""
		if len(lines) > 0 {
			first = lines[0]
		}
		parts := []string{"pr", "view", prURL}
		msg := "gh " + strings.Join(parts, " ") + ": " + first
		fmt.Fprintf(c.errb, "factory: %s\n", msg) // gh_json prints the failure before it raises
		return false, "cannot read PR checks: " + msg, nil
	}
	var pr map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &pr); err != nil {
		return false, "", errDelegate
	}
	state, _, ok := strField(pr, "state")
	oid, _, ok2 := strField(pr, "headRefOid")
	if !ok || !ok2 {
		return false, "", errDelegate
	}
	if state != "OPEN" {
		if _, has := pr["state"]; !has || pr["state"] == nil {
			return false, "", errDelegate
		}
		return false, fmt.Sprintf("PR is %s, not OPEN", state), nil
	}
	if oid != head {
		shown := pyTrunc(oid, 9)
		if oid == "" {
			shown = "?"
		}
		return false, fmt.Sprintf("PR head %s != local HEAD %s: push, wait for CI", shown, pyTrunc(head, 9)), nil
	}
	rollup, isList := pr["statusCheckRollup"].([]any)
	if pr["statusCheckRollup"] != nil && !isList {
		return false, "", errDelegate
	}
	if len(rollup) == 0 {
		return false, "PR has no CI checks yet", nil
	}
	bad, ok := checksNotOK(rollup)
	if !ok {
		return false, "", errDelegate
	}
	seen := map[string]bool{}
	for _, e := range rollup {
		m := e.(map[string]any) // latestChecks accepted every element above
		if s, truthy, _ := strField(m, "name"); truthy {
			seen[s] = true
		} else if s, truthy, _ := strField(m, "context"); truthy {
			seen[s] = true
		}
	}
	req, err := requiredChecksFor(c, prURL)
	if err != nil {
		return false, "", err
	}
	for _, n := range strings.Split(req, ",") {
		if n = pyStrip(n); n != "" && !seen[n] {
			bad = append(bad, fmt.Sprintf("required check %s missing", n))
		}
	}
	if len(bad) > 0 {
		if len(bad) > 5 {
			bad = bad[:5]
		}
		return false, "CI not all green: " + strings.Join(bad, ", "), nil
	}
	return true, "all checks SUCCESS", nil
}

// prcheckAffected is check 5 for a project that never runs the tests locally (gate_fast_projects).
func prcheckAffected(c *ctx, g prcheckGit, prURL string) (bool, string, error) {
	rc, head, err := g.outTrim("rev-parse", "HEAD")
	if err != nil {
		return false, "", err
	}
	ok, why := false, "no PR url or HEAD"
	if rc == 0 && prURL != "" {
		if ok, why, err = ciGreenOnHead(c, head, prURL); err != nil {
			return false, "", err
		}
	}
	pre := "not run locally (gate_fast_projects, M16816)"
	if ok {
		return true, fmt.Sprintf("%s; CI is green on the PR head (%s)", pre, why), nil
	}
	return false, fmt.Sprintf("%s; CI is not green on the head yet (%s): push, wait for CI, rerun prcheck", pre, why), nil
}

func jsonStrList(l []string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, s := range l {
		if i > 0 {
			b.WriteString(", ")
		}
		dumpString(&b, s)
	}
	b.WriteString("]")
	return b.String()
}

func runPrcheck(c *ctx, parsed any) (int, error) {
	a := parsed.(prcheckCmd)
	if _, err := requireRole(c, "coder"); err != nil {
		return 0, err
	}
	var role string
	var taskID sql.NullString
	err := c.db.QueryRow("SELECT role, task_id FROM agents WHERE name=?", c.who).Scan(&role, &taskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read agent %s: %w", c.who, err)
	}
	if err != nil || role != "coder" || taskID.String == "" {
		return 0, dieErr(3, "prcheck is for a coder with a task; run it from your task worktree")
	}
	var tid string
	var goalID, wtPath, taskPR, projectID, baseBranch sql.NullString
	err = c.db.QueryRow("SELECT t.id, t.goal_id, t.worktree, t.pr_url, g.project_id, p.base_branch FROM tasks t "+
		"JOIN goals g ON g.id=t.goal_id LEFT JOIN projects p ON p.id=g.project_id WHERE t.id=?", taskID.String).
		Scan(&tid, &goalID, &wtPath, &taskPR, &projectID, &baseBranch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read task %s: %w", taskID.String, err)
	}
	wt := wtPath.String
	if fi, serr := os.Stat(wt); err != nil || wt == "" || serr != nil || !fi.IsDir() {
		return 0, dieErr(3, fmt.Sprintf("%s has no worktree directory; ask your lead", taskID.String))
	}
	base := baseBranch.String
	if base == "" {
		base = "main"
	}
	prURL := a.pr
	if prURL == "" {
		prURL = taskPR.String
	}
	opts, err := gitOptions(c)
	if err != nil {
		return 0, err
	}
	g := prcheckGit{wt: wt, opts: opts}

	// Checks Go does not answer hand over before anything runs (fetch, gh and the test slot are not repeated).
	pub, ok, err := cfgSlugs(c, "public_text_repos")
	if err != nil {
		return 0, err
	}
	if !ok || len(pub) > 0 {
		return 0, errDelegate
	}
	scriptPath := wt + "/ci/affected.py"
	hasScript := false
	if fi, serr := os.Stat(scriptPath); serr == nil && fi.Mode().IsRegular() {
		hasScript = true
	}
	fast := false
	if hasScript {
		fastCfg, err := cfgText(c, "gate_fast_projects")
		if err != nil {
			return 0, err
		}
		for _, p := range strings.Fields(fastCfg) {
			if projectID.String != "" && p == projectID.String {
				fast = true
			}
		}
		if !fast {
			return 0, errDelegate // the test slot and ci/affected.py --run
		}
	}

	_, st, err := g.outTrim("status", "--porcelain", "--untracked-files=no") // Python reads only the output
	if err != nil {
		return 0, err
	}
	if st != "" {
		return 0, dieErr(3, "uncommitted tracked changes: commit them (a WIP commit is fine), then rerun prcheck")
	}
	rc, head, err := g.outTrim("rev-parse", "HEAD")
	if err != nil {
		return 0, err
	}
	if rc != 0 {
		return 0, dieErr(3, fmt.Sprintf("cannot read HEAD of %s", wt))
	}

	type result struct {
		name string
		ok   bool
		note string
	}
	var results []result
	emit := func(name string, ok bool, note string, err error) error {
		if err != nil {
			return err
		}
		results = append(results, result{name, ok, note})
		word := "PASS"
		if !ok {
			word = "FAIL"
		}
		fmt.Fprintf(c.out, "%s %s: %s\n", word, name, note)
		return nil
	}
	steps := []struct {
		name string
		fn   func() (bool, string, error)
	}{
		{"fresh", func() (bool, string, error) { return prcheckFresh(g, base) }},
		{"identity", func() (bool, string, error) { return prcheckIdentity(g, base) }},
		{"body", func() (bool, string, error) { return prcheckBody(c, g, base, prURL) }},
		{"public-text", func() (bool, string, error) { return true, "n/a: the repo is not in public_text_repos", nil }},
		{"affected", func() (bool, string, error) {
			if !hasScript {
				return false, "ci/affected.py is missing in the worktree: git fetch origin && git merge origin/main", nil
			}
			return prcheckAffected(c, g, prURL)
		}},
		{"tier", func() (bool, string, error) { return true, "tier: unavailable (no risk classifier in this build)", nil }},
	}
	for _, s := range steps {
		ok, note, err := s.fn()
		if err := emit(s.name, ok, note, err); err != nil {
			return 0, err
		}
	}
	all := true
	failed := []string{}
	for _, r := range results {
		if !r.ok {
			all = false
			failed = append(failed, r.name)
		}
	}
	var d strings.Builder
	d.WriteString(`{"head": `)
	dumpString(&d, head)
	d.WriteString(`, "ok": `)
	if all {
		d.WriteString("true")
	} else {
		d.WriteString("false")
	}
	d.WriteString(`, "failed": ` + jsonStrList(failed) + "}")
	if err := event(c, "task", tid, "prcheck", strp(d.String())); err != nil {
		return 0, fmt.Errorf("record prcheck: %w", err)
	}
	if all {
		fmt.Fprintf(c.out, "prcheck passed on %s\n", pyTrunc(head, 9))
		return 0, nil
	}
	fmt.Fprintf(c.out, "prcheck FAILED on %s: fix the FAIL lines, commit, rerun\n", pyTrunc(head, 9))
	return 1, nil
}
