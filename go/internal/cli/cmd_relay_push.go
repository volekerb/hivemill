package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"factory/go/internal/gitx"
	"factory/go/internal/guard"
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
	// GIT_NO_REPLACE_OBJECTS: a worker-planted refs/replace/<sha> must not make the checks read a substitute commit
	out = append(out, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
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

	// every later git read and the push run in an HQ-owned temporary bare repo that borrows the worktree's objects
	// (alternates): git reads only that repo's config, refs and state, so worktree config planted after step 1
	// (url.*.insteadOf, reviewer B2), a .git/shallow that hides commits from the checks, grafts or replace refs are never
	// seen. The objects are content-addressed, so the borrowed store cannot change what a sha names.
	relay, cleanup, err := relayPushRepo(g, opts)
	if err != nil {
		return 0, relayRefuse(a.task, err.Error())
	}
	defer cleanup()
	g = relayGit{c: c, opts: opts, wt: relay, env: g.env}

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

	// 5. the same public-text check the pre-push hook runs, over every commit message, identity, path and added line
	if err := relayPublicText(c, relay, mainSHA, a.sha); err != nil {
		return 0, relayRefuse(a.task, err.Error())
	}

	// 6. push with the HQ gh credential; the token lives only in this process and in git's env
	tout, terr, code, err := c.deps.runCmd("gh", []string{"auth", "token", "--hostname", "github.com"}, wt.String, ghEnv, 30*time.Second)
	token := strings.TrimSpace(tout)
	if err != nil || code != 0 || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return 0, relayRefuse(a.task, fmt.Sprintf("the HQ gh has no github.com token (gh auth token: exit %d %s); run `gh auth login` as the owner",
			code, relayRedact(firstLine(terr), token)))
	}
	push := relayGit{c: c, opts: opts, wt: relay, env: relayScrubEnv(c.deps.Environ, "FACTORY_RELAY_TOKEN="+token)}
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

