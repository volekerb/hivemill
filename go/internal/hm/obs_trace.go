package hm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// The task trace (GET /api/obs/task): one timeline per task, folded from the events of the obs model. It reads only.

// Trace is one task's timeline. TotalUSD is the sum of its sessions' costs, as on the task page.
type Trace struct {
	Task     string      `json:"task"`
	Goal     string      `json:"goal,omitempty"`
	Project  string      `json:"project,omitempty"`
	Steps    []TraceStep `json:"steps"`
	TotalUSD float64     `json:"total_usd"`
}

// TraceStep is one meaningful event. Kind is the event's name; Model and Machine are set on session steps.
type TraceStep struct {
	TS      string     `json:"ts"`
	Kind    string     `json:"kind"`
	Role    string     `json:"role,omitempty"`
	Model   string     `json:"model,omitempty"`
	Machine string     `json:"machine,omitempty"`
	Summary string     `json:"summary"`
	CostUSD *float64   `json:"cost_usd,omitempty"`
	Links   TraceLinks `json:"links"`
}

// TraceLinks are the PR (the CI checks are on it) and the session page of a step.
type TraceLinks struct {
	PR      string `json:"pr,omitempty"`
	Session string `json:"session,omitempty"`
}

// buildTrace folds the task's events (in time order) into its timeline; events of other kinds are left out.
func buildTrace(t *ObsTask) Trace {
	out := Trace{Task: t.Task, Goal: t.Goal, Project: t.Project, Steps: []TraceStep{}, TotalUSD: t.Row.USD}
	// session.ended is cumulative and can repeat: only a session's last line with a cost is its step, so costs add up.
	ended := map[string]int{}
	for i, e := range t.Events {
		if e.Event != "session.ended" {
			continue
		}
		if j, ok := ended[e.Session]; !ok || e.CostUSD != nil || t.Events[j].CostUSD == nil {
			ended[e.Session] = i
		}
	}
	for i, e := range t.Events {
		if e.Event == "session.ended" && ended[e.Session] != i {
			continue
		}
		summary, ok := traceSummary(e)
		if !ok {
			continue
		}
		s := TraceStep{TS: e.TS, Kind: e.Event, Role: e.Role, CostUSD: e.CostUSD,
			Summary: truncate(strings.Join(strings.Fields(summary), " "), 900)}
		if e.Event == "session.started" || e.Event == "session.ended" {
			s.Model, s.Machine = e.Model, e.Machine
		}
		s.Links.PR, _ = e.Data["pr"].(string)
		if e.Session != "" {
			s.Links.Session = "/session?s=" + url.QueryEscape(e.Session)
		}
		out.Steps = append(out.Steps, s)
	}
	return out
}

// traceSummary is the step's one line; false = the event is not a step of the timeline.
func traceSummary(e Event) (string, bool) {
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	list := func(k string) string {
		xs, _ := e.Data[k].([]any)
		parts := make([]string, 0, len(xs))
		for _, x := range xs {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, "; ")
	}
	with := func(head, detail string) (string, bool) {
		if detail == "" {
			return head, true
		}
		return head + ": " + detail, true
	}
	switch e.Event {
	case "task.dispatched":
		return "dispatched", true
	case "session.started", "session.ended":
		head := fmt.Sprintf("%s session %s on %s", or(e.Role, "agent"), strings.TrimPrefix(e.Event, "session."), e.Machine)
		if e.Model != "" {
			head += " (" + e.Model + ")"
		}
		if e.CostUSD != nil {
			head += fmt.Sprintf(", $%.2f", *e.CostUSD)
		}
		return head, true
	case "gate.deferred":
		return with("gate deferred", str("why"))
	case "gate.blocked":
		return with("gate blocked", list("problems"))
	case "gate.passed":
		var rr []Rerun
		b, _ := json.Marshal(e.Data["reruns"])
		_ = json.Unmarshal(b, &rr)
		notes := make([]string, 0, len(rr))
		for _, r := range rr {
			notes = append(notes, fmt.Sprintf("%d %s %s", r.ID, r.Kind, r.Note))
		}
		return with("gate passed", strings.Join(notes, "; "))
	case "review.findings":
		return fmt.Sprintf("review: %d findings, %d fixed", num(e.Data["count"]), num(e.Data["fixed"])), true
	case "ci.red":
		return with(fmt.Sprintf("CI red (round %d)", num(e.Data["ci_round"])), list("failed"))
	case "judge.verdict":
		return with("judge "+str("verdict"), list("reasons"))
	case "run.held":
		return with("held", str("why"))
	case "pr.merged":
		return "merged", true
	case "run.long_working":
		return fmt.Sprintf("long in working: %d minutes, %d%% of the %d minute limit", num(e.Data["minutes"]),
			num(e.Data["pct"]), num(e.Data["limit"])), true
	case "task.failed":
		return with("failed", str("reason"))
	case "prompt.sent": // kind and path only: the prompt's text never reaches the trace
		return with("prompt sent", strings.TrimSpace(str("kind")+" "+str("path")))
	}
	return "", false
}
