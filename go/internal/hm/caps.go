package hm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Usage is the plan usage the old pipeline's gate reads (~/.claude/usage-limits.json, written by the status line).
type Usage struct {
	FiveHour, SevenDay *float64
	Fresh              bool // updated in the last 3 hours
	AgeMin             int
}

func pctField(v any) *float64 {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return nil
		}
	default:
		return nil
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

// ReadUsage parses the usage file; a missing or unreadable file is not fresh (no data is not 0%).
func ReadUsage(userHome string, now time.Time) Usage {
	b, err := os.ReadFile(filepath.Join(userHome, ".claude", "usage-limits.json"))
	if err != nil {
		return Usage{AgeMin: -1}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return Usage{AgeMin: -1}
	}
	u := Usage{FiveHour: pctField(m["five_hour_pct"]), SevenDay: pctField(m["seven_day_pct"]), AgeMin: -1}
	if ts := pctField(m["updated_at"]); ts != nil {
		age := now.Sub(time.Unix(int64(*ts), 0))
		u.AgeMin, u.Fresh = int(age.Minutes()), age < 3*time.Hour
	}
	return u
}

// MemAvailableMB reads MemAvailable from /proc/meminfo (-1 when it cannot).
func MemAvailableMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) >= 2 && fs[0] == "MemAvailable:" {
			kb, _ := strconv.Atoi(fs[1])
			return kb / 1024
		}
	}
	return -1
}

func cfgFloat(s Store, key string) (float64, bool) {
	v := strings.TrimSpace(s.Cfg(key))
	if v == "" || v == "off" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil && f > 0
}

// Caps is the state of every dispatch cap at one moment.
type Caps struct {
	Usage           Usage
	MemMB, MemMin   int
	TodayUSD        map[string]float64 // per project, sessions started today (local time)
	WeekUSD         float64            // all projects, sessions started in the last 7 days
	DayCap, WeekCap float64
	HasDay, HasWeek bool
}

// ReadCaps gathers usage, memory and spend. Spend sums the session transcripts at API prices.
func (r *Runner) ReadCaps(now time.Time) Caps {
	c := Caps{Usage: ReadUsage(r.Env.UserHome, now), MemMB: MemAvailableMB(), MemMin: cfgInt(r.Store, "mem_floor_mb"),
		TodayUSD: map[string]float64{}}
	c.DayCap, c.HasDay = cfgFloat(r.Store, "project_daily_budget_usd")
	c.WeekCap, c.HasWeek = cfgFloat(r.Store, "weekly_budget_usd")
	runs, _ := r.Store.Runs()
	project := map[string]string{}
	for _, x := range runs {
		project[x.Task] = x.Project
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	week := now.Add(-7 * 24 * time.Hour)
	var ids []string
	type ss struct {
		task string
		at   time.Time
	}
	sess := map[string]ss{}
	for _, x := range runs {
		list, _ := r.Store.Sessions(x.Task)
		for _, s := range list {
			at, err := time.Parse(time.RFC3339, s.Started)
			if err != nil || at.Before(week) {
				continue
			}
			ids = append(ids, s.SID)
			sess[s.SID] = ss{x.Task, at}
		}
	}
	if len(ids) == 0 {
		return c
	}
	for sid, usd := range SessionCosts(r.Env.UserHome, ids) {
		c.WeekUSD += usd
		if s := sess[sid]; !s.at.Before(day) {
			c.TodayUSD[project[s.task]] += usd
		}
	}
	return c
}

// DispatchBlock says why no new run may start now ("" = it may). Active runs are never stopped by these caps.
func (c Caps) DispatchBlock(s Store, project string) string {
	if c.Usage.Fresh {
		if f := c.Usage.FiveHour; f != nil && *f >= float64(cfgInt(s, "max_five_hour_pct")) {
			return fmt.Sprintf("usage: 5h at %.0f%% (cap %s%%)", *f, s.Cfg("max_five_hour_pct"))
		}
		if f := c.Usage.SevenDay; f != nil && *f >= float64(cfgInt(s, "max_seven_day_pct")) {
			return fmt.Sprintf("usage: 7d at %.0f%% (cap %s%%)", *f, s.Cfg("max_seven_day_pct"))
		}
	}
	if c.MemMB >= 0 && c.MemMB < c.MemMin {
		return fmt.Sprintf("memory: %d MB available, floor %d MB", c.MemMB, c.MemMin)
	}
	if c.HasWeek && c.WeekUSD >= c.WeekCap {
		return fmt.Sprintf("budget: $%.2f in 7 days reached weekly_budget_usd $%.2f", c.WeekUSD, c.WeekCap)
	}
	if c.HasDay && project != "" && c.TodayUSD[project] >= c.DayCap {
		return fmt.Sprintf("budget: project %s spent $%.2f today, project_daily_budget_usd $%.2f", project, c.TodayUSD[project], c.DayCap)
	}
	return ""
}

// Header is the one-line cap summary of `hivemill report`.
func (c Caps) Header(s Store) string {
	pct := func(p *float64) string {
		if p == nil {
			return "n/a"
		}
		return fmt.Sprintf("%.0f%%", *p)
	}
	usage := fmt.Sprintf("usage 5h %s / 7d %s (caps %s%%/%s%%", pct(c.Usage.FiveHour), pct(c.Usage.SevenDay), s.Cfg("max_five_hour_pct"),
		s.Cfg("max_seven_day_pct"))
	if !c.Usage.Fresh {
		usage += ", reading stale"
	}
	usage += ")"
	today := 0.0
	for _, v := range c.TodayUSD {
		today += v
	}
	day := fmt.Sprintf("today $%.2f", today)
	if c.HasDay {
		day += fmt.Sprintf(" (cap $%.2f per project)", c.DayCap)
	}
	week := fmt.Sprintf("7d $%.2f", c.WeekUSD)
	if c.HasWeek {
		week += fmt.Sprintf(" of $%.2f", c.WeekCap)
	}
	return fmt.Sprintf("%s, %s, %s, task cap $%s, mem %.1f GB free (floor %.1f GB)", usage, day, week, s.Cfg("task_budget_usd"),
		float64(c.MemMB)/1024, float64(c.MemMin)/1024)
}

// overBudget stops a run whose sessions cost more than task_budget_usd, marking it FAIL with "budget".
func (r *Runner) overBudget(run *Run) bool {
	limit, ok := cfgFloat(r.Store, "task_budget_usd")
	if !ok {
		return false
	}
	sess, _ := r.Store.Sessions(run.Task)
	ids := make([]string, 0, len(sess))
	for _, s := range sess {
		if s.Started < run.Dispatched {
			continue // an earlier, cancelled attempt does not count against this run
		}
		ids = append(ids, s.SID)
	}
	total := 0.0
	for _, v := range SessionCosts(r.Env.UserHome, ids) {
		total += v
	}
	if total <= limit {
		return false
	}
	r.Close(run.Agent)
	for _, j := range run.Judges {
		r.Close(j)
	}
	r.fail(run, fmt.Sprintf("budget: sessions cost $%.2f, over task_budget_usd $%.2f", total, limit))
	return true
}
