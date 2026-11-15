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

	"factory/go/internal/gitx"
)

// relay-push (G-058 T-1618, owner decision M37756): the worker gh token has no `workflow` scope, so a coder cannot push
// a commit that touches .github/workflows/**. The director verifies the exact head (`relay-push verify`), then
// `relay-push <task> <sha>` checks the head again mechanically and pushes it to refs/heads/factory/<task> with the HQ
// gh credential. Go only: bin/factory routes the word here (exec_schedule_go), Python has no copy; a refusal never
// delegates. Every check fails closed: a git or gh call that cannot answer refuses the push.

func init() {
	register("relay-push", "", handler{parse: parseRelayPush, run: runRelayPush})
	register("relay-push", "verify", handler{parse: parseRelayVerify, run: runRelayVerify})
}

const relayUsage = "usage: factory relay-push <task> <sha>  |  factory relay-push verify <task> <sha> --reason TEXT"

var (
	relayTaskRE = regexp.MustCompile(`^T-[0-9]+$`)
	relaySHARE  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type relayCmd struct {
	task, sha, reason string
	bad               string // non-empty: the usage error to print (never delegate: Python has no relay-push)
}

func relayArgs(task, sha string) relayCmd {
	t := strings.ToUpper(strings.TrimSpace(task))
	if !relayTaskRE.MatchString(t) {
		return relayCmd{bad: fmt.Sprintf("%s is not a task id (T-1234)\n%s", verdictRepr(task), relayUsage)}
	}
	if !relaySHARE.MatchString(sha) {
		return relayCmd{bad: fmt.Sprintf("%s is not a full lower-case commit sha (40 hex digits; `git rev-parse <ref>`)\n%s", verdictRepr(sha), relayUsage)}
	}
	return relayCmd{task: t, sha: sha}
}

func parseRelayPush(rest []string) (any, bool) {
	if len(rest) != 2 {
		return relayCmd{bad: relayUsage}, true
	}
	return relayArgs(rest[0], rest[1]), true
}

func parseRelayVerify(rest []string) (any, bool) {
	var pos []string
	reason, seen := "", false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--reason" && i+1 < len(rest) && !seen:
			reason, seen = rest[i+1], true
			i++
		case strings.HasPrefix(a, "--reason=") && !seen:
			reason, seen = strings.TrimPrefix(a, "--reason="), true
		case strings.HasPrefix(a, "-"):
			return relayCmd{bad: relayUsage}, true
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return relayCmd{bad: relayUsage}, true
	}
	r := relayArgs(pos[0], pos[1])
	if r.bad == "" && strings.TrimSpace(reason) == "" {
		r.bad = "relay-push verify needs --reason \"what you checked in this head\"\n" + relayUsage
	}
	r.reason = strings.Join(strings.Fields(reason), " ")
	return r, true
}

// relayRole refuses every worker (process ancestry, FACTORY_ROLE or agents row) and every role but director/owner.
func relayRole(c *ctx, what string) error {
	w, unknown := callerIsWorker(c)
	if unknown {
		return dieErr(3, what+": cannot tell whether the caller is a worker; run it from the director pane")
	}
	if w != "" {
		return dieErr(3, fmt.Sprintf("%s is director-only; a %s asks its lead, who asks the director", what, w))
	}
	_, err := requireRole(c, "director")
	return err
}

func runRelayVerify(c *ctx, parsed any) (int, error) {
	a := parsed.(relayCmd)
	if a.bad != "" {
		return 0, dieErr(2, a.bad)
	}
	if err := relayRole(c, "relay-push verify"); err != nil {
		return 0, err
	}
	var id string
	if err := c.db.QueryRow("SELECT id FROM tasks WHERE id=?", a.task).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, dieErr(2, fmt.Sprintf("no task %s", a.task))
		}
		return 0, fmt.Errorf("read task %s: %w", a.task, err)
	}
	if err := event(c, "task", a.task, "relay-verify", strp(a.sha+" "+a.reason)); err != nil {
		return 0, fmt.Errorf("record relay-verify: %w", err)
	}
	fmt.Fprintf(c.out, "verified %s head %s; push it with `factory relay-push %s %s`\n", a.task, a.sha, a.task, a.sha)
	return 0, nil
}

