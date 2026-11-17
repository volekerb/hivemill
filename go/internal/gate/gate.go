// Package gate is the Go port of the spawn resource gate of bin/factory_cli.py (gate(), usage(), still_starting(),
// the /proc readers and the host-process scan). Decide is a pure function of a Snapshot, so tests need no real /proc,
// herdr or clock; the readers in proc.go build the parts of a Snapshot that come from the host.
package gate

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrUnparsable means a config value or a pinned number is one Python would read differently (non-ASCII digits,
// inf/nan, ...) or would crash on; the caller hands the command to Python.
var ErrUnparsable = errors.New("gate: value Python parses differently")

// StartupGraceS is STARTUP_GRACE_S: herdr may not list a freshly started agent yet.
const StartupGraceS = 180

// ReviewReserveBlocked is REVIEW_RESERVE_BLOCKED: roles that may not take the slots review_reserved_slots keeps.
var ReviewReserveBlocked = []string{"pm", "coder", "cleaner"}

// Config is the part of the config table gate() reads. The *Text fields are the raw config strings, which gate()
// prints in its refusal reasons exactly as stored.
type Config struct {
	MaxLiveAgents       int
	MaxTotalClaude      int
	MinMemAvailableMB   int
	TmpMaxPct           float64
	MaxFiveHourPct      float64
	MaxSevenDayPct      float64
	LoadRatio           float64
	MaxLiveLeads        int // _int_cfg: a bad value is 4
	ReservedWorkerSlots int // _int_cfg: a bad value is 10
	ReviewReservedSlots int // _int_cfg: a bad value is 2
	ReviewAutospawn     string
	LeadParking         string

	MinMemText, TmpMaxPctText, LoadRatioText, FiveHourText, SevenDayText string
}

// LoadConfig reads the gate keys through get (the config row, else the built-in default). A value the hard
// int()/float() calls of Python cannot read the same way returns ErrUnparsable (Python raises there).
func LoadConfig(get func(key string) (string, error)) (Config, error) {
	var c Config
	var err error
	text := func(key string) string {
		if err != nil {
			return ""
		}
		var v string
		v, err = get(key)
		return v
	}
	hardInt := func(key string) int {
		v := text(key)
		if err != nil {
			return 0
		}
		n, e := PyInt(v)
		if e != nil {
			err = fmt.Errorf("config %s %q: %w", key, v, e)
		}
		return n
	}
	hardFloat := func(key string) (float64, string) {
		v := text(key)
		if err != nil {
			return 0, v
		}
		f, e := PyFloat(v)
		if e != nil {
			err = fmt.Errorf("config %s %q: %w", key, v, e)
		}
		return f, v
	}
	softInt := func(key string, def int) int {
		v := text(key)
		if err != nil {
			return 0
		}
		n, e := PyInt(v)
		if e != nil {
			if hasNonASCII(v) || strings.Contains(v, "_") || intRE.MatchString(pyStrip(v)) {
				// "1_0" and integers beyond int64 are valid Python ints: not ours to default
				err = fmt.Errorf("config %s %q: %w", key, v, ErrUnparsable)
			}
			return def
		}
		return n
	}
	c.MaxLiveAgents = hardInt("max_live_agents")
	c.MaxTotalClaude = hardInt("max_total_claude")
	c.MinMemAvailableMB = hardInt("min_mem_available_mb")
	c.MinMemText = text("min_mem_available_mb")
	c.TmpMaxPct, c.TmpMaxPctText = hardFloat("tmp_max_pct")
	c.MaxFiveHourPct, c.FiveHourText = hardFloat("max_five_hour_pct")
	c.MaxSevenDayPct, c.SevenDayText = hardFloat("max_seven_day_pct")
	c.LoadRatio, c.LoadRatioText = hardFloat("gate_max_load_ratio")
	c.MaxLiveLeads = softInt("max_live_leads", 4)
	c.ReservedWorkerSlots = softInt("reserved_worker_slots", 10)
	c.ReviewReservedSlots = softInt("review_reserved_slots", 2)
	c.ReviewAutospawn = text("review_autospawn")
	c.LeadParking = text("lead_parking")
	if err != nil {
		return Config{}, fmt.Errorf("gate config: %w", err)
	}
	return c, nil
}

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 0x7f && !pyIsSpace(r) {
			return true
		}
	}
	return false
}