// relayPushRepo creates the HQ-owned bare repo the push runs in: no templates (no hooks), objects/info/alternates
// pointing at the worktree's object store. cleanup removes it.
func relayPushRepo(wt relayGit, opts gitx.Options) (string, func(), error) {
	objOut, err := wt.ok("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", nil, fmt.Errorf("cannot find the worktree git dir: %v", err)
	}
	objects := filepath.Join(strings.TrimSpace(objOut), "objects")
	if fi, err := os.Stat(objects); err != nil || !fi.IsDir() {
		return "", nil, fmt.Errorf("the worktree object store %s is not a directory", verdictRepr(objects))
	}
	dir, err := os.MkdirTemp("", "factory-relay-")
	if err != nil {
		return "", nil, fmt.Errorf("cannot create the relay push repo: %v; check free space in %s", err, os.TempDir())
	}
	cleanup := func() { os.RemoveAll(dir) }
	r := relayGit{c: wt.c, opts: opts, wt: dir, env: wt.env}
	if _, err := r.ok("init", "-q", "--bare", "--template=", "."); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("cannot create the relay push repo: %v", err)
	}
	info := filepath.Join(dir, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("cannot write the relay push repo alternates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(objects+"\n"), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("cannot write the relay push repo alternates: %v", err)
	}
	return dir, cleanup, nil
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

// relayPublicText runs the Go public-text guard (ci/public_text_guard.py push, the check the pre-push hook makes) over
// main..sha with the owner terms (config scrub_terms_file, else the terms env var spawn exports) and the HQ allowlist
// (<home>/ci/scrub_allowlist.txt). Any hit, or any failure to check, refuses. The refusal names each hit's pattern id and
// its commit (message, author, committer, path) or "an added line"; never the matched text, a masked excerpt or a path.
func relayPublicText(c *ctx, repo, mainSHA, sha string) error {
	allow := filepath.Join(c.env.Home(), "ci", "scrub_allowlist.txt")
	termsFile, err := cfgText(c, "scrub_terms_file")
	if err != nil {
		return fmt.Errorf("cannot read config scrub_terms_file: %v", err)
	}
	var args []string
	if t := strings.TrimSpace(termsFile); t != "" {
		args = append(args, "--terms-file", t)
	}
	args = append(args, "push", "--repo", repo, "--allowlist", allow, mainSHA, sha)
	var out, errb strings.Builder
	code := guard.CIPublicTextGuardMain(args, strings.NewReader(""), &out, &errb)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if code == 0 && last == "0 hits" {
		return nil
	}
	var hits []string
	seen := map[string]bool{}
	for _, ln := range lines[:len(lines)-1] {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		where := "an added line"
		if p := strings.Split(f[1], ":"); len(p) == 3 && p[0] == "commit" && relayShortRE.MatchString(p[1]) {
			switch p[2] {
			case "message", "author", "committer", "path":
				where = "commit " + p[1] + " " + p[2]
			}
		}
		if h := f[0] + " in " + where; !seen[h] {
			seen[h] = true
			hits = append(hits, h)
		}
	}
	if code == 1 && len(hits) > 0 && strings.HasSuffix(last, " hits") {
		return fmt.Errorf("public text in the pushed range (%s): %s; reword or drop the commits, then verify the new head",
			last, strings.Join(hits, "; "))
	}
	msg := "no result"
	for _, ln := range strings.Split(errb.String(), "\n") {
		if strings.HasPrefix(ln, "public-text-guard:") && !strings.HasSuffix(ln, "term(s) loaded") {
			msg = relayRedact(ln, "")
			break
		}
	}
	return fmt.Errorf("the public-text check could not decide (exit %d: %s); set the owner terms (`factory config scrub_terms_file <path>`) "+
		"and make sure %s exists", code, msg, allow)
}

var relayShortRE = regexp.MustCompile(`^[0-9a-f]{4,40}$`)

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

// wfKeyRE: a block mapping key line; YAML allows spaces before the colon (`permissions : write-all`, reviewer B1).
var wfKeyRE = regexp.MustCompile(`^(\s*)(-\s+)?["']?([A-Za-z_][\w-]*)["']?\s*:(?:\s+(.*)|\s*)$`)

var (
	// wfMapLikeRE: a line that is not a key line by wfKeyRE but still reads as a mapping to YAML (an odd key, a
	// complex "? key", a merge key "<<:"): the line reader cannot place it, so it fails closed.
	wfMapLikeRE = regexp.MustCompile(`^\?(\s|$)|:(\s|$)`)
	// wfAnchorRE: a value that is (or starts with) a YAML anchor, alias or tag: an alias repeats content the line reader
	// sees only where the anchor is, so any anchor or alias in a changed workflow is refused.
	wfAnchorRE = regexp.MustCompile(`^[&*!][^\s,\[\]{}]+`)
)

// wfEntry is one significant line: the key path it sits under (jobs.build.runs-on), and its text.
type wfEntry struct {
	path  []string
	text  string
	block bool // block-scalar content (| or >): YAML decodes no escapes there
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

// wfOpenScalar: the value starts a quoted scalar or a flow sequence that does not end on its line. YAML folds the
// next lines into it, so a later line that looks like a key or a block-scalar header (`y: |"`) is not one, and the
// line reader would misplace every line after it (self-review): the caller refuses any workflow that has one.
func wfOpenScalar(val string) bool {
	switch {
	case strings.HasPrefix(val, `"`):
		for i := 1; i < len(val); i++ {
			switch val[i] {
			case '\\':
				i++
			case '"':
				return false
			}
		}
		return true
	case strings.HasPrefix(val, "'"):
		for i := 1; i < len(val); i++ {
			if val[i] == '\'' {
				if i+1 < len(val) && val[i+1] == '\'' {
					i++
					continue
				}
				return false
			}
		}
		return true
	case strings.HasPrefix(val, "["):
		v := strings.TrimSpace(wfCommentRE.ReplaceAllString(val, ""))
		return strings.Count(v, "[") != strings.Count(v, "]") || !strings.HasSuffix(v, "]")
	}
	return false
}

// wfEntries reads a workflow into entries. A key line is an entry under its own path (text = its value); any other
// line (a list item, a block-scalar line) is an entry under the path of the key it belongs to. odd lists the lines
// the reader cannot follow (wfMapLikeRE, a flow mapping value): the caller refuses any odd line the change adds.
func wfEntries(text string) (out []wfEntry, odd []string, multi []string) {
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
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
		if s == "" {
			continue
		}
		ind := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if scalarIndent >= 0 {
			if ind > scalarIndent { // block-scalar content, a "#" line included (a script comment GitHub still expands)
				out = append(out, wfEntry{keys(), s, true})
				continue
			}
			scalarIndent = -1
		}
		if strings.HasPrefix(s, "#") || s == "---" || s == "..." {
			continue
		}
		m := wfKeyRE.FindStringSubmatch(raw)
		if m == nil {
			e := wfEntry{path: keys(), text: strings.TrimPrefix(s, "- ")}
			if wfOpenScalar(e.text) {
				multi = append(multi, e.String())
			}
			if wfMapLikeRE.MatchString(wfCommentRE.ReplaceAllString(e.text, "")) {
				odd = append(odd, e.String())
			}
			out = append(out, e)
			continue
		}
		kind := ind + len(m[2]) // a "- key:" item: the key sits after the dash
		for len(stack) > 0 && stack[len(stack)-1].indent >= kind {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{kind, m[3]})
		val := strings.TrimSpace(m[4])
		out = append(out, wfEntry{path: keys(), text: val})
		if strings.HasPrefix(val, "{") { // a flow mapping hides keys (jobs: {x: {permissions: write-all}})
			odd = append(odd, out[len(out)-1].String())
		}
		if wfOpenScalar(val) {
			multi = append(multi, out[len(out)-1].String())
		}
		if strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			scalarIndent = kind
		}
	}
	return out, odd, multi
}

