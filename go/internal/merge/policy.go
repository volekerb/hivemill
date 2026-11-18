package merge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	slugQuery   = regexp.MustCompile(`[?#][^\n]*$`)
	slugScheme  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/]*/`)
	slugAtColon = regexp.MustCompile(`^[^/@:]*@[^/:]*:`)
	slugDotGit  = regexp.MustCompile(`(?i)\.git$`)
)

// RepoSlug is _repo_slug: OWNER/NAME (lower case, no .git) of a GitHub URL, scp-style remote, path or bare slug;
// ok=false when it holds no two-part name.
func RepoSlug(s string) (string, bool) {
	u := pyStrip(s)
	if u == "" {
		return "", false
	}
	u = slugQuery.ReplaceAllString(u, "")
	u = slugScheme.ReplaceAllString(u, "")
	u = slugAtColon.ReplaceAllString(u, "")
	var parts []string
	for _, p := range strings.Split(strings.TrimRight(u, "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return "", false
	}
	owner, name := parts[len(parts)-2], slugDotGit.ReplaceAllString(parts[len(parts)-1], "")
	if owner == "" || name == "" || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", false
	}
	return pyLower(owner + "/" + name), true
}

// CfgSlugs is _cfg_slugs: the set of slugs of a comma list.
func CfgSlugs(value string) map[string]bool {
	out := map[string]bool{}
	for _, i := range strings.Split(value, ",") {
		if s, ok := RepoSlug(i); ok {
			out[s] = true
		}
	}
	return out
}

// PushPolicy is _push_policy: the refusal when a merge may not target `target`, "" when allowed.
func PushPolicy(frozenRepos, target string) string {
	if pyStrip(frozenRepos) == "" {
		return ""
	}
	slug, ok := RepoSlug(target)
	if !ok {
		return fmt.Sprintf("cannot tell which repo %s is while frozen_repos is set (G-021); use a github OWNER/NAME remote", PyRepr(target))
	}
	if CfgSlugs(frozenRepos)[slug] {
		return fmt.Sprintf("repo %s is frozen (G-021)", slug)
	}
	return ""
}

// JudgeSlugs is _judge_slugs: the PR repo's slug and the project's gh_repo slug, deduplicated, in that order.
func JudgeSlugs(ownerRepo, projectGHRepo string) []string {
	var out []string
	for _, x := range []string{ownerRepo, projectGHRepo} {
		s, ok := RepoSlug(x)
		if !ok {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == s
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

// RedMain is the stored red-main record of a repo (config key red_main:<slug>) as Python's dict; Raw holds its
// decoded fields. Present is false while main is not known red.
type RedMain struct {
	Present bool
	Fields  map[string]any
}

// ParseRedMain is red_main_state for the stored value: empty or JSON null is no record; a JSON object is the record;
// anything else (a non-object, unreadable JSON) is a fail-closed record {"run": "?", ...}.
func ParseRedMain(value string) RedMain {
	if value == "" {
		return RedMain{}
	}
	var v any
	if err := json.Unmarshal([]byte(value), &v); err != nil {
		return failClosedRed()
	}
	if v == nil {
		return RedMain{}
	}
	if m, ok := v.(map[string]any); ok {
		return RedMain{Present: true, Fields: m}
	}
	return failClosedRed()
}

func failClosedRed() RedMain {
	return RedMain{Present: true, Fields: map[string]any{"run": "?", "sha": "", "url": "", "since": ""}}
}

func (r RedMain) get(k string) any { return r.Fields[k] }

// RedMainRefusal is red_main_refusal: why a merge into one of slugs must wait for a green push run, "" when it may go.
// The record the check filed for taskID (the revert/fix task) may merge.
func RedMainRefusal(freezeOn bool, slugs []string, taskID string, state func(slug string) RedMain) (string, error) {
	if !freezeOn {
		return "", nil
	}
	for _, slug := range slugs {
		st := state(slug)
		if !st.Present {
			continue
		}
		if t, ok := st.get("task").(string); ok && taskID != "" && t == taskID {
			continue
		}
		run, ok := pyScalar(st.get("run"))
		if !ok {
			return "", ErrUnsupported
		}
		url := "no url"
		if u := st.get("url"); u != nil {
			us, ok := truthy(u)
			s, sok := u.(string)
			if !ok || (us && !sok) {
				return "", ErrUnsupported
			}
			if us {
				url = s
			}
		}
		return fmt.Sprintf("red main: run %s on %s failed (%s); merges wait for a green push run", run, slug, url), nil
	}
	return "", nil
}

// MainRunRefusal is main_run_refusal (flag judge_wait_main_run): the tests.yml push run for main's head must be
// completed+success. fetch answers main_head_run: the head sha and the newest run object for it (nil when none); its
// error text is Python's OSError/TimeoutExpired text, or ErrUnsupported when Python's text cannot be reproduced.
func MainRunRefusal(on bool, slugs []string, taskID string, state func(slug string) RedMain,
	fetch func(slug string) (head string, run map[string]any, err error)) (string, error) {
	if !on {
		return "", nil
	}
	for _, slug := range slugs {
		st := state(slug)
		fix := false
		if t, ok := st.get("task").(string); ok && st.Present && taskID != "" && t == taskID {
			fix = true
		}
		head, run, err := fetch(slug)
		if err != nil {
			if err == ErrUnsupported {
				return "", err
			}
			return fmt.Sprintf("main run unknown on %s: %s; merges wait until gh answers (judge_wait_main_run=1)", slug, err), nil
		}
		url := func(r map[string]any) (string, error) {
			u := r["url"]
			if u == nil {
				return "no url", nil
			}
			s, ok := u.(string)
			if !ok {
				return "", ErrUnsupported
			}
			if s == "" {
				return "no url", nil
			}
			return s, nil
		}
		status, _ := run["status"].(string)
		if run == nil || status != "completed" {
			state := "has not started"
			shownURL := "no url"
			if run != nil {
				if status == "" {
					if run["status"] != nil {
						return "", ErrUnsupported
					}
					status = "unknown"
				}
				state = "is " + status
				u, err := url(run)
				if err != nil {
					return "", err
				}
				shownURL = u
			}
			return fmt.Sprintf("main run %s for %s head %s (%s); merges wait for it to finish (judge_wait_main_run=1)", state, slug, short(head), shownURL), nil
		}
		concl, cok := run["conclusion"].(string)
		if (!cok && run["conclusion"] != nil) || (concl != "success" && !fix) {
			c, ok := pyScalar(run["conclusion"])
			id, ok2 := pyScalar(run["databaseId"])
			u, err := url(run)
			if !ok || !ok2 || err != nil {
				return "", ErrUnsupported
			}
			return fmt.Sprintf("main run %s on %s head %s concluded %s (%s); merges wait for a green run (judge_wait_main_run=1)", id, slug, short(head), c, u), nil
		}
	}
	return "", nil
}