// Options are the arguments of gate() besides the connection.
type Options struct {
	OwnSlot, LoadExempt, OwnLead bool
	Role, Runtime                string
}

// Usage is usage(): the 5h/7d quota reading. A nil pointer is Python None.
type Usage struct {
	FiveHour, SevenDay *float64
	Fresh              bool
	AgeMin             int
	Unreadable         bool
	FiveResetsAt       *int64
	SevenResetsAt      *int64
}

// Text is usage_text(): the "claude usage ..." line of `factory res`.
func (u Usage) Text() string {
	if u.Unreadable {
		return "claude usage unreadable"
	}
	five, week := "n/a", "n/a"
	if u.FiveHour != nil {
		five = fmt.Sprintf("%.0f%%", *u.FiveHour)
	}
	if u.SevenDay != nil {
		week = fmt.Sprintf("%.0f%%", *u.SevenDay)
	}
	s := "claude usage 5h " + five + " · week " + week
	if !u.Fresh {
		s += fmt.Sprintf(" (stale: %d min old)", u.AgeMin)
	}
	return s
}

// Tmp is tmp_usage(): the filesystem holding /tmp.
type Tmp struct {
	UsedMB, TotalMB int64
	Pct             float64
	FSType          string // "" is None
	FreeMB          int64  // free space; read on darwin, where TmpMinFreeMB applies
	Mount           string // mount point; read on darwin only
}

// CursorLimit is a Cursor model that is limited until Reset (cursor_limits state entry with status "limited").
type CursorLimit struct{ Slug, Reset string }

// AgentRow is one status='live' agents row as gate() reads it.
type AgentRow struct {
	Name, PaneID, Started, SessionID string
	Runtime                          string // "claude" or "cursor"
	CursorWorkspace                  string // resolved policy workspace of a cursor row, "" when unknown
}

// RoleRow is one status IN ('live','starting') agents row: only its role matters.
type RoleRow struct{ Role string }

// Snapshot is everything Decide and Stats need; Collect-style code in the caller fills it. A nil *int is Python None.
type Snapshot struct {
	Now time.Time

	// Herdr is the `agent list` result; HerdrReachable=false is herdr_agents() returning None.
	HerdrReachable bool
	HerdrAgents    []HerdrAgent

	LiveRows    []AgentRow // status='live'
	HQWorkerRow []RoleRow  // status IN ('live','starting')
	ReviewHeld  int        // live/starting reviewer+qa rows

	MemMB int64
	Usage Usage
	// Procs is total_claude_procs(); nil Argv map with TotalKnown=false is "unreadable".
	Procs  ProcScan
	Tmp    *Tmp
	Load1  float64
	NumCPU int

	// Host-facts extras. The zero values are today's Linux behaviour; only ApplyHostFacts on a strict host sets them.
	MemPressure  int          // 0 not reported, 1 normal, 2 warn, 4 critical, PressureUnknown unreadable
	TmpMinFreeMB int64        // >0: the tmp free-space floor (non-tmpfs only)
	StrictProcs  bool         // an unknown process scan closes the gate instead of skipping the check
	StrictTmp    bool         // a nil Tmp closes the gate instead of skipping the check
	LimitedFor   *CursorLimit // the cursor_limits entry of Options.Role's model when its status is "limited"
}

// HerdrAgent is the part of a herdr agent row gate() reads.
type HerdrAgent struct{ Name, PaneID, Agent string }

// Stats is the stats dict gate() returns.
type Stats struct {
	Live, Other, Leads, LeadsCap, HQ, HQCap, Workers, Cap int
	MemMB                                                 int64
	Usage                                                 Usage
	Total                                                 *int
	TotalCap, Starting                                    int
	FactoryProcs                                          *int
	Tmp                                                   *Tmp
	Load1, LoadCap                                        float64
}

// Decision is the (ok, reason, stats) tuple of gate().
type Decision struct {
	OK     bool
	Reason string
	Stats  Stats
}

// StillStarting is still_starting(row): a started timestamp younger than STARTUP_GRACE_S. A value that is not an
// aware ISO timestamp (Python raises TypeError/ValueError) is false.
func StillStarting(started string, now time.Time) bool {
	t, ok := ParseISO(started)
	if !ok {
		return false
	}
	return now.Sub(t).Seconds() < StartupGraceS
}

