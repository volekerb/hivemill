package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
)

// pr (G-048 T-1462): cmd_pr in bin/factory_cli.py (4634). The repo comes from the own task's project in the DB and the
// branch from the own worktree, never from arguments or the environment; the child env drops GH_REPO and GH_HOST.
// A repo under public_text_repos needs the Python text guard, so that case hands over before stdin is read.

func init() {
	register("pr", "", handler{parse: parsePr, run: runPr})
}

type prCmd struct {
	action      string
	title, body *string
}

// parsePr accepts `pr create|ready` with --title and --body (each at most once, value not starting with "-" except
// exactly "-"). Everything argparse could read differently delegates.
func parsePr(rest []string) (any, bool) {
	pos, vals, ok := msgArgs(rest, []string{"--title", "--body"}, 1, 1)
	if !ok || (pos[0] != "create" && pos[0] != "ready") {
		return nil, false
	}
	return prCmd{action: pos[0], title: msgOpt(vals, "--title"), body: msgOpt(vals, "--body")}, true
}

// ghSlugRE is ghcred.SLUG_RE (scout/ghcred.py 26), used with fullmatch.
var ghSlugRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// gitOptions is the hardening the Python git_run and gh_git_env apply: the factory home and the live homes.
func gitOptions(c *ctx) (gitx.Options, error) {
	_, homes, err := c.env.DeriveLiveHomes()
	if err != nil {
		return gitx.Options{}, errDelegate
	}
	live := make([]string, 0, len(homes))
	for h := range homes {
		live = append(live, h)
	}
	sort.Strings(live)
	return gitx.Options{Home: c.env.Home(), LiveHomes: live}, nil
}

