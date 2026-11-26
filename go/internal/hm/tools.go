package hm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
)

// Tools are the external programs the runner drives. Tests replace them.
type Tools struct {
	Git func(repo string, args ...string) (int, string, string)
	GH  func(input string, args ...string) (int, string, string)
	HC  func(args ...string) (int, string, string)
	Sh  func(dir string, timeout time.Duration, env []string, argv ...string) (int, string)
}

// RealTools runs git through gitx (hooks off, https only, live-checkout moves refused), gh and hc.
func RealTools(e Env) Tools {
	o := gitOptions(e)
	return Tools{
		Git: func(repo string, args ...string) (int, string, string) { return gitx.RC(repo, o, args...) },
		GH: func(input string, args ...string) (int, string, string) {
			res, err := ghx.Run(nil, input, 120*time.Second, args...)
			var timeout *gitx.TimeoutError
			switch {
			case errors.As(err, &timeout):
				return 124, res.Stdout, res.Stderr + "\ntimed out after 2m0s"
			case err != nil && !errors.Is(err, exec.ErrWaitDelay): // ErrWaitDelay: gh exited 0, a child kept a pipe open
				return 127, res.Stdout, err.Error()
			}
			return res.ExitCode, res.Stdout, res.Stderr
		},
		HC: func(args ...string) (int, string, string) {
			return run(90*time.Second, "", "", nil, append([]string{"hc"}, args...)...)
		},
		Sh: func(dir string, timeout time.Duration, env []string, argv ...string) (int, string) {
			rc, out, errs := run(timeout, dir, "", env, argv...)
			return rc, out + errs
		},
	}
}

func gitOptions(e Env) gitx.Options {
	return gitx.Options{Home: e.Home, LiveHomes: []string{e.Home}, Timeout: 180 * time.Second}
}

// gitAs is RealTools' git with email as author and committer, for the runner's own commits. The GIT_* variables are set
// because inherited ones override `-c user.email`. An empty email sets none of them and passes only the -c values, as before.
func gitAs(e Env, email, repo string, args ...string) (int, string, string) {
	o := gitOptions(e)
	if email != "" { // the last value of a repeated key wins
		o.Env = append(os.Environ(), "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_EMAIL="+email,
			"GIT_AUTHOR_NAME="+nameOf(email), "GIT_COMMITTER_NAME="+nameOf(email))
	}
	return gitx.RC(repo, o, append([]string{"-c", "user.email=" + email, "-c", "user.name=" + nameOf(email)}, args...)...)
}

func run(timeout time.Duration, dir, input string, env []string, argv ...string) (int, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = 30 * time.Second // a child left holding the output pipe must not block past the command
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return 124, out.String(), errb.String() + fmt.Sprintf("\ntimed out after %s", timeout)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // the command itself exited 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String(), errb.String()
	}
	if err != nil {
		return 127, out.String(), err.Error()
	}
	return 0, out.String(), errb.String()
}

