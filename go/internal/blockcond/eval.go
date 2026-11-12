package blockcond

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const releaseListCap = 20

var reReleaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// GoalFacts is goal state supplied by the tick.
type GoalFacts struct {
	Status   string `json:"status"`
	ClosedAt string `json:"closed_at"`
}

// PRFacts is PR state supplied by the tick.
type PRFacts struct {
	State string `json:"state"`
}

// ReleaseFacts is one GitHub release row from the tick.
type ReleaseFacts struct {
	Tag         string `json:"tag"`
	PublishedAt string `json:"published_at"`
}

// Facts is the JSON envelope for Eval.
type Facts struct {
	Now            string               `json:"now"`
	MainGreen      bool                 `json:"main_green"`
	Tasks          map[string]string    `json:"tasks"`
	Goals          map[string]GoalFacts `json:"goals"`
	Releases       []ReleaseFacts       `json:"releases"`
	Config         map[string]string    `json:"config"`
	ConfigAbsent   []string             `json:"config_absent"`
	Decisions      []string             `json:"decisions"`
	PRs            map[string]PRFacts   `json:"prs"`
	PRBudget       []string             `json:"pr_budget"`
	OpenFiles      []string             `json:"open_files"`
	FilesTruncated bool                 `json:"files_truncated"`
	Conditions     []Cond               `json:"conditions"`
}

// ResultItem is one evaluated condition.
type ResultItem struct {
	Index  int    `json:"index"`
	Met    bool   `json:"met"`
	Detail string `json:"detail"`
}

// EvalOutput is the JSON envelope written by block-eval eval.
type EvalOutput struct {
	Results []ResultItem `json:"results"`
	AllMet  bool         `json:"all_met"`
}

// Eval evaluates every condition in facts.
func Eval(f Facts) (EvalOutput, error) {
	now, err := time.Parse(time.RFC3339, f.Now)
	if err != nil {
		return EvalOutput{}, fmt.Errorf("parse now: %w", err)
	}
	decisions := stringSet(f.Decisions)
	configAbsent := stringSet(f.ConfigAbsent)
	prBudget := stringSet(f.PRBudget)
	openFiles := stringSet(f.OpenFiles)

	out := EvalOutput{Results: make([]ResultItem, 0, len(f.Conditions))}
	allMet := len(f.Conditions) > 0

	for i, c := range f.Conditions {
		met, detail, err := evalCond(c, f, now, decisions, configAbsent, prBudget, openFiles)
		if err != nil {
			return EvalOutput{}, err
		}
		out.Results = append(out.Results, ResultItem{Index: i, Met: met, Detail: detail})
		if !met {
			allMet = false
		}
	}
	out.AllMet = allMet
	return out, nil
}

func evalCond(c Cond, f Facts, now time.Time, decisions, configAbsent, prBudget, openFiles map[string]struct{}) (bool, string, error) {
	switch c.Type {
	case "task":
		return evalTask(c.Arg, f.Tasks)
	case "pr":
		return evalPR(c, f.PRs, prBudget)
	case "file":
		return evalFile(c.Arg, openFiles, f.FilesTruncated)
	case "main":
		if f.MainGreen {
			return true, "met", nil
		}
		return false, "unmet", nil
	case "config":
		return evalConfig(c.Arg, f.Config, configAbsent)
	case "owner":
		if _, ok := decisions[c.Arg]; ok {
			return true, "met", nil
		}
		return false, "unmet", nil
	case "until":
		return evalUntil(c.Arg, now)
	case "goal":
		return evalGoal(c, f.Goals, f.Releases)
	case "check":
		ok, err := RunCheckProbe(c.Arg)
		if err != nil {
			return false, "", err
		}
		if ok {
			return true, "met", nil
		}
		return false, "unmet", nil
	default:
		return false, "unmet", nil
	}
}

func evalTask(id string, tasks map[string]string) (bool, string, error) {
	st, ok := tasks[id]
	if !ok {
		return false, "absent", nil
	}
	switch st {
	case "done":
		return true, "met", nil
	case "cancelled":
		return false, "cancelled", nil
	default:
		return false, "unmet", nil
	}
}

func evalPR(c Cond, prs map[string]PRFacts, prBudget map[string]struct{}) (bool, string, error) {
	num := prNumber(c.Arg)
	if _, over := prBudget[num]; over {
		return false, "gh-budget", nil
	}
	pr, ok := prs[num]
	if !ok {
		return false, "absent", nil
	}
	switch c.Form {
	case "merged":
		if pr.State == "MERGED" {
			return true, "merged", nil
		}
		return false, "unmet", nil
	case "closed":
		switch pr.State {
		case "MERGED":
			return true, "merged", nil
		case "CLOSED":
			return true, "closed", nil
		default:
			return false, "unmet", nil
		}
	default:
		return false, "unmet", nil
	}
}

func evalFile(path string, openFiles map[string]struct{}, truncated bool) (bool, string, error) {
	if truncated {
		return false, "gh-budget", nil
	}
	if _, open := openFiles[path]; open {
		return false, "unmet", nil
	}
	return true, "met", nil
}

func evalConfig(arg string, config map[string]string, absent map[string]struct{}) (bool, string, error) {
	key, want, ok := strings.Cut(arg, "=")
	if !ok {
		return false, "unmet", nil
	}
	if _, isAbsent := absent[key]; isAbsent {
		return false, "absent", nil
	}
	got, exists := config[key]
	if !exists {
		return false, "absent", nil
	}
	if got == want {
		return true, "met", nil
	}
	return false, "unmet", nil
}

func evalUntil(arg string, now time.Time) (bool, string, error) {
	deadline, err := time.Parse(time.RFC3339, arg)
	if err != nil {
		return false, "unmet", fmt.Errorf("parse until: %w", err)
	}
	if !now.Before(deadline) {
		return true, "met", nil
	}
	return false, "unmet", nil
}

func evalGoal(c Cond, goals map[string]GoalFacts, releases []ReleaseFacts) (bool, string, error) {
	g, ok := goals[c.Arg]
	if !ok {
		return false, "absent", nil
	}
	switch c.Form {
	case "done":
		switch g.Status {
		case "done":
			return true, "met", nil
		case "cancelled":
			return false, "cancelled", nil
		default:
			return false, "unmet", nil
		}
	case "released":
		if g.Status == "cancelled" {
			return false, "cancelled", nil
		}
		if g.Status != "done" {
			return false, "unmet", nil
		}
		closedAt, err := time.Parse(time.RFC3339, g.ClosedAt)
		if err != nil {
			return false, "unmet", nil
		}
		for _, rel := range releases {
			if !reReleaseTag.MatchString(rel.Tag) {
				continue
			}
			pub, err := time.Parse(time.RFC3339, rel.PublishedAt)
			if err != nil {
				continue
			}
			if !pub.Before(closedAt) {
				return true, "met", nil
			}
		}
		if len(releases) >= releaseListCap {
			return false, "release-list-capped", nil
		}
		return false, "unmet", nil
	default:
		return false, "unmet", nil
	}
}

func prNumber(arg string) string {
	if strings.HasPrefix(arg, "#") {
		return arg[1:]
	}
	return arg
}

func stringSet(ss []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}
