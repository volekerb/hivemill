package hm

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factory/go/internal/kit/spend"
)

// SessionCosts is each session's $ at API prices (subagent transcripts included), from ~/.claude/projects.
func SessionCosts(userHome string, ids []string) map[string]float64 {
	out := map[string]float64{}
	for k, b := range spend.SessionCosts(filepath.Join(userHome, ".claude", "projects"), ids) {
		out[k] = math.Round(b.Cost*10000) / 10000
	}
	return out
}

// Row is one task of the scoreboard.
type Row struct {
	Task          string             `json:"task"`
	Outcome       string             `json:"outcome"`
	USD           float64            `json:"usd"`
	USDTask       float64            `json:"usd_task"`
	USDJudge      float64            `json:"usd_judge"`
	Sessions      map[string]string  `json:"sessions"` // session id -> role
	Cost          map[string]float64 `json:"cost"`
	HoursToMerge  *float64           `json:"hours_to_merge"`
	GateBlocks    int                `json:"gate_blocks"`
	Judge         []string           `json:"judge"`
	CIRounds      int                `json:"ci_rounds"`
	Findings      int                `json:"findings"`
	Fixed         int                `json:"findings_fixed"`
	Criteria      int                `json:"criteria"`
	Proven        int                `json:"criteria_proven"`
	Manual        int                `json:"criteria_manual"`
	RerunFailures int                `json:"rerun_failures"`
	Refreshes     int                `json:"refreshes"`
	GoalCheck     string             `json:"goal_check,omitempty"`
	OwnerCheck    bool               `json:"needs_owner_check"`
	dispatched    string
	merged        string
}

// Baseline is the old pipeline, measured 2026-11-24 (factory review).
var Baseline = map[string]any{"usd_per_merged_pr": "3.56-14", "two_plus_rounds_pct": 50, "median_hours_open_to_merge": 3.4}

// ReportRows builds the scoreboard from the event log, plus the sessions table and each session's transcript for $.
func (r *Runner) ReportRows() ([]Row, error) {
	evs, err := r.Log.Read()
	if err != nil {
		return nil, err
	}
	rows := map[string]*Row{}
	get := func(t string) *Row {
		if rows[t] == nil {
			rows[t] = &Row{Task: t, Outcome: "open", Sessions: map[string]string{}}
		}
		return rows[t]
	}
	for _, e := range evs {
		if e.TaskID == "" {
			continue
		}
		x := get(e.TaskID)
		switch e.Event {
		case "task.dispatched":
			if x.dispatched == "" {
				x.dispatched = e.TS
			}
		case "session.started":
			x.Sessions[e.Session] = e.Role
		case "gate.blocked":
			x.GateBlocks++
		case "gate.passed":
			b, _ := json.Marshal(e.Data["reruns"])
			var rr []Rerun
			_ = json.Unmarshal(b, &rr)
			x.Criteria, x.Proven, x.Manual = len(rr), 0, 0
			for _, q := range rr {
				if q.OK != nil && *q.OK {
					x.Proven++
				}
				if q.Kind == "manual" {
					x.Manual++
				}
			}
		case "review.findings":
			x.Findings += num(e.Data["count"])
			x.Fixed += num(e.Data["fixed"])
		case "judge.verdict":
			x.Judge = append(x.Judge, fmt.Sprint(e.Data["verdict"]))
		case "ci.red":
			x.CIRounds++
		case "pr.refreshed":
			x.Refreshes++
		case "pr.merged":
			x.merged, x.Outcome = e.TS, "merged"
		case "pr.reverted":
			x.Outcome = "reverted"
		case "task.failed":
			x.Outcome = "FAIL: " + truncate(fmt.Sprint(e.Data["reason"]), 60)
		case "task.cancelled":
			x.Outcome = "cancelled"
		case "goal.checked":
			x.GoalCheck = fmt.Sprint(e.Data["result"])
		}
	}
	var ids []string
	out := make([]Row, 0, len(rows))
	for t, x := range rows {
		if sess, err := r.Store.Sessions(t); err == nil {
			for _, s := range sess {
				if _, ok := x.Sessions[s.SID]; !ok {
					x.Sessions[s.SID] = s.Role
				}
			}
		}
		if run, _ := r.Store.Run(t); run != nil {
			x.RerunFailures = run.RerunFailures
			if x.Criteria == 0 {
				x.Criteria = run.Criteria
			}
		}
		for sid := range x.Sessions {
			ids = append(ids, sid)
		}
	}
	costs := SessionCosts(r.Env.UserHome, ids)
	for _, x := range rows {
		x.Cost = map[string]float64{}
		for sid, role := range x.Sessions {
			c := costs[sid]
			x.Cost[sid] = c
			x.USD += c
			if role == "judge" {
				x.USDJudge += c
			} else {
				x.USDTask += c
			}
		}
		x.USD, x.USDTask, x.USDJudge = round2(x.USD), round2(x.USDTask), round2(x.USDJudge)
		if x.merged != "" {
			a, e1 := time.Parse(time.RFC3339, x.dispatched)
			b, e2 := time.Parse(time.RFC3339, x.merged)
			if e1 == nil && e2 == nil {
				h := round2(b.Sub(a).Hours())
				x.HoursToMerge = &h
			}
		}
		x.OwnerCheck = x.Manual > 0
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Task < out[j].Task })
	return out, nil
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// Totals summarise the rows next to the baseline.
type Totals struct {
	Tasks, Merged, GateBlocks int
	USD                       float64
	PerMergedPR, MedianHours  *float64
	TwoPlusRoundsPct          *float64
	Baseline                  map[string]any
}

