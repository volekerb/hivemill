package hm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Usage is the plan usage the old pipeline's gate reads (~/.claude/usage-limits.json, written by the status line).
type Usage struct {
	FiveHour, SevenDay *float64
	Found              bool // the file exists and parses
	AgeS               int  // seconds since updated_at; -1 unknown
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

// ReadUsage parses the usage file the status line writes (the old pipeline's source). Claude Code puts
// rate_limits.five_hour in the status line input only while the API reports a 5-hour window, so the field can be empty.
func ReadUsage(userHome string, now time.Time) Usage {
	u, _ := localUsage(userHome, now)
	return u
}

// localUsage is ReadUsage plus whether the file has an updated_at.
func localUsage(userHome string, now time.Time) (Usage, bool) {
	b, err := os.ReadFile(filepath.Join(userHome, ".claude", "usage-limits.json"))
	if err != nil {
		return Usage{AgeS: -1}, false
	}
	return parseUsage(b, now)
}

// parseUsage parses the content of one usage file; dated says whether it has an updated_at.
func parseUsage(b []byte, now time.Time) (u Usage, dated bool) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return Usage{AgeS: -1}, false
	}
	u = Usage{FiveHour: pctField(m["five_hour_pct"]), SevenDay: pctField(m["seven_day_pct"]), Found: true, AgeS: -1}
	ts := pctField(m["updated_at"])
	if ts != nil {
		u.AgeS = int(now.Sub(time.Unix(int64(*ts), 0)).Seconds())
	}
	return u, ts != nil
}

// remoteSkewS is how far ahead of now a remote reading's updated_at may be (clock skew between machines).
const remoteSkewS = 60

// newestUsage is the newest reading of the local file and the files of the session machines: a session placed on
// another machine refreshes only that machine's file.
func (r *Runner) newestUsage(now time.Time) Usage {
	best, localDated := localUsage(r.Env.UserHome, now)
	if localDated && best.AgeS < 0 && best.AgeS >= -remoteSkewS {
		best.AgeS = 0 // the clock stepped back a little: still the newest, not an undated file
	}
	for _, b := range r.remoteUsageFiles() {
		u, dated := parseUsage(b, now)
		if !dated || u.AgeS < -remoteSkewS {
			continue
		}
		u.AgeS = max(u.AgeS, 0)
		if !best.Found || best.AgeS < 0 || u.AgeS < best.AgeS {
			best = u
		}
	}
	return best
}

// MemAvailableMB is the free memory of this machine in MB (-1 when it cannot be read): MemAvailable of /proc/meminfo
// on Linux; on macOS (free + inactive + speculative) pages of vm_stat, and 0 while the memory pressure level is not
// normal (the old pipeline's rule), so the mem floors hold there too instead of reading -1 and being skipped.
var MemAvailableMB = func() int {
	if runtime.GOOS == "darwin" {
		return darwinHostMemMB()
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	return linuxMemMB(string(b))
}

// darwinHostMemMB fails closed like the old pipeline: a reading it cannot take is 0 MB, so the floor holds.
func darwinHostMemMB() int {
	lvl, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.memorystatus_vm_pressure_level").Output()
	if err != nil {
		return 0
	}
	vm, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return 0
	}
	return max(0, darwinMemMB("pressure="+strings.TrimSpace(string(lvl))+"\n"+string(vm)))
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
	Active          int  // runs holding a slot now
	Probe           bool // set by DispatchBlock: the usage reading is stale, but one probe run may refresh it
	MemMB, MemMin   int
	TodayUSD        map[string]float64 // per project, sessions started today (local time)
	WeekUSD         float64            // all projects, sessions started in the last 7 days
	DayCap, WeekCap float64
	HasDay, HasWeek bool
}