// PR is the part of `gh pr view --json` the runner reads.
type PR struct {
	Number      int              `json:"number"`
	State       string           `json:"state"`
	HeadRefOid  string           `json:"headRefOid"`
	HeadRefName string           `json:"headRefName"`
	BaseRefName string           `json:"baseRefName"`
	Mergeable   string           `json:"mergeable"`
	IsDraft     bool             `json:"isDraft"`
	Body        string           `json:"body"`
	Title       string           `json:"title"`
	URL         string           `json:"url"`
	Checks      []map[string]any `json:"statusCheckRollup"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	MergedAt string `json:"mergedAt"`
}

// PRView reads one PR (one gh call).
func PRView(t Tools, url string) (*PR, error) {
	rc, out, errs := t.GH("", "pr", "view", url, "--json",
		"number,state,headRefOid,headRefName,baseRefName,mergeable,isDraft,body,title,url,statusCheckRollup,mergeCommit,mergedAt")
	if rc != 0 {
		return nil, fmt.Errorf("gh pr view %s: exit %d: %s", url, rc, firstLine(errs))
	}
	var p PR
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return nil, fmt.Errorf("gh pr view %s: %w", url, err)
	}
	return &p, nil
}

// CIVerdict is ci_verdict(): green = at least one check and all completed successful (success, neutral, skipped);
// red = all completed and some failed; pending otherwise. Only the newest run of each check counts.
func CIVerdict(checks []map[string]any) (string, []string) {
	verdict, rows := ciRows(checks)
	var failed []string
	for _, c := range rows {
		failed = append(failed, checkName(c))
	}
	return verdict, failed
}

// checkName is the name of a rollup row (a commit status has only a context).
func checkName(c map[string]any) string {
	if name, _ := c["name"].(string); name != "" {
		return name
	}
	name, _ := c["context"].(string)
	return name
}

// ciCancelledRuns lists the Actions run ids of the newest check rows (per workflow and name) that ended CANCELLED.
func ciCancelledRuns(checks []map[string]any) []string {
	newest := map[string]map[string]any{}
	for _, c := range checks {
		wf, _ := c["workflowName"].(string)
		n, _ := c["name"].(string)
		k := wf + "\x00" + n
		at, _ := c["startedAt"].(string)
		if old, ok := newest[k]; ok {
			if oat, _ := old["startedAt"].(string); at < oat {
				continue
			}
		}
		newest[k] = c
	}
	seen := map[string]bool{}
	var ids []string
	for _, c := range newest {
		if concl, _ := c["conclusion"].(string); !strings.EqualFold(concl, "CANCELLED") {
			continue
		}
		link, _ := c["detailsUrl"].(string)
		if m := actionsLinkRe.FindStringSubmatch(link); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	sort.Strings(ids)
	return ids
}

// ciRows is CIVerdict with the failed rows themselves.
func ciRows(checks []map[string]any) (string, []map[string]any) {
	type key struct{ wf, name string }
	latest := map[key]map[string]any{}
	order := []key{}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	for _, c := range checks {
		k := key{str(c, "workflowName"), str(c, "name")}
		if str(c, "__typename") == "StatusContext" || (str(c, "context") != "" && str(c, "name") == "") {
			k = key{"ctx", str(c, "context")}
		}
		ts := func(m map[string]any) string {
			for _, f := range []string{"startedAt", "completedAt", "createdAt"} {
				if v := str(m, f); v != "" {
					return v
				}
			}
			return ""
		}
		if old, ok := latest[k]; !ok {
			order = append(order, k)
			latest[k] = c
		} else if ts(c) >= ts(old) {
			latest[k] = c
		}
	}
	if len(order) == 0 {
		return "pending", nil
	}
	var failed []map[string]any
	done := true
	for _, k := range order {
		c := latest[k]
		if k.wf == "ctx" {
			switch strings.ToUpper(str(c, "state")) {
			case "FAILURE", "ERROR":
				failed = append(failed, c)
			case "SUCCESS":
			default:
				done = false
			}
			continue
		}
		concl := strings.ToUpper(strings.TrimSpace(str(c, "conclusion")))
		finished := strings.ToUpper(str(c, "status")) == "COMPLETED" || (concl != "" && strings.TrimSpace(str(c, "completedAt")) != "") ||
			concl == "STARTUP_FAILURE" // such a check never ran: it may carry neither a status nor a time
		if !finished || concl == "CANCELLED" {
			// cancelled is not a failure: GitHub cancels a run that a newer one superseded or that sat in a full queue.
			// It is pending (never green) until a re-run finishes; Merge re-runs it (rerunCancelled).
			done = false
		} else if concl != "SUCCESS" && concl != "NEUTRAL" && concl != "SKIPPED" {
			failed = append(failed, c)
		}
	}
	if !done {
		return "pending", nil
	}
	if len(failed) > 0 {
		return "red", failed
	}
	return "green", nil
}

// Terminal is one hc terminal (hc ls --json).
type Terminal struct {
	Name, Address, Agent, State, Status string
}

// Terminals lists live hc terminals; nil, false when hc cannot say.
func Terminals(t Tools) ([]Terminal, bool) {
	rc, out, _ := t.HC("ls", "--json")
	if rc != 0 {
		return nil, false
	}
	var doc struct {
		Terminals []Terminal `json:"terminals"`
	}
	if json.Unmarshal([]byte(out), &doc) != nil {
		return nil, false
	}
	return ownTerminals(doc.Terminals), true
}

// ownTerminals drops the terminals other machines' cores started (address "machine/t1"). This core's own terminals
// have a bare address (t1) or, when it started them on another machine, "machine~t1". Without the filter, a runner
// whose hc also lists another runner's machine took that runner's planner for its own (2026-11-26: a second
// runner saw the first runner's "planner" and never started its own).
func ownTerminals(list []Terminal) []Terminal {
	out := list[:0:0]
	for _, t := range list {
		if !strings.Contains(t.Address, "/") {
			out = append(out, t)
		}
	}
	return out
}

func alive(list []Terminal, ok bool, name string) bool {
	if !ok {
		return true // cannot tell: never fail a run on a missing listing
	}
	for _, x := range list {
		if x.Name == name && x.State == "live" {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func writeFile(path, text string, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