// Compute fills Stats from the snapshot, exactly as the first half of gate().
func Compute(s Snapshot, c Config) Stats {
	names, panes := map[string]bool{}, map[string]bool{}
	for _, a := range s.HerdrAgents {
		if a.Name != "" {
			names[a.Name] = true
		}
		if a.PaneID != "" {
			panes[a.PaneID] = true
		}
	}
	live := 0
	known := map[string]bool{}
	for _, r := range s.LiveRows {
		listed := names[r.Name] || (r.PaneID != "" && panes[r.PaneID])
		known[r.Name] = true
		if r.PaneID != "" {
			known[r.PaneID] = true
		}
		if listed || StillStarting(r.Started, s.Now) {
			live++
		}
	}
	other := 0
	for _, a := range s.HerdrAgents {
		if (a.Agent == "claude" || a.Agent == "cursor") && !known[a.Name] && !known[a.PaneID] {
			other++
		}
	}
	sessions, workspaces := map[string]bool{}, map[string]bool{}
	starting := 0
	for _, r := range s.LiveRows {
		var hasProc bool
		if r.Runtime == "cursor" {
			hasProc = r.CursorWorkspace != "" && s.Procs.HasCursorProc(r.CursorWorkspace)
			if hasProc {
				workspaces[r.CursorWorkspace] = true
			}
		} else {
			hasProc = r.SessionID != "" && s.Procs.HasSession(r.SessionID)
			if hasProc {
				sessions[r.SessionID] = true
			}
		}
		if StillStarting(r.Started, s.Now) && !hasProc {
			starting++
		}
	}
	matched := s.Procs.Matching(sessions, workspaces)
	var leads, hq, workers int
	for _, r := range s.HQWorkerRow {
		switch r.Role {
		case "lead":
			leads++
			hq++
		case "boss", "director":
			hq++
		case "pm", "coder", "qa", "reviewer", "judge", "cleaner":
			workers++
		}
	}
	hqCap := c.MaxLiveAgents - c.ReservedWorkerSlots
	if hqCap < 3 {
		hqCap = 3 // boss + director + one lead: a small box still admits its first lead
	}
	st := Stats{Live: live, Other: other, Leads: leads, LeadsCap: c.MaxLiveLeads, HQ: hq, HQCap: hqCap, Workers: workers,
		Cap: c.MaxLiveAgents, MemMB: s.MemMB, Usage: s.Usage, TotalCap: c.MaxTotalClaude, Starting: starting, Tmp: s.Tmp,
		Load1: s.Load1, LoadCap: c.LoadRatio * float64(s.NumCPU)}
	if s.Procs.Known {
		t, f := s.Procs.Total+starting, matched+starting
		st.Total, st.FactoryProcs = &t, &f
	}
	return st
}