// ReadCaps gathers usage, memory and spend. Spend sums the session transcripts at API prices.
func (r *Runner) ReadCaps(now time.Time) Caps {
	c := Caps{Usage: r.newestUsage(now), MemMB: MemAvailableMB(), MemMin: cfgInt(r.Store, "mem_floor_mb"),
		TodayUSD: map[string]float64{}}
	c.DayCap, c.HasDay = cfgFloat(r.Store, "project_daily_budget_usd")
	c.WeekCap, c.HasWeek = cfgFloat(r.Store, "weekly_budget_usd")
	runs, _ := r.Store.Runs()
	project := map[string]string{}
	for _, x := range runs {
		project[x.Task] = x.Project
		if Active(x.State) {
			c.Active++
		}
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
func (c *Caps) DispatchBlock(s Store, project string) string {
	c.Probe = false
	if why := c.UsageUnknown(s); why != "" {
		if c.probeAllowed(s) {
			c.Probe = true // one run may start; it renders a status line and so refreshes the reading
		} else {
			return "usage unknown: " + why
		}
	}
	if f := c.Usage.FiveHour; f != nil && *f >= float64(cfgInt(s, "max_five_hour_pct")) {
		return fmt.Sprintf("usage: 5h at %.0f%% (cap %s%%)", *f, s.Cfg("max_five_hour_pct"))
	}
	if f := c.Usage.SevenDay; f != nil && *f >= float64(cfgInt(s, "max_seven_day_pct")) {
		return fmt.Sprintf("usage: 7d at %.0f%% (cap %s%%)", *f, s.Cfg("max_seven_day_pct"))
	}
	// with placement, Start applies the floor when the pick is local; a probe always runs here, so it is applied now
	if c.MemMB >= 0 && c.MemMB < c.MemMin && (!placementOn(s) || c.Probe) {
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
	if why := c.UsageUnknown(s); why != "" {
		usage += ", UNKNOWN: " + why + "; dispatch held"
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
	mem := "unknown"
	if c.MemMB >= 0 {
		mem = fmt.Sprintf("%.1f GB", float64(c.MemMB)/1024)
	}
	return fmt.Sprintf("%s, %s, %s, task cap $%s, mem %s free (floor %.1f GB)", usage, day, week, s.Cfg("task_budget_usd"),
		mem, float64(c.MemMin)/1024)
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
	r.fail(run, fmt.Sprintf("budget: sessions cost $%.2f, over task_budget_usd $%.2f", total, limit))
	return true
}

// UsageUnknown says why the usage reading cannot be trusted ("" = it can). The gate fails closed: no reading, a reading
// older than usage_max_age_s, an empty 7-day field, or an empty 5-hour field (unless usage_require_5h is off) holds
// new runs.
func (c Caps) UsageUnknown(s Store) string {
	u := c.Usage
	switch {
	case !u.Found:
		return "no usage reading (~/.claude/usage-limits.json is missing or unreadable)"
	case u.AgeS < 0:
		return "the usage reading has no updated_at"
	case u.AgeS > cfgInt(s, "usage_max_age_s"):
		return fmt.Sprintf("the usage reading is %ds old (usage_max_age_s %s)", u.AgeS, s.Cfg("usage_max_age_s"))
	case u.SevenDay == nil:
		return "the 7-day field is empty"
	case u.FiveHour == nil && s.Cfg("usage_require_5h") != "off":
		return "the 5-hour field is empty (the API reports no 5-hour window; usage_require_5h=off accepts that)"
	}
	return ""
}

// probeAllowed: the reading exists but is stale, no run is active, and its last values were under the caps. Then
// exactly one run may start (the effective max_concurrent is 1 until a fresh reading arrives), because only a running
// session refreshes the reading. A missing reading, an over-cap last reading or an empty required field stays closed.
func (c Caps) probeAllowed(s Store) bool {
	u := c.Usage
	if !u.Found || u.AgeS <= cfgInt(s, "usage_max_age_s") || c.Active > 0 || u.SevenDay == nil ||
		*u.SevenDay >= float64(cfgInt(s, "max_seven_day_pct")) {
		return false
	}
	if u.FiveHour == nil {
		return s.Cfg("usage_require_5h") == "off"
	}
	return *u.FiveHour < float64(cfgInt(s, "max_five_hour_pct"))
}

// UsageHopeless: dispatch is held on usage and waiting will not help: no reading has ever existed, or the last
// reading is over a cap.
func (c Caps) UsageHopeless(s Store) bool {
	u := c.Usage
	if !u.Found {
		return true
	}
	over := func(p *float64, key string) bool { return p != nil && *p >= float64(cfgInt(s, key)) }
	return over(u.SevenDay, "max_seven_day_pct") || over(u.FiveHour, "max_five_hour_pct")
}
