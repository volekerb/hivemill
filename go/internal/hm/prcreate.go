package hm

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"factory/go/internal/guard"
)

// `hivemill pr create|ready`: the task session opens (or marks ready) the PR of its own task without the old factory
// CLI. It mirrors `factory pr` (bin/factory_cli.py cmd_pr): the repo and base come from the task's project in the DB
// and the head from the session's own branch, never from arguments or GH_REPO/GH_HOST, so the PR cannot be aimed at
// another repo; the body comes on stdin; in a public_text_repos repo the title, body and branch must pass the
// public-text check (raw `gh pr create` stays denied by the settings).

var (
	slugRe       = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	scTitleTagRe = regexp.MustCompile(`^\[SC\d+(?:\s*,\s*SC\d+)*\]\s*`)
	prTitleSepRe = regexp.MustCompile(`^[ :-]+`)
)

// ErrUsage is a wrong call of a command (exit 2).
var ErrUsage = fmt.Errorf("usage")

// PRTitle is _pr_create_gh_title: "<task id>: <description>", the description from the given title (a leading task
// id and separator dropped) or else from the task title.
func PRTitle(id, given, taskTitle string) (string, error) {
	given = strings.TrimSpace(given)
	emit := func(d string) (string, error) {
		d = strings.TrimSpace(d)
		if d == "" || strings.EqualFold(d, id) {
			return "", fmt.Errorf("%w: title needs a description", ErrUsage)
		}
		return id + ": " + d, nil
	}
	dbDesc := strings.TrimSpace(scTitleTagRe.ReplaceAllString(strings.TrimSpace(taskTitle), ""))
	if len(given) >= len(id) && strings.EqualFold(given[:len(id)], id) {
		after := given[len(id):]
		if after == "" {
			return emit(dbDesc)
		}
		if m := prTitleSepRe.FindStringIndex(after); m != nil {
			rem := strings.TrimSpace(after[m[1]:])
			if rem == "" || strings.EqualFold(rem, id) {
				return emit(dbDesc)
			}
			return emit(rem)
		}
	}
	return emit(given)
}

// PR runs `hivemill pr create` (title, body) or `hivemill pr ready` for the calling session, from the clone dir cwd.
// It returns gh's output.
func (r *Runner) PR(action, title, body, cwd string) (string, error) {
	s, ok := r.Store.(*SQLite)
	if !ok {
		return "", fmt.Errorf("pr needs the SQLite store")
	}
	actor := r.Env.Actor
	role := s.AgentRole(actor)
	if actor == "" || (role != "task" && role != "coder") {
		return "", fmt.Errorf("%w: `hivemill pr` is for a task session (role task), not %q (role %q)", ErrRefused, actor, role)
	}
	var tid string
	_ = s.db.QueryRow(`SELECT COALESCE(task_id,'') FROM agents WHERE name=?`, actor).Scan(&tid)
	if tid == "" {
		return "", fmt.Errorf("%w: %s has no task: `hivemill pr` opens the PR of your own task", ErrRefused, actor)
	}
	t, err := s.Task(tid)
	if err != nil {
		return "", err
	}
	repo := strings.TrimSpace(t.GHRepo)
	if !slugRe.MatchString(repo) {
		return "", fmt.Errorf("%w: %s: its project has no valid gh_repo (%q)", ErrRefused, tid, repo)
	}
	rc, branch, errs := r.Tools.Git(cwd, "branch", "--show-current")
	if rc != 0 {
		return "", fmt.Errorf("%w: cannot read the current git branch (%s); run `hivemill pr` from your task clone", ErrRefused, firstLine(errs))
	}
	branch = strings.TrimSpace(branch)
	want := "factory/" + strings.ToLower(tid)
	dbOK := t.Branch != "" && branch == t.Branch &&
		regexp.MustCompile(`(?i)^factory/`+regexp.QuoteMeta(tid)+`(-v[0-9]+)?$`).MatchString(branch)
	if branch != want && !dbOK {
		return "", fmt.Errorf("%w: current branch is %q, not %q or the task's recorded branch: run `hivemill pr` from your own task clone",
			ErrRefused, branch, want)
	}
	// gh reads GH_REPO and GH_HOST; --repo is explicit, and the host must be github.com's own
	os.Unsetenv("GH_REPO")
	os.Unsetenv("GH_HOST")
	var argv []string
	input := ""
	switch action {
	case "create":
		if strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("%w: `hivemill pr create` needs a non-empty --title and a body on stdin (--body - with a heredoc)", ErrUsage)
		}
		ghTitle, err := PRTitle(tid, title, t.Title)
		if err != nil {
			return "", err
		}
		if r.publicTextRepo(repo) {
			if why := r.prTextHits(ghTitle, body, branch); why != "" {
				return "", fmt.Errorf("%w: pr create refused (public text): %s", ErrRefused, why)
			}
		}
		// after the check: a body that holds a key block is still refused, not sent redacted
		ghTitle, body = ScrubSecrets(ghTitle), ScrubSecrets(body)
		argv = []string{"pr", "create", "--repo", repo, "--base", t.Base, "--head", branch, "--title", ghTitle, "--body-file", "-"}
		input = body
	case "ready":
		argv = []string{"pr", "ready", branch, "--repo", repo}
	default:
		return "", fmt.Errorf("%w: hivemill pr create --title TITLE --body - | pr ready", ErrUsage)
	}
	rc, out, errs := r.Tools.GH(input, argv...)
	if rc != 0 {
		return out, fmt.Errorf("gh pr %s exited %d: %s", action, rc, strings.TrimSpace(errs))
	}
	_ = r.Store.Event(actor, "task", tid, "pr-"+action, repo+" "+branch)
	return out, nil
}

// publicTextRepo: the repo is in public_text_repos.
func (r *Runner) publicTextRepo(repo string) bool {
	want, _ := guard.CIRepoSlug(repo)
	for _, x := range strings.Split(r.Store.FactoryCfg("public_text_repos"), ",") {
		if s, ok := guard.CIRepoSlug(x); ok && s == want {
			return true
		}
	}
	return false
}

// prTextHits runs the public-text guard on the title, body and branch; "" = clean. A guard that cannot decide (no
// terms, any error) refuses.
func (r *Runner) prTextHits(title, body, branch string) string {
	args := []string{}
	if terms := r.Store.FactoryCfg("scrub_terms_file"); terms != "" {
		args = append(args, "--terms-file", terms)
	}
	var out, errb bytes.Buffer
	rc := guard.CIPublicTextGuardMain(append(args, "text", "-"), strings.NewReader(title+"\n"+body+"\n"+branch), &out, &errb)
	if rc != 0 {
		return fmt.Sprintf("exit %d: %s", rc, lastLines(strings.TrimSpace(out.String()+"\n"+errb.String()), 4))
	}
	return ""
}