// Decide is gate(): (ok, reason, stats). It is the hard cap: every check below refuses with Python's reason text.
func Decide(s Snapshot, c Config, o Options) Decision {
	st := Compute(s, c)
	d := Decision{Stats: st}
	refuse := func(why string) Decision { d.Reason = why; return d }
	if !s.HerdrReachable {
		return refuse("herdr unreachable")
	}
	mine := 0
	if o.OwnSlot {
		mine = 1
	}
	if st.Live-mine >= st.Cap {
		return refuse(fmt.Sprintf("%d/%d agent panes live", st.Live, st.Cap))
	}
	if st.Total != nil && *st.Total-mine >= c.MaxTotalClaude {
		return refuse(fmt.Sprintf("%d/%d claude processes on host (incl. non-factory)", *st.Total, c.MaxTotalClaude))
	}
	if !s.Procs.Known && s.StrictProcs {
		return refuse("host processes unknown — gate closed")
	}
	if s.MemMB < int64(c.MinMemAvailableMB) {
		if s.MemMB < 0 { // unreadable on a strict host (ApplyHostFacts)
			return refuse(fmt.Sprintf("MemAvailable unknown < floor %sMB", c.MinMemText))
		}
		return refuse(fmt.Sprintf("MemAvailable %dMB < floor %sMB", s.MemMB, c.MinMemText))
	}
	if s.MemPressure != 0 && s.MemPressure != PressureNormal {
		return refuse(fmt.Sprintf("memory pressure %s (level %d) — min-mem gate closed", PressureName(s.MemPressure), s.MemPressure))
	}
	if st.Tmp == nil && s.StrictTmp && o.Role != "cleaner" {
		return refuse("/tmp usage unknown — gate closed")
	}
	if t := st.Tmp; t != nil && s.TmpMinFreeMB > 0 && t.FSType != "tmpfs" && t.FreeMB < s.TmpMinFreeMB && o.Role != "cleaner" {
		return refuse(fmt.Sprintf("/tmp on %s (%s) %.1f%% used, %d MB free < floor %d MB — clean finished tasks' scratchpads",
			t.Mount, t.FSType, t.Pct, t.FreeMB, s.TmpMinFreeMB))
	}
	if t := st.Tmp; t != nil && t.Pct >= c.TmpMaxPct {
		// a cleaner frees /tmp: holding it behind the tmp gate deadlocks the factory (T-0610). Only this check is skipped.
		if o.Role != "cleaner" {
			return refuse(fmt.Sprintf("/tmp %d/%d MB used (%.1f%%) ≥ %s%% — clean finished tasks' scratchpads",
				t.UsedMB, t.TotalMB, t.Pct, c.TmpMaxPctText))
		}
	}
	if !o.OwnSlot && c.ReviewAutospawn == "on" && inList(ReviewReserveBlocked, o.Role) {
		// G-022 #6: review_reserved_slots of max_live_agents are kept for reviewer/qa; slots they hold count toward them
		kept := c.ReviewReservedSlots - s.ReviewHeld
		if kept < 0 {
			kept = 0
		}
		if st.Live >= st.Cap-kept {
			return refuse(fmt.Sprintf("%d/%d agent panes live; %d slots kept for reviewer/qa (review_reserved_slots)", st.Live, st.Cap, kept))
		}
	}
	if !(o.OwnSlot || o.LoadExempt) && st.Load1 > st.LoadCap {
		load := fmt.Sprintf("%.1f", st.Load1)
		if math.IsInf(st.Load1, 1) { // unreadable on a strict host (ApplyHostFacts)
			load = "unknown"
		}
		return refuse(fmt.Sprintf("load1 %s > %s x %d CPUs (%.1f) — the tests or builds on the box are saturating the CPU",
			load, c.LoadRatioText, s.NumCPU, st.LoadCap))
	}
	// Claude 5h/7d caps measure Anthropic quota only; cursor-runtime agents do not consume it (G-027 SC4).
	if o.Runtime != "cursor" {
		u := s.Usage
		if u.Fresh && u.FiveHour != nil && *u.FiveHour >= c.MaxFiveHourPct {
			return refuse(fmt.Sprintf("5h usage %.0f%% ≥ %s%%", *u.FiveHour, c.FiveHourText))
		}
		if u.Fresh && u.SevenDay != nil && *u.SevenDay >= c.MaxSevenDayPct {
			return refuse(fmt.Sprintf("weekly usage %.0f%% ≥ %s%%", *u.SevenDay, c.SevenDayText))
		}
	}
	if o.Runtime == "cursor" && o.Role != "" && s.LimitedFor != nil {
		reset := s.LimitedFor.Reset
		if reset == "" {
			reset = "unknown date"
		}
		return refuse(fmt.Sprintf("Cursor pane limit: %s limited until %s", s.LimitedFor.Slug, reset))
	}
	if o.Role == "lead" && c.LeadParking == "1" && !(o.OwnSlot || o.OwnLead) {
		if st.Leads >= st.LeadsCap {
			return refuse(fmt.Sprintf("%d/%d leads live (max_live_leads)", st.Leads, st.LeadsCap))
		}
		if st.HQ+1 > st.HQCap {
			return refuse(fmt.Sprintf("%d/%d HQ panes; %d slots reserved for workers (reserved_worker_slots)",
				st.HQ, st.HQCap, c.ReservedWorkerSlots))
		}
	}
	d.OK, d.Reason = true, "ok"
	return d
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// pyIsSpace is str.isspace for one rune (Python's whitespace set).
func pyIsSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }
