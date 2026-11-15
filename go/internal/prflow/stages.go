package prflow

import (
	"sort"
	"time"
)

// Stage names, in report order.
const (
	StageCIWait       = "ci_wait"
	StageReviewQA     = "review_qa"
	StageCoderRework  = "coder_rework"
	StageCapacityGate = "capacity_gate"
	StageJudge        = "judge"
	StageHold         = "hold"
	StageRefresh      = "refresh"
	StageRedMain      = "red_main"
	StageRedPRUnowned = "red_pr_unowned"
	StageBlocked      = "blocked"
)

// StageOrder is the stable order of stages in every output.
var StageOrder = []string{
	StageCIWait, StageReviewQA, StageCoderRework, StageCapacityGate, StageJudge, StageHold, StageRefresh,
	StageRedMain, StageRedPRUnowned, StageBlocked,
}

// simpleStage maps the actions that always start the same stage.
var simpleStage = map[string]string{
	"pr-create":        StageCIWait,
	"pr-ready":         StageCIWait,
	actCoderDone:       StageCIWait,
	actCoderSpawn:      StageCoderRework,
	"coder-ci-resume":  StageCoderRework,
	"judge-reject":     StageCoderRework,
	"red-pr-unowned":   StageRedPRUnowned,
	"judge-autospawn":  StageJudge,
	"mech-blocked":     StageJudge,
	"judge-hold-clear": StageJudge,
	"judge-hold-set":   StageHold,
	"judge-hold":       StageHold,
	"refreshed":        StageRefresh,
	"verdict-carried":  StageRefresh,
}

var waitKindStage = map[string]string{
	"ci":    StageCIWait,
	"coder": StageCoderRework,
	"gate":  StageCapacityGate,
}

// Segment is one stretch of a PR's life in one stage.
type Segment struct {
	Stage    string
	From, To time.Time
}

// Walk is the result of walking one PR's events.
type Walk struct {
	Segments     []Segment
	ReviewerFail int
	QAFail       int
	Rounds       int
	Refreshes    int
}

// Stages walks a task's events between created and end. The PR starts in ci_wait; each mapped event starts a stage
// that lasts until the next mapped event. Events outside [created, end] and unknown actions are ignored. Time in
// judge, hold or refresh that overlaps a red-main window is reported as red_main.
func Stages(created, end time.Time, events []TaskEvent, red []Interval) Walk {
	var w Walk
	if end.Before(created) {
		end = created
	}
	evs := make([]TaskEvent, 0, len(events))
	for _, e := range events {
		if e.At.Before(created) || e.At.After(end) {
			continue
		}
		evs = append(evs, e)
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })

	stage, from := StageCIWait, created
	var raw []Segment
	enter := func(next string, at time.Time) {
		if next == stage {
			return
		}
		raw = append(raw, Segment{Stage: stage, From: from, To: at})
		stage, from = next, at
	}
	var round struct {
		open     bool
		sha      string
		reviewer string
		qa       string
	}
	for _, e := range evs {
		switch e.Action {
		case "review-autospawn":
			if !round.open || (e.Arg != "" && round.sha != "" && e.Arg != round.sha) {
				w.Rounds++
				round.open, round.sha, round.reviewer, round.qa = true, e.Arg, "", ""
			}
			enter(StageReviewQA, e.At)
		case "reviewer-pass", "reviewer-fail", "qa-pass", "qa-fail":
			if !round.open {
				w.Rounds++
				round.open, round.sha, round.reviewer, round.qa = true, "", "", ""
			}
			switch e.Action {
			case "reviewer-pass":
				round.reviewer = "pass"
			case "reviewer-fail":
				round.reviewer = "fail"
				w.ReviewerFail++
			case "qa-pass":
				round.qa = "pass"
			case "qa-fail":
				round.qa = "fail"
				w.QAFail++
			}
			if round.reviewer != "" && round.qa != "" {
				if round.reviewer == "pass" && round.qa == "pass" {
					enter(StageJudge, e.At)
				} else {
					enter(StageCoderRework, e.At)
				}
				round.open = false
			}
		case "review-autospawn-wait":
			if s, ok := waitKindStage[e.Arg]; ok {
				enter(s, e.At)
			}
		case "judge-blocked":
			if e.Arg == "stale" {
				enter(StageRefresh, e.At)
			}
		case "update":
			if e.Arg == "blocked" {
				enter(StageBlocked, e.At)
			}
		default:
			if e.Action == "refreshed" {
				w.Refreshes++
			}
			if s, ok := simpleStage[e.Action]; ok {
				enter(s, e.At)
			}
		}
	}
	raw = append(raw, Segment{Stage: stage, From: from, To: end})
	for _, s := range raw {
		w.Segments = append(w.Segments, overlayRed(s, red, end)...)
	}
	return w
}

// overlayRed splits a judge, hold or refresh segment at the red-main windows; the parts inside a window become
// red_main. An open window (zero To) lasts until now.
func overlayRed(s Segment, red []Interval, now time.Time) []Segment {
	if s.Stage != StageJudge && s.Stage != StageHold && s.Stage != StageRefresh {
		return []Segment{s}
	}
	var out []Segment
	cur := s.From
	for _, r := range red {
		to := r.To
		if to.IsZero() {
			to = now
		}
		lo, hi := maxTime(r.From, cur), minTime(to, s.To)
		if !lo.Before(hi) {
			continue
		}
		if cur.Before(lo) {
			out = append(out, Segment{Stage: s.Stage, From: cur, To: lo})
		}
		out = append(out, Segment{Stage: StageRedMain, From: lo, To: hi})
		cur = hi
	}
	if cur.Before(s.To) || len(out) == 0 {
		out = append(out, Segment{Stage: s.Stage, From: cur, To: s.To})
	}
	return out
}

// Hours sums the segment durations per stage.
func (w Walk) Hours() map[string]float64 {
	h := map[string]float64{}
	for _, s := range w.Segments {
		h[s.Stage] += s.To.Sub(s.From).Hours()
	}
	return h
}

// Current is the stage of the last segment.
func (w Walk) Current() string {
	if len(w.Segments) == 0 {
		return StageCIWait
	}
	return w.Segments[len(w.Segments)-1].Stage
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