// relayVerified: an events row relay-verify for this task whose first word is this exact sha, written by a director
// (agents.role) or the owner.
func relayVerified(c *ctx, task, sha string) (bool, error) {
	rows, err := c.query("SELECT e.actor, e.detail, COALESCE(a.role,'') FROM events e LEFT JOIN agents a ON a.name=e.actor "+
		"WHERE e.entity='task' AND e.entity_id=? AND e.action='relay-verify'", task)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var actor, detail sql.NullString
		var role string
		if err := rows.Scan(&actor, &detail, &role); err != nil {
			return false, err
		}
		first, _, _ := strings.Cut(detail.String, " ")
		if first == sha && (role == "director" || actor.String == "owner") {
			return true, nil
		}
	}
	return false, rows.Err()
}

// relayScrubEnv is the process env without anything that could redirect, trace or authenticate git: every GIT_*
// variable (GIT_TRACE_CURL prints the Authorization header, GIT_DIR moves the repo, GIT_CONFIG_* adds config) and the
// token and askpass variables. A non-nil add is appended.
func relayScrubEnv(environ []string, add ...string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "GIT_"), k == "GH_TOKEN", k == "GITHUB_TOKEN", k == "GH_ENTERPRISE_TOKEN",
			k == "GITHUB_ENTERPRISE_TOKEN", k == "SSH_ASKPASS", k == "FACTORY_RELAY_TOKEN":
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	return append(out, add...)
}

// relayGhEnv is the env for the HQ gh: the process env (gh's own auth, GH_TOKEN included) minus GH_HOST/GH_REPO.
func relayGhEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k != "GH_HOST" && k != "GH_REPO" && k != "GH_ENTERPRISE_TOKEN" && k != "GITHUB_ENTERPRISE_TOKEN" {
			out = append(out, kv)
		}
	}
	return out
}

var (
	relayGhTokenRE = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{20,})`)
	relayURLUserRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
)

// relayRedact removes the token (exact value) and anything shaped like a GitHub token or URL credentials.
func relayRedact(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[REDACTED]")
	}
	s = relayGhTokenRE.ReplaceAllString(s, "[REDACTED]")
	return relayURLUserRE.ReplaceAllString(s, "${1}[REDACTED]@")
}

// relayRiskyKey: repo-local config git would obey on a push and a worker controls (its worktree's .git/config or
// config.worktree): URL rewrites (url.*.insteadOf, pushInsteadOf) could send the token elsewhere, http.* proxies or
// sslVerify, credential helpers, includes that pull more config in, askpass/ssh commands, submodule recursion.
func relayRiskyKey(key string) bool {
	k := strings.ToLower(key)
	for _, p := range []string{"url.", "http.", "credential.", "include.", "includeif.", "push.", "submodule.", "transfer.",
		"protocol.", "core.askpass", "core.sshcommand", "core.gitproxy"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

type relayGit struct {
	c    *ctx
	opts gitx.Options
	wt   string
	env  []string
}

func (g relayGit) run(timeout time.Duration, args ...string) (string, string, int, error) {
	argv := g.opts.Argv(g.wt, args...)
	return g.c.deps.runCmd(argv[0], argv[1:], g.wt, g.env, timeout)
}

// ok runs git and returns stdout; any failure to run or non-zero exit is an error naming the command.
func (g relayGit) ok(args ...string) (string, error) {
	out, errs, code, err := g.run(60*time.Second, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %v", args[0], err)
	}
	if code != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", args[0], code, firstLine(errs))
	}
	return out, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func relayRefuse(task, why string) error {
	return dieErr(3, fmt.Sprintf("relay-push %s refused: %s", task, why))
}

func runRelayPush(c *ctx, parsed any) (int, error) {
	a := parsed.(relayCmd)
	if a.bad != "" {
		return 0, dieErr(2, a.bad)
	}
	if err := relayRole(c, "relay-push"); err != nil {
		return 0, err
	}
	var branch, wt, ghRepo, base sql.NullString
	err := c.db.QueryRow("SELECT t.branch, t.worktree, p.gh_repo, p.base_branch FROM tasks t JOIN goals g ON g.id=t.goal_id "+
		"JOIN projects p ON p.id=g.project_id WHERE t.id=?", a.task).Scan(&branch, &wt, &ghRepo, &base)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, dieErr(2, fmt.Sprintf("no task %s (or it has no project)", a.task))
	}
	if err != nil {
		return 0, fmt.Errorf("read task %s: %w", a.task, err)
	}
	want := "factory/" + strings.ToLower(a.task)
	if branch.String != want {
		return 0, relayRefuse(a.task, fmt.Sprintf("the task's branch is %s, not %s; relay-push only pushes factory/<task>",
			verdictRepr(branch.String), want))
	}
	ok, err := relayVerified(c, a.task, a.sha)
	if err != nil {
		return 0, fmt.Errorf("read relay-verify events: %w", err)
	}
	if !ok {
		return 0, relayRefuse(a.task, fmt.Sprintf("no director verify record for %s; the director reviews this exact head, "+
			"then runs `factory relay-push verify %s %s --reason ...`", a.sha, a.task, a.sha))
	}
	repo := pyStrip(ghRepo.String)
	if !ghSlugRE.MatchString(repo) {
		return 0, relayRefuse(a.task, fmt.Sprintf("its project has no valid gh_repo (%s); fix it with `factory project update`", verdictRepr(repo)))
	}
	baseBr := base.String
	if !relayBaseRE.MatchString(baseBr) || strings.Contains(baseBr, "..") {
		return 0, relayRefuse(a.task, fmt.Sprintf("its project base branch %s is not a plain branch name", verdictRepr(baseBr)))
	}
	if fi, err := os.Stat(wt.String); wt.String == "" || err != nil || !fi.IsDir() {
		return 0, relayRefuse(a.task, fmt.Sprintf("the task worktree %s is not a directory; the commit must be in it", verdictRepr(wt.String)))
	}
	opts, err := gitOptions(c)
	if err != nil {
		return 0, relayRefuse(a.task, "cannot derive the git hardening options (live homes)")
	}
	g := relayGit{c: c, opts: opts, wt: wt.String, env: relayScrubEnv(c.deps.Environ)}

	// 1. the worktree's own config must not redirect or re-authenticate the push
	cfgOut, err := g.ok("config", "--list", "--show-scope", "-z")
	if err != nil {
		return 0, relayRefuse(a.task, "cannot read the worktree git config: "+err.Error())
	}
	ents := strings.Split(strings.TrimSuffix(cfgOut, "\x00"), "\x00") // -z --show-scope: scope NUL key LF value NUL
	if cfgOut == "" {
		ents = nil
	}
	if len(ents)%2 != 0 {
		return 0, relayRefuse(a.task, "cannot parse the worktree git config (git config --list --show-scope -z)")
	}
	for i := 0; i < len(ents); i += 2 {
		scope := ents[i]
		k, _, _ := strings.Cut(ents[i+1], "\n")
		if (scope == "local" || scope == "worktree") && relayRiskyKey(k) {
			return 0, relayRefuse(a.task, fmt.Sprintf("the worktree git config sets %s (%s scope); a relay push obeys no repo-local "+
				"url/http/credential/include/push config: remove it (`git config --unset %s`) and verify again", k, scope, k))
		}
	}

	// 2. the trusted main: from GitHub through the HQ gh, never the worktree's own refs/remotes/origin/*
	ghEnv := relayGhEnv(c.deps.Environ)
	out, errs, code, err := c.deps.runCmd("gh", []string{"api", "repos/" + repo + "/git/ref/heads/" + baseBr, "--jq", ".object.sha"},
		wt.String, ghEnv, 60*time.Second)
	mainSHA := strings.TrimSpace(out)
	if err != nil || code != 0 || !relaySHARE.MatchString(mainSHA) {
		return 0, relayRefuse(a.task, fmt.Sprintf("cannot read origin/%s of %s from GitHub (gh api: exit %d %s); check `gh auth status`",
			baseBr, repo, code, relayRedact(firstLine(errs), "")))
	}
	for _, s := range []string{a.sha, mainSHA} {
		if _, err := g.ok("cat-file", "-e", s+"^{commit}"); err != nil {
			return 0, relayRefuse(a.task, fmt.Sprintf("commit %s is not in %s; run `git fetch origin %s` there (or commit the head) and retry",
				s, wt.String, baseBr))
		}
	}
	_, _, code, err = g.run(60*time.Second, "merge-base", "--is-ancestor", mainSHA, a.sha)
	if err != nil || (code != 0 && code != 1) {
		return 0, relayRefuse(a.task, fmt.Sprintf("git merge-base failed (exit %d %v)", code, err))
	}
	if code == 1 {
		return 0, relayRefuse(a.task, fmt.Sprintf("%s does not contain origin/%s (%s): merge origin/%s into the branch, then verify the new head",
			a.sha, baseBr, mainSHA, baseBr))
	}

	// 3. every commit the push adds is authored and committed by the factory noreply
	noreply, err := relayNoreply(c, opts)
	if err != nil {
		return 0, relayRefuse(a.task, err.Error())
	}
	logOut, err := g.ok("log", "--no-show-signature", "--format=%H%x00%ae%x00%ce", mainSHA+".."+a.sha)
	if err != nil {
		return 0, relayRefuse(a.task, err.Error())
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) != 3 {
			return 0, relayRefuse(a.task, "cannot read the commit emails (git log)")
		}
		for i, who := range []string{"author", "committer"} {
			if !strings.EqualFold(f[i+1], noreply) {
				return 0, relayRefuse(a.task, fmt.Sprintf("commit %s %s email %s is not the factory noreply %s", f[0][:12], who,
					verdictRepr(f[i+1]), noreply))
			}
		}
		n++
	}
	if n == 0 {
		return 0, relayRefuse(a.task, fmt.Sprintf("%s adds no commit over origin/%s", a.sha, baseBr))
	}

	// 4. the workflow files change only in allowed ways
	files, problems, err := relayWorkflowCheck(g, mainSHA, a.sha)
	if err != nil {
		return 0, relayRefuse(a.task, err.Error())
	}
	if len(problems) > 0 {
		return 0, relayRefuse(a.task, "forbidden workflow change:\n  "+strings.Join(problems, "\n  "))
	}

	// 5. push with the HQ gh credential; the token lives only in this process and in git's env
	tout, terr, code, err := c.deps.runCmd("gh", []string{"auth", "token", "--hostname", "github.com"}, wt.String, ghEnv, 30*time.Second)
	token := strings.TrimSpace(tout)
	if err != nil || code != 0 || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return 0, relayRefuse(a.task, fmt.Sprintf("the HQ gh has no github.com token (gh auth token: exit %d %s); run `gh auth login` as the owner",
			code, relayRedact(firstLine(terr), token)))
	}
	push := relayGit{c: c, opts: opts, wt: wt.String, env: relayScrubEnv(c.deps.Environ, "FACTORY_RELAY_TOKEN="+token)}
	helper := `!f() { test "$1" = get || exit 0; echo username=x-access-token; printf 'password=%s\n' "$FACTORY_RELAY_TOKEN"; }; f`
	url := "https://github.com/" + repo + ".git"
	pout, perr, code, err := push.run(120*time.Second, "-c", "credential.helper=", "-c", "credential.helper="+helper,
		"-c", "push.recurseSubmodules=no", "push", "--no-verify", "--porcelain", url, a.sha+":refs/heads/"+want)
	pout, perr = relayRedact(pout, token), relayRedact(perr, token)
	if err != nil || code != 0 {
		c.out.WriteString(pout)
		c.errb.WriteString(perr)
		why := "exit " + fmt.Sprint(code)
		if err != nil {
			why = relayRedact(err.Error(), token)
		}
		return 0, dieErr(1, fmt.Sprintf("relay-push %s: git push failed (%s); nothing recorded. A non-fast-forward means the branch "+
			"moved: merge, verify the new head, retry", a.task, why))
	}
	detail, err := json.Marshal(map[string]any{"sha": a.sha, "branch": want, "repo": repo, "files": files, "pusher": c.who})
	if err != nil {
		return 0, err
	}
	if err := event(c, "task", a.task, "relay-push", strp(string(detail))); err != nil {
		return 0, fmt.Errorf("pushed, but recording the relay-push event failed: %w", err)
	}
	fmt.Fprintf(c.out, "relay-push %s: pushed %s to %s refs/heads/%s (%d files)\n", a.task, a.sha, repo, want, len(files))
	return 0, nil
}

// relayNoreply is noreply_email(): config noreply_email, else user.email of the HQ checkout; it must end in the GitHub
// noreply suffix or the check cannot decide and refuses.
func relayNoreply(c *ctx, opts gitx.Options) (string, error) {
	email, err := cfgText(c, "noreply_email")
	if err != nil {
		return "", fmt.Errorf("cannot read config noreply_email: %v", err)
	}
	email = strings.TrimSpace(email)
	if email == "" {
		argv := opts.Argv(c.env.Home(), "config", "--get", "user.email")
		out, _, code, err := c.deps.runCmd(argv[0], argv[1:], c.env.Home(), relayScrubEnv(c.deps.Environ), 30*time.Second)
		if err == nil && code == 0 {
			email = strings.TrimSpace(out)
		}
	}
	if !strings.HasSuffix(strings.ToLower(email), "@users.noreply.github.com") {
		return "", fmt.Errorf("the factory noreply email is unknown (%s): set `factory config noreply_email <id+handle>@users.noreply.github.com`",
			verdictRepr(email))
	}
	return email, nil
}

const relayWorkflowDir = ".github/workflows/"

// relayWorkflowCheck lists the files the push changes over main and the policy problems of the workflow files among
// them. Raw diff-tree output: no external diff, no textconv, renames as delete + add.
func relayWorkflowCheck(g relayGit, mainSHA, sha string) ([]string, []string, error) {
	out, err := g.ok("diff-tree", "-r", "--no-renames", "--no-ext-diff", "--no-textconv", "-z", mainSHA, sha)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(out, "\x00")
	var files, problems []string
	for i := 0; i+1 < len(parts); i += 2 {
		meta, path := parts[i], parts[i+1]
		f := strings.Fields(strings.TrimPrefix(meta, ":"))
		if len(f) != 5 {
			return nil, nil, fmt.Errorf("cannot parse git diff-tree output %s", verdictRepr(meta))
		}
		files = append(files, path)
		if !strings.HasPrefix(strings.ToLower(path), strings.ToLower(relayWorkflowDir)) {
			continue
		}
		for _, mode := range f[:2] {
			if mode != "000000" && mode != "100644" && mode != "100755" {
				problems = append(problems, fmt.Sprintf("%s: mode %s (symlink or submodule) under .github/workflows", path, mode))
			}
		}
		blob := func(id string) (string, error) {
			if strings.Trim(id, "0") == "" {
				return "", nil
			}
			return g.ok("cat-file", "blob", id)
		}
		oldText, err := blob(f[2])
		if err != nil {
			return nil, nil, err
		}
		newText, err := blob(f[3])
		if err != nil {
			return nil, nil, err
		}
		for _, p := range workflowProblems(oldText, newText) {
			problems = append(problems, path+": "+p)
		}
	}
	return files, problems, nil
}

// ---- workflow policy: line-based YAML reading (stdlib only), the same approach as tests/test_ci_policy.py ----

var wfKeyRE = regexp.MustCompile(`^(\s*)(-\s+)?["']?([A-Za-z_][\w-]*)["']?:(?:\s+(.*)|\s*)$`)

// wfEntry is one significant line: the key path it sits under (jobs.build.runs-on), and its text.
type wfEntry struct {
	path []string
	text string
}

func (e wfEntry) String() string { return strings.Join(e.path, ".") + ": " + e.text }

func (e wfEntry) under(key string) bool {
	for _, p := range e.path {
		if p == key {
			return true
		}
	}
	return false
}

// wfEntries reads a workflow into entries. A key line is an entry under its own path (text = its value); any other
// line (a list item, a block-scalar line) is an entry under the path of the key it belongs to.
func wfEntries(text string) []wfEntry {
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	var out []wfEntry
	scalarIndent := -1 // inside a block scalar (| or >): lines indented deeper than this are content
	keys := func() []string {
		p := make([]string, len(stack))
		for i, f := range stack {
			p[i] = f.key
		}
		return p
	}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "#") || s == "---" || s == "..." {
			continue
		}
		ind := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if scalarIndent >= 0 {
			if ind > scalarIndent {
				out = append(out, wfEntry{keys(), s})
				continue
			}
			scalarIndent = -1
		}
		m := wfKeyRE.FindStringSubmatch(raw)
		if m == nil {
			out = append(out, wfEntry{keys(), strings.TrimPrefix(s, "- ")})
			continue
		}
		kind := ind + len(m[2]) // a "- key:" item: the key sits after the dash
		for len(stack) > 0 && stack[len(stack)-1].indent >= kind {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{kind, m[3]})
		val := strings.TrimSpace(m[4])
		out = append(out, wfEntry{keys(), val})
		if strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			scalarIndent = kind
		}
	}
	return out
}

var (
	wfSecretsRE = regexp.MustCompile(`(?i)\bsecrets\b`) // expression contexts are case-insensitive
	wfHostedRE  = regexp.MustCompile(`^(?:ubuntu|windows|macos)-[A-Za-z0-9.-]+$`)
	// wfForkSafe are the triggers a fork pull request cannot start; any other trigger (pull_request, workflow_call,
	// workflow_run, issue_comment, ...) or an unreadable trigger set makes the workflow fork-reachable.
	wfForkSafe = map[string]bool{"push": true, "workflow_dispatch": true, "schedule": true, "release": true,
		"merge_group": true, "repository_dispatch": true}
	wfWordRE    = regexp.MustCompile(`[A-Za-z_][\w-]*`)
	wfCommentRE = regexp.MustCompile(`\s+#.*$`)
	relayBaseRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// added is the multiset difference: entries of b not matched by an entry of a.
func wfAdded(a, b []string) []string {
	have := map[string]int{}
	for _, x := range a {
		have[x]++
	}
	var out []string
	for _, x := range b {
		if have[x] > 0 {
			have[x]--
			continue
		}
		out = append(out, x)
	}
	return out
}

func wfSelect(es []wfEntry, keep func(wfEntry) bool) []string {
	var out []string
	for _, e := range es {
		if keep(e) {
			out = append(out, e.String())
		}
	}
	return out
}

func wfTopIs(e wfEntry, key string) bool { return len(e.path) > 0 && e.path[0] == key }

// wfForkReachable: the workflow has a trigger outside wfForkSafe, or none could be read.
func wfForkReachable(es []wfEntry) bool {
	found := false
	for _, e := range es {
		var words []string
		switch {
		case !wfTopIs(e, "on"):
			continue
		case len(e.path) == 1:
			words = wfWordRE.FindAllString(e.text, -1) // on: push, on: [push, pull_request], or a "- push" item
		case len(e.path) == 2:
			words = []string{e.path[1]} // on:\n  pull_request:
		}
		for _, w := range words {
			found = true
			if !wfForkSafe[w] {
				return true
			}
		}
	}
	return !found
}

// wfRunner: a line that names a self-hosted runner, or a runs-on value that is not one GitHub-hosted label (a group,
// labels, an expression or a list with anything else in it).
func wfRunner(e wfEntry) bool {
	if strings.Contains(strings.ToLower(e.String()), "self-hosted") {
		return true
	}
	if !e.under("runs-on") || e.text == "" {
		return false
	}
	if e.path[len(e.path)-1] != "runs-on" {
		return true // runs-on: {group: ..., labels: ...}
	}
	v := strings.TrimSpace(wfCommentRE.ReplaceAllString(e.text, ""))
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		v = v[1 : len(v)-1]
	}
	for _, item := range strings.Split(v, ",") {
		if !wfHostedRE.MatchString(strings.Trim(strings.TrimSpace(item), `'"`)) {
			return true
		}
	}
	return false
}