var (
	wfSecretsRE = regexp.MustCompile(`(?i)\bsecrets\b`) // expression contexts are case-insensitive
	// wfHostedRE: the GitHub-hosted image labels (latest, a version, an arm or larger macOS variant), nothing looser
	wfHostedRE = regexp.MustCompile(`^(?:ubuntu-(?:latest|\d{2}\.\d{2})(?:-arm)?|windows-(?:latest|\d{4}|11)(?:-arm)?|macos-(?:latest|\d{2})(?:-large|-xlarge|-intel)?)$`)
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
	// a lone CR (YAML 1.2) or NEL/LS/PS (YAML 1.1) is a line break to a YAML parser but not to the line reader
	if strings.ContainsAny(strings.ReplaceAll(newText, "\r\n", "\n"), "\r\u0085\u2028\u2029") {
		return []string{"cannot read the lines: a CR, NEL, LS or PS line break other than CRLF (refused)"}
	}
	oldE, oldOdd, _ := wfEntries(oldText)
	newE, newOdd, newMulti := wfEntries(newText)
	var out []string
	add := func(rule string, lines []string) {
		sort.Strings(lines)
		for _, l := range lines {
			out = append(out, rule+": "+l)
		}
	}
	// fail closed on a line the reader cannot place, and on any anchor, alias or tag
	add("cannot read the line (odd key or flow mapping)", wfAdded(oldOdd, newOdd))
	// a multi-line quoted scalar or flow sequence anywhere in the changed file: refused even when the line itself is
	// unchanged, since a change to its continuation lines alone can hide a key (the owner pushes those)
	add("quoted scalar or flow sequence spans lines (refused)", newMulti)
	add("YAML anchor, alias or tag (refused)", wfSelect(newE, func(e wfEntry) bool { return wfAnchorRE.MatchString(e.text) }))
	// a backslash outside a block scalar may be a double-quoted escape (\x73ecrets, \u0073, an escaped line break)
	// that YAML decodes after the line rules ran (reviewer N1): any added or changed one fails closed
	esc := func(e wfEntry) bool { return !e.block && strings.Contains(e.text, `\`) }
	add("backslash outside a block scalar (a YAML escape is refused)", wfAdded(wfSelect(oldE, esc), wfSelect(newE, esc)))
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