var (
	slugQueryRE  = regexp.MustCompile(`[?#].*$`)
	slugSchemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/]*/`)
	slugUserRE   = regexp.MustCompile(`^[^/@:]*@[^/:]*:`)
)

// repoSlug is _repo_slug: OWNER/NAME lower-cased without ".git", "" when there is none. Input the Go regexps could read
// differently from Python's (a line break, a non-ASCII character) reports ok=false and the caller hands over.
func repoSlug(s string) (slug string, ok bool) {
	u := pyStrip(s)
	if u == "" {
		return "", true
	}
	for _, r := range u {
		if r > 0x7e || r < 0x20 {
			return "", false
		}
	}
	u = slugQueryRE.ReplaceAllString(u, "")
	u = slugSchemeRE.ReplaceAllString(u, "")
	u = slugUserRE.ReplaceAllString(u, "")
	var parts []string
	for _, p := range strings.Split(strings.TrimRight(u, "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return "", true
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if len(name) >= 4 && strings.EqualFold(name[len(name)-4:], ".git") {
		name = name[:len(name)-4]
	}
	if owner == "" || name == "" || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", true
	}
	return strings.ToLower(owner + "/" + name), true
}

// cfgSlugs is _cfg_slugs: the set of slugs in a comma-list config key; ok=false when a slug needs Python.
func cfgSlugs(c *ctx, key string) (map[string]bool, bool, error) {
	v, err := cfgText(c, key)
	if err != nil {
		return nil, false, err
	}
	out := map[string]bool{}
	for _, item := range strings.Split(v, ",") {
		s, ok := repoSlug(item)
		if !ok {
			return nil, false, nil
		}
		if s != "" {
			out[s] = true
		}
	}
	return out, true, nil
}

// ghEnv is gh_git_env() without the variables a caller could use to redirect gh.
func ghEnv(o gitx.Options, drop ...string) []string {
	var out []string
	for _, kv := range ghx.GitEnv(o) {
		k, _, _ := strings.Cut(kv, "=")
		skip := false
		for _, d := range drop {
			if k == d {
				skip = true
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

var (
	scTitleTagRE = regexp.MustCompile(`^\[SC\d+(?:\s*,\s*SC\d+)*\]\s*`)
	prTitleSepRE = regexp.MustCompile(`^[ :-]+`)
)

// prCreateTitle is _pr_create_gh_title (T-1263, G-039): "T-1234: description". Only the task id gives the task title
// from the database minus one leading [SCn] tag; a title without the id gets it in front; id plus description keeps it.
func prCreateTitle(tid, givenRaw, dbTitle string) (string, error) {
	emit := func(desc string) (string, error) {
		d := pyStrip(desc)
		if d == "" || strings.EqualFold(d, tid) {
			return "", dieErr(2, "title needs a description")
		}
		return tid + ": " + d, nil
	}
	dbDesc := pyStrip(scTitleTagRE.ReplaceAllString(pyStrip(dbTitle), ""))
	given := pyStrip(givenRaw)
	if len(given) >= len(tid) && strings.EqualFold(given[:len(tid)], tid) {
		after := given[len(tid):]
		if after == "" {
			return emit(dbDesc)
		}
		if loc := prTitleSepRE.FindStringIndex(after); loc != nil {
			rem := pyStrip(after[loc[1]:])
			if rem == "" || strings.EqualFold(rem, tid) {
				return emit(dbDesc)
			}
			return emit(rem)
		}
	}
	return emit(given)
}

func runPr(c *ctx, parsed any) (int, error) {
	a := parsed.(prCmd)
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	if role != "coder" {
		return 0, dieErr(3, fmt.Sprintf("role '%s' (%s) may not do this: `factory pr` is for coders; ask your lead", role, c.who))
	}
	var tid sql.NullString
	err = c.db.QueryRow("SELECT task_id FROM agents WHERE name=?", c.who).Scan(&tid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read agent %s: %w", c.who, err)
	}
	if tid.String == "" {
		return 0, dieErr(3, fmt.Sprintf("%s has no task: `factory pr` opens the PR of your own task; ask your lead", c.who))
	}
	var ghRepo, base, taskBranch, taskTitle sql.NullString
	err = c.db.QueryRow("SELECT p.gh_repo, p.base_branch, t.branch, t.title FROM tasks t JOIN goals g ON g.id=t.goal_id "+
		"JOIN projects p ON p.id=g.project_id WHERE t.id=?", tid.String).Scan(&ghRepo, &base, &taskBranch, &taskTitle)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read project of %s: %w", tid.String, err)
	}
	repo := pyStrip(ghRepo.String)
	if !ghSlugRE.MatchString(repo) {
		return 0, dieErr(3, fmt.Sprintf("%s: its project has no valid gh_repo (%s); ask your lead to set it with `factory project update`", tid.String, verdictRepr(repo)))
	}
	if a.action == "create" && a.body != nil && *a.body == "-" {
		pub, ok, err := cfgSlugs(c, "public_text_repos")
		if err != nil {
			return 0, err
		}
		rs, rok := repoSlug(repo)
		if !ok || !rok || pub[rs] {
			return 0, errDelegate // the G-021 public text guard is Python; nothing is read or written yet
		}
	}
	opts, err := gitOptions(c)
	if err != nil {
		return 0, err
	}
	cwd, err := c.deps.getwd()
	if err != nil {
		return 0, errDelegate
	}
	bo := opts
	bo.Timeout = 30 * time.Second
	g, err := gitx.Run(cwd, bo, "branch", "--show-current")
	if err != nil {
		return 0, errDelegate // OSError or timeout: Python words it, and nothing was written
	}
	branch := ""
	if g.ExitCode == 0 {
		branch = pyStrip(g.Stdout)
	}
	want := "factory/" + strings.ToLower(tid.String)
	dbOK := taskBranch.String != "" && branch == taskBranch.String &&
		regexp.MustCompile(`(?i)^factory/`+regexp.QuoteMeta(tid.String)+`(-v[0-9]+)?$`).MatchString(branch)
	if branch != want && !dbOK {
		shown := branch
		if shown == "" {
			shown = "(none)"
		}
		return 0, dieErr(3, fmt.Sprintf("current branch is %s, not %s or the task's recorded branch (factory/%s-vN, set in the task): "+
			"run `factory pr` from your own task worktree", verdictRepr(shown), verdictRepr(want), strings.ToLower(tid.String)))
	}
	env := ghEnv(opts, "GH_REPO", "GH_HOST")
	var argv []string
	input := ""
	if a.action == "create" {
		if a.body == nil || *a.body != "-" {
			return 0, dieErr(2, "`factory pr create` takes the body on stdin only: --body - with a heredoc")
		}
		bp, err := readText(c, a.body, false)
		if err != nil {
			return 0, err
		}
		input = *bp
		title := ""
		if a.title != nil {
			title = *a.title
		}
		if pyStrip(title) == "" || input == "" {
			return 0, dieErr(2, "`factory pr create` needs a non-empty --title and a body on stdin")
		}
		title, terr := prCreateTitle(tid.String, title, taskTitle.String)
		if terr != nil {
			return 0, terr
		}
		argv = []string{"pr", "create", "--repo", repo, "--base", base.String, "--head", branch, "--title", title, "--body-file", "-"}
	} else {
		if a.title != nil || a.body != nil {
			return 0, dieErr(2, "`factory pr ready` takes no arguments: it targets the PR of your own branch")
		}
		argv = []string{"pr", "ready", branch, "--repo", repo}
	}
	res, err := ghx.Run(env, input, ghTimeout, argv...)
	if err != nil {
		return 0, dieErr(1, fmt.Sprintf("gh pr %s failed to run: %v", a.action, err))
	}
	if res.ExitCode == 0 {
		if err := event(c, "task", tid.String, "pr-"+a.action, strp(repo+" "+branch)); err != nil {
			return 0, fmt.Errorf("record pr-%s: %w", a.action, err)
		}
	}
	c.out.WriteString(res.Stdout)
	c.errb.WriteString(res.Stderr)
	return res.ExitCode, nil
}
