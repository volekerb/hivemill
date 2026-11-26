package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Digest is the morning summary of one day (`hivemill digest`): merges and $, failed and held runs, open owner
// questions and goal progress.
type Digest struct {
	Date       string             `json:"date"`
	Merges     []DigestMerge      `json:"merges"`
	USD        float64            `json:"usd"` // every session started that day, at API prices (failed runs and the planner included)
	USDByRole  map[string]float64 `json:"usd_by_role"`
	Failed     []DigestRun        `json:"failed"`
	Held       []DigestRun        `json:"held"` // runs held for the owner now
	Asks       []Ask              `json:"asks"` // open owner questions
	Goals      []DigestGoal       `json:"goals"`
	Dispatched int                `json:"dispatched"`
	GHCalls    int                `json:"gh_calls"`      // gh calls of the daemon's passes that day
	GHPeak     int                `json:"gh_calls_peak"` // the most in one hour
	GHHours    int                `json:"gh_hours"`      // hours with at least one call
}

type DigestMerge struct {
	Task, PR string
	USD      float64 // all sessions of the task, any day
}

type DigestRun struct {
	Task, Why, Since string
}

type DigestGoal struct {
	ID, Project, Title                 string
	Done, Total, Ready, Running, Other int
}

// BuildDigest summarises the local calendar day of `day`.
func (r *Runner) BuildDigest(day time.Time) (*Digest, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	to := from.AddDate(0, 0, 1)
	d := &Digest{Date: from.Format("2006-01-02"), USDByRole: map[string]float64{}}
	evs, err := r.Log.Read()
	if err != nil {
		return nil, err
	}
	in := func(ts string) bool {
		at, err := time.Parse(time.RFC3339, ts)
		return err == nil && !at.Before(from) && at.Before(to)
	}
	for at := from; at.Before(to); at = at.Add(time.Hour) {
		var n int
		if _, err := fmt.Sscan(r.Store.Meta(ghKey(at)), &n); err != nil || n == 0 {
			continue
		}
		d.GHCalls, d.GHPeak, d.GHHours = d.GHCalls+n, max(d.GHPeak, n), d.GHHours+1
	}
	var mergedTasks []string
	for _, e := range evs {
		if !in(e.TS) {
			continue
		}
		switch e.Event {
		case "pr.merged":
			pr, _ := e.Data["pr"].(string)
			d.Merges = append(d.Merges, DigestMerge{Task: e.TaskID, PR: pr})
			mergedTasks = append(mergedTasks, e.TaskID)
		case "task.failed":
			why, _ := e.Data["reason"].(string)
			d.Failed = append(d.Failed, DigestRun{Task: e.TaskID, Why: truncate(why, 200), Since: e.TS})
		case "task.dispatched":
			d.Dispatched++
		}
	}
	// $: the sessions started that day, and every session of a task merged that day
	day0, err := s.SessionsBetween(from, to)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, x := range day0 {
		ids[x.SID] = true
	}
	perTask := map[string][]string{}
	for _, t := range mergedTasks {
		list, _ := r.Store.Sessions(t)
		for _, x := range list {
			ids[x.SID] = true
			perTask[t] = append(perTask[t], x.SID)
		}
	}
	all := make([]string, 0, len(ids))
	for id := range ids {
		all = append(all, id)
	}
	costs := SessionCosts(r.Env.UserHome, all)
	for _, x := range day0 {
		d.USD += costs[x.SID]
		d.USDByRole[x.Role] += costs[x.SID]
	}
	d.USD = round2(d.USD)
	for k, v := range d.USDByRole {
		d.USDByRole[k] = round2(v)
	}
	for i := range d.Merges {
		for _, sid := range perTask[d.Merges[i].Task] {
			d.Merges[i].USD += costs[sid]
		}
		d.Merges[i].USD = round2(d.Merges[i].USD)
	}
	runs, _ := r.Store.Runs()
	running := map[string]int{}
	for i := range runs {
		x := &runs[i]
		if heldForOwner(x) && !x.Eval {
			d.Held = append(d.Held, DigestRun{Task: x.Task, Why: truncate(or(x.MergeHold, x.FailReason), 200), Since: x.Ended})
		}
		if Active(x.State) {
			running[x.Goal]++
		}
	}
	d.Asks, _ = s.Asks("open")
	goals, err := s.Goals("active")
	if err != nil {
		return nil, err
	}
	readyBy := map[string]int{}
	seen := map[string]bool{}
	for _, g := range goals {
		if g.Project == "" || seen[g.Project] {
			continue
		}
		seen[g.Project] = true
		ready, _ := r.ReadyIn([]string{g.Project})
		for _, t := range ready {
			readyBy[t.Goal]++
		}
	}
	for _, g := range goals {
		ts, _ := s.Tasks(g.ID, "")
		dg := DigestGoal{ID: g.ID, Project: g.Project, Title: g.Title, Ready: readyBy[g.ID], Running: running[g.ID]}
		for _, t := range ts {
			if t.Status == "cancelled" {
				continue
			}
			dg.Total++
			if t.Status == "done" {
				dg.Done++
			}
		}
		dg.Other = dg.Total - dg.Done - dg.Ready - dg.Running
		if dg.Other < 0 {
			dg.Other = 0
		}
		d.Goals = append(d.Goals, dg)
	}
	return d, nil
}

// Summary is the digest in one line (the owner alert feed row).
func (d *Digest) Summary() string {
	return fmt.Sprintf("hivemill %s: %d merged, $%.2f, %d failed, %d held, %d open questions", d.Date, len(d.Merges), d.USD,
		len(d.Failed), len(d.Held), len(d.Asks))
}

// Markdown is the digest as the owner reads it.
func (d *Digest) Markdown(now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# hivemill digest %s\n\n%s\n\n## Merged (%d)\n", d.Date, d.Summary(), len(d.Merges))
	for _, m := range d.Merges {
		fmt.Fprintf(&b, "- %s %s ($%.2f)\n", m.Task, or(m.PR, "-"), m.USD)
	}
	roles := make([]string, 0, len(d.USDByRole))
	for k := range d.USDByRole {
		roles = append(roles, k)
	}
	sort.Strings(roles)
	var parts []string
	for _, k := range roles {
		parts = append(parts, fmt.Sprintf("%s $%.2f", k, d.USDByRole[k]))
	}
	fmt.Fprintf(&b, "\n## Spend\n$%.2f in sessions started that day (%s); %d runs dispatched\n", d.USD, or(strings.Join(parts, ", "), "none"), d.Dispatched)
	fmt.Fprintf(&b, "\n## gh API use\n%d calls; %d per hour on average over %d active hours, %d in the busiest hour\n",
		d.GHCalls, d.GHCalls/max(1, d.GHHours), d.GHHours, d.GHPeak)
	fmt.Fprintf(&b, "\n## Failed (%d)\n", len(d.Failed))
	for _, f := range d.Failed {
		fmt.Fprintf(&b, "- %s: %s\n", f.Task, f.Why)
	}
	fmt.Fprintf(&b, "\n## Held for the owner (%d)\n", len(d.Held))
	for _, h := range d.Held {
		fmt.Fprintf(&b, "- %s: %s\n", h.Task, h.Why)
	}
	fmt.Fprintf(&b, "\n## Open owner questions (%d)\n", len(d.Asks))
	for _, a := range d.Asks {
		fmt.Fprintf(&b, "- %s\n", AskLine(a, now))
	}
	fmt.Fprintf(&b, "\n## Goals (%d active)\n", len(d.Goals))
	for _, g := range d.Goals {
		fmt.Fprintf(&b, "- %s %s: %d/%d done, %d running, %d ready, %d other; %s\n", g.ID, g.Project, g.Done, g.Total, g.Running, g.Ready, g.Other, g.Title)
	}
	return b.String()
}

// SendDigest writes the digest to state/hivemill/digests/<date>.md and opens one owner alert feed row that points at it.
func (r *Runner) SendDigest(d *Digest, now time.Time) (string, error) {
	s, err := r.sq()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.Env.StateDir(), "digests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, d.Date+".md")
	if err := writeFile(p, d.Markdown(now), 0o600); err != nil {
		return "", err
	}
	opened, err := s.ReplaceAlert("hivemill-digest", d.Summary(), p)
	if err != nil {
		return p, err
	}
	if opened {
		r.ownerAlert("hivemill-digest", d.Summary()+" ("+p+")")
	}
	_ = r.Log.Emit("digest.sent", nil, Event{Data: map[string]any{"date": d.Date, "path": p}})
	return p, nil
}
