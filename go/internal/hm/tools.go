package hm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
			return run(120*time.Second, "", input, nil, append([]string{"gh"}, args...)...)
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

// gitAs is RealTools' git with email as author and committer, for the runner's own commits. It sets the GIT_* variables
// because inherited ones override `-c user.email`. An empty email leaves the environment as it is.
func gitAs(e Env, email, repo string, args ...string) (int, string, string) {
	o := gitOptions(e)
	if email != "" { // the last value of a repeated key wins
		o.Env = append(os.Environ(), "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_EMAIL="+email,
			"GIT_AUTHOR_NAME="+nameOf(email), "GIT_COMMITTER_NAME="+nameOf(email))
	}
	return gitx.RC(repo, o, args...)
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
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return 124, out.String(), errb.String() + fmt.Sprintf("\ntimed out after %s", timeout)
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
	var failed []string
	done := true
	for _, k := range order {
		c := latest[k]
		name := str(c, "name")
		if name == "" {
			name = str(c, "context")
		}
		if k.wf == "ctx" {
			switch strings.ToUpper(str(c, "state")) {
			case "FAILURE", "ERROR":
				failed = append(failed, name)
			case "SUCCESS":
			default:
				done = false
			}
			continue
		}
		concl := strings.ToUpper(strings.TrimSpace(str(c, "conclusion")))
		finished := strings.ToUpper(str(c, "status")) == "COMPLETED" || (concl != "" && strings.TrimSpace(str(c, "completedAt")) != "")
		if !finished {
			done = false
		} else if concl != "SUCCESS" && concl != "NEUTRAL" && concl != "SKIPPED" {
			failed = append(failed, name)
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
	return doc.Terminals, true
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