// Summarise computes the totals.
func Summarise(rows []Row) Totals {
	t := Totals{Tasks: len(rows), Baseline: Baseline}
	var hours []float64
	multi := 0
	for _, x := range rows {
		t.USD += x.USD
		t.GateBlocks += x.GateBlocks
		if x.Outcome == "merged" {
			t.Merged++
			if x.HoursToMerge != nil {
				hours = append(hours, *x.HoursToMerge)
			}
			if len(x.Judge)+x.CIRounds >= 2 {
				multi++
			}
		}
	}
	t.USD = round2(t.USD)
	if t.Merged > 0 {
		p := round2(t.USD / float64(t.Merged))
		t.PerMergedPR = &p
		m := round2(100 * float64(multi) / float64(t.Merged))
		t.TwoPlusRoundsPct = &m
	}
	if len(hours) > 0 {
		sort.Float64s(hours)
		h := hours[len(hours)/2]
		t.MedianHours = &h
	}
	return t
}

func fp(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *p)
}

// Report prints the scoreboard (or JSON).
func (r *Runner) Report(asJSON bool) error {
	rows, err := r.ReportRows()
	if err != nil {
		return err
	}
	tot := Summarise(rows)
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"tasks": rows, "totals": tot}, "", " ")
		r.printf("%s\n", b)
		return nil
	}
	r.printf("%-7s %-10s %6s %6s %6s %6s %3s %-12s %2s %5s %4s %4s %3s %5s sessions\n", "task", "outcome", "$", "$task", "$judge", "h",
		"blk", "judge", "ci", "fix", "crit", "prov", "man", "rfail")
	for _, x := range rows {
		var ss []string
		for sid, role := range x.Sessions {
			ss = append(ss, truncate(sid, 8)+":"+role)
		}
		sort.Strings(ss)
		judge := strings.Join(x.Judge, ",")
		if judge == "" {
			judge = "-"
		}
		r.printf("%-7s %-10s %6.2f %6.2f %6.2f %6s %3d %-12s %2d %2d/%-2d %4d %4d %3d %5d %s\n", x.Task, truncate(x.Outcome, 10), x.USD,
			x.USDTask, x.USDJudge, fp(x.HoursToMerge), x.GateBlocks, truncate(judge, 12), x.CIRounds, x.Fixed, x.Findings, x.Criteria,
			x.Proven, x.Manual, x.RerunFailures, strings.Join(ss, " "))
	}
	for _, x := range rows {
		if x.OwnerCheck {
			r.printf("%s: needs owner check (%d manual criteria)\n", x.Task, x.Manual)
		}
		if x.GoalCheck != "" {
			r.printf("%s: goal check %s\n", x.Task, x.GoalCheck)
		}
	}
	r.printf("\nmerged %d/%d, $%.2f total, $/merged PR %s, median h to merge %s, 2+ rounds %s%%, gate blocks %d\n", tot.Merged, tot.Tasks,
		tot.USD, fp(tot.PerMergedPR), fp(tot.MedianHours), fp(tot.TwoPlusRoundsPct), tot.GateBlocks)
	r.printf("baseline: $/merged PR %v, 2+ rounds %v%%, median h open-to-merge %v\n", Baseline["usd_per_merged_pr"],
		Baseline["two_plus_rounds_pct"], Baseline["median_hours_open_to_merge"])
	return nil
}
