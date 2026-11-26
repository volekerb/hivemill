package hm

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The pass's one poll (docs/pr-flow.md): every open runner PR, the base protection and the checks of the last base
// commits in one GraphQL query, cached until the pass ends.

type prPoll struct {
	prs   map[string]*PR       // by PR URL
	errs  map[string]error     // by PR URL: why it could not be read
	bases map[string]*basePoll // by baseKey
}

type basePoll struct {
	NotStrict bool // the protection of the base requires status checks and is not strict
	Commits   []baseCommit
}

// baseCommit is one commit of the base branch, newest first.
type baseCommit struct {
	Oid    string
	Suites []baseSuite
}

// baseSuite is one workflow run on a base commit.
type baseSuite struct {
	Status, Conclusion string
	Event, Workflow    string
	Path               string // the workflow's resource path, ".../tests.yml"
	Jobs               []baseJob
}

type baseJob struct{ Name, Conclusion, CompletedAt string }

func baseKey(repo, base string) string { return strings.ToLower(repo) + ":" + base }

var prURLRe = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/(\d+)`)

const (
	prFields = `number state isDraft mergeable title body url headRefOid headRefName baseRefName mergedAt mergeCommit{oid} ` +
		`commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){pageInfo{hasNextPage} nodes{__typename ` +
		`... on CheckRun{name status conclusion startedAt completedAt detailsUrl checkSuite{workflowRun{workflow{name}}}} ` +
		`... on StatusContext{context state createdAt targetUrl}}}}}}}`
	baseFields = `branchProtectionRule{requiresStatusChecks requiresStrictStatusChecks} target{... on Commit{history(first:30){nodes{oid ` +
		`checkSuites(first:10){nodes{status conclusion workflowRun{event workflow{name resourcePath}} ` +
		`checkRuns(first:30){nodes{name conclusion completedAt}}}}}}}}`
)

type gqlPR struct {
	PR
	Commits struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					Contexts struct {
						PageInfo struct{ HasNextPage bool }
						Nodes    []map[string]any
					}
				} `json:"statusCheckRollup"`
			}
		}
	}
}

type gqlRef struct {
	BranchProtectionRule *struct{ RequiresStatusChecks, RequiresStrictStatusChecks bool }
	Target               struct {
		History struct {
			Nodes []struct {
				Oid         string
				CheckSuites struct {
					Nodes []struct {
						Status, Conclusion string
						WorkflowRun        *struct {
							Event    string
							Workflow struct{ Name, ResourcePath string }
						}
						CheckRuns struct{ Nodes []baseJob }
					}
				}
			}
		}
	}
}

// rollupRow turns a GraphQL check into the row shape `gh pr view --json statusCheckRollup` gives.
func rollupRow(n map[string]any) map[string]any {
	row := map[string]any{}
	for k, v := range n {
		if s, ok := v.(string); ok {
			row[k] = s
		}
	}
	suite, _ := n["checkSuite"].(map[string]any)
	wr, _ := suite["workflowRun"].(map[string]any)
	wf, _ := wr["workflow"].(map[string]any)
	if name, _ := wf["name"].(string); name != "" {
		row["workflowName"] = name
	}
	return row
}

// pollRuns are the runs whose PR a pass reads: the active ones and those held for the owner.
func pollRuns(runs []Run) []Run {
	var out []Run
	for _, x := range runs {
		if x.PRURL != "" && (Active(x.State) || (heldForOwner(&x) && !x.Eval)) {
			out = append(out, x)
		}
	}
	return out
}

// pollPRs reads every PR of runs with one GraphQL query; no runs, no call.
func (r *Runner) pollPRs(runs []Run) *prPoll {
	p := &prPoll{prs: map[string]*PR{}, errs: map[string]error{}, bases: map[string]*basePoll{}}
	type ref struct {
		owner, name string
		urls        map[string]string // PR number -> run's PR URL
		bases       []string
	}
	repos := map[string]*ref{}
	var order []string
	for _, x := range pollRuns(runs) {
		m := prURLRe.FindStringSubmatch(x.PRURL)
		if m == nil {
			p.errs[x.PRURL] = fmt.Errorf("poll: %s is not a pull request link", x.PRURL)
			continue
		}
		k := strings.ToLower(m[1] + "/" + m[2])
		g := repos[k]
		if g == nil {
			g = &ref{owner: m[1], name: m[2], urls: map[string]string{}}
			repos[k], order = g, append(order, k)
		}
		g.urls[m[3]] = x.PRURL
		if x.Base != "" && !slices.Contains(g.bases, x.Base) {
			g.bases = append(g.bases, x.Base)
		}
	}
	if len(order) == 0 {
		return p
	}
	var q strings.Builder
	q.WriteString("query{")
	for i, k := range order {
		g := repos[k]
		fmt.Fprintf(&q, "r%d:repository(owner:%s,name:%s){", i, strconv.Quote(g.owner), strconv.Quote(g.name))
		for num := range g.urls {
			fmt.Fprintf(&q, "p%s:pullRequest(number:%s){%s}", num, num, prFields)
		}
		for j, b := range g.bases {
			fmt.Fprintf(&q, "b%d:ref(qualifiedName:%s){%s}", j, strconv.Quote("refs/heads/"+b), baseFields)
		}
		q.WriteString("}")
	}
	q.WriteString("}")
	rc, out, errs := r.Tools.GH("", "api", "graphql", "-f", "query="+q.String())
	var doc struct {
		Data map[string]map[string]json.RawMessage
	}
	// gh exits 1 when any part of the query errored; what was answered is still good
	err := json.Unmarshal([]byte(out), &doc)
	if err != nil || doc.Data == nil {
		why := fmt.Errorf("poll: gh api graphql: exit %d: %s", rc, firstLine(errs))
		r.printf("%v\n", why)
		for _, k := range order {
			for _, url := range repos[k].urls {
				p.errs[url] = why
			}
		}
		return p
	}
	for i, k := range order {
		g, got := repos[k], doc.Data[fmt.Sprintf("r%d", i)]
		for num, url := range g.urls {
			var n *gqlPR
			if json.Unmarshal(got["p"+num], &n) != nil || n == nil || n.State == "" {
				p.errs[url] = errors.New("poll: no answer for " + url)
				continue
			}
			pr := n.PR
			pr.Checks = nil
			if c := n.Commits.Nodes; len(c) > 0 && c[0].Commit.Rollup != nil {
				ctx := c[0].Commit.Rollup.Contexts
				if ctx.PageInfo.HasNextPage { // more checks than one page: never judge a part of them
					full, err := PRView(r.Tools, url)
					if err != nil {
						p.errs[url] = err
						continue
					}
					p.prs[url] = full
					continue
				}
				for _, row := range ctx.Nodes {
					pr.Checks = append(pr.Checks, rollupRow(row))
				}
			}
			p.prs[url] = &pr
		}
		for j, b := range g.bases {
			var n *gqlRef
			if json.Unmarshal(got[fmt.Sprintf("b%d", j)], &n) != nil || n == nil {
				continue
			}
			bp := &basePoll{}
			if rule := n.BranchProtectionRule; rule != nil {
				bp.NotStrict = rule.RequiresStatusChecks && !rule.RequiresStrictStatusChecks
			}
			for _, c := range n.Target.History.Nodes {
				bc := baseCommit{Oid: c.Oid}
				for _, s := range c.CheckSuites.Nodes {
					if s.WorkflowRun == nil {
						continue
					}
					bc.Suites = append(bc.Suites, baseSuite{Status: s.Status, Conclusion: s.Conclusion, Event: s.WorkflowRun.Event,
						Workflow: s.WorkflowRun.Workflow.Name, Path: s.WorkflowRun.Workflow.ResourcePath, Jobs: s.CheckRuns.Nodes})
				}
				bp.Commits = append(bp.Commits, bc)
			}
			p.bases[baseKey(g.owner+"/"+g.name, b)] = bp
		}
	}
	return p
}

// prView is the run's PR: from the pass's poll inside a pass, else read directly (commands run by hand).
func (r *Runner) prView(run *Run) (*PR, error) {
	if r.poll == nil {
		return PRView(r.Tools, run.PRURL)
	}
	p := r.poll.prs[run.PRURL]
	if p == nil {
		if err := r.poll.errs[run.PRURL]; err != nil {
			return nil, err
		}
		return nil, errors.New("poll: " + run.PRURL + " is read by the next pass")
	}
	c := *p
	c.Checks = maskRows(run.Rerun, p)
	return &c, nil
}

// pushState is the tests.yml push run of a base commit: green, red, pending, or "" when the commit has none.
func (b *basePoll) pushState(oid string) string {
	for _, c := range b.Commits {
		if c.Oid != oid {
			continue
		}
		state := ""
		for _, s := range c.Suites {
			if !isMainTests(s) {
				continue
			}
			switch {
			case s.Status != "COMPLETED":
				return "pending"
			case s.Conclusion == "SUCCESS":
				state = "green"
			case s.Conclusion == "CANCELLED" || s.Conclusion == "SKIPPED":
				state = or(state, "pending")
			default:
				return "red"
			}
		}
		return state
	}
	return ""
}

func isMainTests(s baseSuite) bool {
	return s.Event == "push" && strings.HasSuffix(s.Path, "/tests.yml")
}

// mainState is mainRun from the poll: the push run of head, none when the base has no such workflow.
func (b *basePoll) mainState(head string) string {
	if s := b.pushState(head); s != "" {
		return s
	}
	for _, c := range b.Commits {
		if slices.ContainsFunc(c.Suites, isMainTests) {
			return "pending"
		}
	}
	return "none"
}

// passedSince reports whether a job of that workflow and name succeeded on a base commit after t.
func (b *basePoll) passedSince(workflow, name string, t time.Time) bool {
	for _, c := range b.Commits {
		for _, s := range c.Suites {
			for _, j := range s.Jobs {
				if s.Workflow != workflow || j.Name != name || j.Conclusion != "SUCCESS" {
					continue
				}
				if done, err := time.Parse(time.RFC3339, j.CompletedAt); err == nil && done.After(t) {
					return true
				}
			}
		}
	}
	return false
}