// workflowProblems compares one workflow file before and after (either may be "" for an added or deleted file).
func workflowProblems(oldText, newText string) []string {
	oldE, newE := wfEntries(oldText), wfEntries(newText)
	var out []string
	add := func(rule string, lines []string) {
		sort.Strings(lines)
		for _, l := range lines {
			out = append(out, rule+": "+l)
		}
	}
	// fail closed on a layout the line reader cannot follow: a flow/JSON document, or no block-style top-level on:
	if strings.TrimSpace(newText) != "" {
		hasOn := false
		for _, e := range newE {
			hasOn = hasOn || wfTopIs(e, "on")
		}
		for _, raw := range strings.Split(newText, "\n") {
			if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
				hasOn = false
				break
			}
		}
		if !hasOn {
			out = append(out, "cannot read the triggers: no block-style top-level on: key (flow or JSON workflows are refused)")
		}
	}
	// on: triggers and permissions: any added, removed or changed line
	for _, rule := range []struct {
		name string
		keep func(wfEntry) bool
	}{
		{"on: trigger changed", func(e wfEntry) bool { return wfTopIs(e, "on") }},
		{"permissions changed", func(e wfEntry) bool { return e.under("permissions") }},
	} {
		o, n := wfSelect(oldE, rule.keep), wfSelect(newE, rule.keep)
		add(rule.name+" (added)", wfAdded(o, n))
		add(rule.name+" (removed)", wfAdded(n, o))
	}
	// a trigger word anywhere new (a flow mapping or odd quoting the line reader misses)
	for _, w := range []string{"pull_request_target", "workflow_run"} {
		has := func(e wfEntry) bool { return strings.Contains(e.String(), w) }
		add(w+" added", wfAdded(wfSelect(oldE, has), wfSelect(newE, has)))
	}
	// secrets: any added or changed line that names secrets (expressions, secrets: inherit)
	sec := func(e wfEntry) bool { return wfSecretsRE.MatchString(e.String()) }
	add("secrets reference added", wfAdded(wfSelect(oldE, sec), wfSelect(newE, sec)))
	// self-hosted runners on a fork-reachable workflow
	if wfForkReachable(newE) {
		add("non-GitHub-hosted runner on a fork-reachable workflow", wfAdded(wfSelect(oldE, wfRunner), wfSelect(newE, wfRunner)))
	}
	return out
}
