package gate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func baseConfig() Config {
	return Config{MaxLiveAgents: 3, MaxTotalClaude: 10, MinMemAvailableMB: 500, TmpMaxPct: 90, MaxFiveHourPct: 90,
		MaxSevenDayPct: 95, LoadRatio: 1.5, MaxLiveLeads: 4, ReservedWorkerSlots: 10, ReviewReservedSlots: 2,
		ReviewAutospawn: "off", LeadParking: "0", MinMemText: "500", TmpMaxPctText: "90", LoadRatioText: "1.5",
		FiveHourText: "90", SevenDayText: "95"}
}

func f64(v float64) *float64 { return &v }

func liveRow(name string) AgentRow {
	return AgentRow{Name: name, PaneID: "p-" + name, Started: "2026-11-15T01:00:00+00:00", SessionID: "s-" + name, Runtime: "claude"}
}

// baseSnap is a snapshot every check passes: nothing live, plenty of memory, quiet CPU, fresh low usage.
func baseSnap() Snapshot {
	return Snapshot{Now: t0, HerdrReachable: true, MemMB: 3000, Usage: Usage{FiveHour: f64(10), SevenDay: f64(20), Fresh: true},
		Procs: ProcScan{Known: true, Argv: map[int][]string{}}, Load1: 0.5, NumCPU: 8}
}

func withAgents(s Snapshot, n int, roles ...string) Snapshot {
	for i := 0; i < n; i++ {
		name := "a" + string(rune('0'+i))
		s.LiveRows = append(s.LiveRows, liveRow(name))
		s.HerdrAgents = append(s.HerdrAgents, HerdrAgent{Name: name, PaneID: "p-" + name, Agent: "claude"})
	}
	for _, r := range roles {
		s.HQWorkerRow = append(s.HQWorkerRow, RoleRow{Role: r})
	}
	return s
}

// TestGateDecision: every refusal carries Python's reason text; an allowed case and the per-cap boundaries.
func TestGateDecision(t *testing.T) {
	tmpFull := &Tmp{UsedMB: 920, TotalMB: 1000, Pct: 92}
	cases := []struct {
		name   string
		snap   func() Snapshot
		cfg    func(*Config)
		opt    Options
		ok     bool
		reason string
	}{
		{"allowed under every cap", baseSnap, nil, Options{}, true, "ok"},
		{"herdr unreachable", func() Snapshot { s := baseSnap(); s.HerdrReachable = false; return s }, nil, Options{}, false, "herdr unreachable"},
		{"over the worker cap", func() Snapshot { return withAgents(baseSnap(), 3) }, nil, Options{}, false, "3/3 agent panes live"},
		{"one under the cap", func() Snapshot { return withAgents(baseSnap(), 2) }, nil, Options{}, true, "ok"},
		{"own slot discounts one pane", func() Snapshot { return withAgents(baseSnap(), 3) }, nil, Options{OwnSlot: true}, true, "ok"},
		{"host claude processes at cap", func() Snapshot {
			s := baseSnap()
			s.Procs = ProcScan{Known: true, Total: 10}
			return s
		}, nil, Options{}, false, "10/10 claude processes on host (incl. non-factory)"},
		{"host processes unknown never refuse", func() Snapshot { s := baseSnap(); s.Procs = ProcScan{}; return s }, nil, Options{}, true, "ok"},
		{"memory under the floor", func() Snapshot { s := baseSnap(); s.MemMB = 499; return s }, nil, Options{}, false, "MemAvailable 499MB < floor 500MB"},
		{"memory at the floor is allowed", func() Snapshot { s := baseSnap(); s.MemMB = 500; return s }, nil, Options{}, true, "ok"},
		{"tmp over its cap", func() Snapshot { s := baseSnap(); s.Tmp = tmpFull; return s }, nil, Options{}, false,
			"/tmp 920/1000 MB used (92.0%) ≥ 90% — clean finished tasks' scratchpads"},
		{"cleaner is exempt from the tmp gate", func() Snapshot { s := baseSnap(); s.Tmp = tmpFull; return s }, nil, Options{Role: "cleaner"}, true, "ok"},
		{"load over the cap", func() Snapshot { s := baseSnap(); s.Load1 = 12.5; return s }, nil, Options{}, false,
			"load1 12.5 > 1.5 x 8 CPUs (12.0) — the tests or builds on the box are saturating the CPU"},
		{"load exempt role passes", func() Snapshot { s := baseSnap(); s.Load1 = 12.5; return s }, nil, Options{LoadExempt: true}, true, "ok"},
		{"5h usage at the stop percent", func() Snapshot { s := baseSnap(); s.Usage.FiveHour = f64(90); return s }, nil, Options{}, false, "5h usage 90% ≥ 90%"},
		{"weekly usage at the stop percent", func() Snapshot { s := baseSnap(); s.Usage.SevenDay = f64(95); return s }, nil, Options{}, false, "weekly usage 95% ≥ 95%"},
		{"weekly usage just under", func() Snapshot { s := baseSnap(); s.Usage.SevenDay = f64(94.4); return s }, nil, Options{}, true, "ok"},
		{"stale usage never refuses", func() Snapshot { s := baseSnap(); s.Usage.Fresh = false; s.Usage.SevenDay = f64(99); return s }, nil, Options{}, true, "ok"},
		{"cursor runtime ignores the claude quota", func() Snapshot { s := baseSnap(); s.Usage.SevenDay = f64(99); return s }, nil, Options{Runtime: "cursor"}, true, "ok"},
		{"cursor model limited", func() Snapshot {
			s := baseSnap()
			s.LimitedFor = &CursorLimit{Slug: "grok-4", Reset: "2026-11-20"}
			return s
		}, nil, Options{Runtime: "cursor", Role: "coder"}, false, "Cursor pane limit: grok-4 limited until 2026-11-20"},
		{"cursor model limited, no reset known", func() Snapshot {
			s := baseSnap()
			s.LimitedFor = &CursorLimit{Slug: "grok-4"}
			return s
		}, nil, Options{Runtime: "cursor", Role: "coder"}, false, "Cursor pane limit: grok-4 limited until unknown date"},
		{"slots kept for reviewer/qa", func() Snapshot { return withAgents(baseSnap(), 2) }, func(c *Config) { c.MaxLiveAgents = 4; c.ReviewAutospawn = "on" },
			Options{Role: "coder"}, false, "2/4 agent panes live; 2 slots kept for reviewer/qa (review_reserved_slots)"},
		{"reviewer may take the kept slot", func() Snapshot { return withAgents(baseSnap(), 2) }, func(c *Config) { c.MaxLiveAgents = 4; c.ReviewAutospawn = "on" },
			Options{Role: "reviewer"}, true, "ok"},
		{"lead cap", func() Snapshot { return withAgents(baseSnap(), 1, "lead", "lead") }, func(c *Config) { c.LeadParking = "1"; c.MaxLiveLeads = 2; c.MaxLiveAgents = 30 },
			Options{Role: "lead"}, false, "2/2 leads live (max_live_leads)"},
		{"lead own slot skips the lead cap", func() Snapshot { return withAgents(baseSnap(), 1, "lead", "lead") }, func(c *Config) { c.LeadParking = "1"; c.MaxLiveLeads = 2; c.MaxLiveAgents = 30 },
			Options{Role: "lead", OwnLead: true}, true, "ok"},
		{"hq reserve for workers", func() Snapshot { return withAgents(baseSnap(), 1, "boss", "director", "lead") },
			func(c *Config) { c.LeadParking = "1"; c.MaxLiveAgents = 13; c.ReservedWorkerSlots = 10 },
			Options{Role: "lead"}, false, "3/3 HQ panes; 10 slots reserved for workers (reserved_worker_slots)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			if tc.cfg != nil {
				tc.cfg(&c)
			}
			d := Decide(tc.snap(), c, tc.opt)
			if d.OK != tc.ok || d.Reason != tc.reason {
				t.Fatalf("Decide = (%v, %q), want (%v, %q)", d.OK, d.Reason, tc.ok, tc.reason)
			}
		})
	}
}

// TestGateDecisionPerOrg: the org caps are the per-org gate config, so one org at its cap is refused and the other
// is not (each org's snapshot carries only its own live agents and caps).
func TestGateDecisionPerOrg(t *testing.T) {
	claude := withAgents(baseSnap(), 3)
	cursor := withAgents(baseSnap(), 1)
	c := baseConfig()
	if d := Decide(claude, c, Options{}); d.OK || d.Reason != "3/3 agent panes live" {
		t.Fatalf("org at its cap: %+v", d)
	}
	if d := Decide(cursor, c, Options{}); !d.OK {
		t.Fatalf("org under its cap must pass: %+v", d)
	}
}

// TestGateStartingAgentCounts: a row herdr does not list yet counts while it is inside the startup grace.
func TestGateStartingAgentCounts(t *testing.T) {
	s := baseSnap()
	young := liveRow("new")
	young.Started = t0.Add(-60 * time.Second).Format("2006-01-02T15:04:05-07:00")
	old := liveRow("gone")
	old.Started = t0.Add(-600 * time.Second).Format("2006-01-02T15:04:05-07:00")
	naive := liveRow("naive")
	naive.Started = "2026-11-15T11:59:30"
	s.LiveRows = []AgentRow{young, old, naive}
	st := Compute(s, baseConfig())
	if st.Live != 1 {
		t.Fatalf("live = %d, want 1 (only the young unlisted row)", st.Live)
	}
	if st.Starting != 1 {
		t.Fatalf("starting = %d, want 1", st.Starting)
	}
}

func TestLoadConfig(t *testing.T) {
	m := map[string]string{"max_live_agents": "20", "max_total_claude": "30", "min_mem_available_mb": " 800 ", "tmp_max_pct": "90",
		"max_five_hour_pct": "85", "max_seven_day_pct": "92.5", "gate_max_load_ratio": "1.5", "max_live_leads": "x",
		"reserved_worker_slots": "7", "review_reserved_slots": "", "review_autospawn": "on", "lead_parking": "1"}
	get := func(k string) (string, error) { return m[k], nil }
	c, err := LoadConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxLiveLeads != 4 || c.ReviewReservedSlots != 2 || c.ReservedWorkerSlots != 7 || c.MinMemAvailableMB != 800 || c.MaxSevenDayPct != 92.5 {
		t.Fatalf("config = %+v", c)
	}
	m["max_live_agents"] = "٣" // Python int() reads ARABIC-INDIC DIGIT THREE as 3; Go must not guess
	if _, err := LoadConfig(get); !errors.Is(err, ErrUnparsable) {
		t.Fatalf("non-ASCII digit: err = %v, want ErrUnparsable", err)
	}
}

// TestUnicodeWhitespaceAndDigits: Python's int()/float() strip NBSP and \v; non-ASCII digits are Python's too and
// are handed back to it (ErrUnparsable). Python: float("\xa01.5\x0b") == 1.5, int("٣") == 3.
func TestUnicodeWhitespaceAndDigits(t *testing.T) {
	if f, err := PyFloat(" 1.5\v"); err != nil || f != 1.5 {
		t.Fatalf("PyFloat(NBSP 1.5 VT) = %v, %v", f, err)
	}
	if n, err := PyInt("\v42 "); err != nil || n != 42 {
		t.Fatalf("PyInt = %v, %v", n, err)
	}
	if _, err := PyInt("٣"); !errors.Is(err, ErrUnparsable) {
		t.Fatalf("PyInt(U+0663) err = %v", err)
	}
	if _, err := PyFloat("1٣"); !errors.Is(err, ErrUnparsable) {
		t.Fatalf("PyFloat(1 U+0663) err = %v", err)
	}
	if _, err := PyFloat("nan"); !errors.Is(err, ErrUnparsable) {
		t.Fatalf("PyFloat(nan) err = %v", err)
	}
}

// TestProcSnapshot: fixture meminfo/loadavg/swaps/usage files give the numbers Python computes from the same files.
// Python one-off (see the PR body): MemAvailable 3141592 kB // 1024 = 3067; (SwapTotal-SwapFree) // 1024 = 512;
// float("0.52") = 0.52.
func TestProcSnapshot(t *testing.T) {
	mem, err := ReadFile("testdata/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := MemAvailableMB(mem); err != nil || got != 3067 {
		t.Fatalf("MemAvailableMB = %d, %v; Python 3067", got, err)
	}
	if got, err := SwapUsedMB(mem); err != nil || got != 512 {
		t.Fatalf("SwapUsedMB = %d, %v; Python 512", got, err)
	}
	if got, err := MemAvailableMB("MemTotal: 1 kB\n"); err != nil || got != 0 {
		t.Fatalf("no MemAvailable line = %d, %v; Python 0", got, err)
	}
	la, _ := ReadFile("testdata/loadavg")
	if got, err := Load1(la); err != nil || got != 0.52 {
		t.Fatalf("Load1 = %v, %v; Python 0.52", got, err)
	}
	swaps, _ := ReadFile("testdata/swaps")
	if !strings.Contains(swaps, "/swapfile") { // Python's res reads swap from meminfo; the fixture only documents the file
		t.Fatal("swaps fixture missing")
	}
	data, _ := os.ReadFile("testdata/usage.json")
	u, ok, amb := ParseUsage(data, time.Unix(1000000+7260, 0))
	if !ok || amb || *u.FiveHour != 41.5 || *u.SevenDay != 93 || u.Fresh != true || u.AgeMin != 121 || *u.FiveResetsAt != 1000600 || u.SevenResetsAt != nil {
		t.Fatalf("usage = %+v ok=%v", u, ok)
	}
	if got := u.Text(); got != "claude usage 5h 42% · week 93%" { // Python: f"{41.5:.0f}" == "42"
		t.Fatalf("Text = %q", got)
	}
	stale, _, _ := ParseUsage(data, time.Unix(1000000+4*3600, 0))
	usageZone = time.UTC
	defer func() { usageZone = nil }()
	// updated_at 1000000 = 1970-01-12 13:46:40 UTC; an old reading never shows its percentages (T-1822)
	if stale.Fresh || stale.Text() != "claude usage stale since 1970-01-12 13:46 UTC" {
		t.Fatalf("stale = %+v %q", stale, stale.Text())
	}
}

func TestTmpUsage(t *testing.T) {
	dir := t.TempDir()
	rd := ResolvePath(dir)
	mounts := filepath.Join(t.TempDir(), "mounts")
	body := "rootfs / rootfs rw 0 0\ntmpfs " + rd + " tmpfs rw 0 0\next4 " + filepath.Dir(rd) + " ext4 rw 0 0\n"
	if err := os.WriteFile(mounts, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := TmpUsage(dir, "1073741824,268435456", mounts)
	if got == nil || got.UsedMB != 768 || got.TotalMB != 1024 || got.Pct != 75.0 || got.FSType != "tmpfs" {
		t.Fatalf("TmpUsage = %+v; Python used 768 total 1024 pct 75.0 tmpfs", got)
	}
	if TmpUsage(dir, "0,0", mounts) != nil || TmpUsage(dir, "1", mounts) != nil || TmpUsage(dir, "a,b", mounts) != nil {
		t.Fatal("zero total or a malformed pin must be None")
	}
	if real := TmpUsage(dir, "", mounts); real == nil || real.TotalMB <= 0 {
		t.Fatalf("real statfs = %+v", real)
	}
}

func TestReadUsage(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Unix(2000, 0) }
	var slept int
	sleep := func(time.Duration) { slept++ }
	u, _ := ReadUsage(filepath.Join(dir, "missing.json"), now, sleep)
	if u.AgeMin != -1 || u.Fresh || u.FiveHour != nil || u.SevenDay != nil || slept != 0 || u.Text() != "claude usage stale since never (no usage file)" {
		t.Fatalf("missing file: %+v %q", u, u.Text())
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"five_hour_pct": `), 0o600)
	u, _ = ReadUsage(bad, now, sleep)
	if !u.Unreadable || slept != 1 || u.Text() != "claude usage unreadable" {
		t.Fatalf("bad file: %+v slept=%d", u, slept)
	}
	nan := filepath.Join(dir, "nan.json")
	_ = os.WriteFile(nan, []byte(`{"updated_at": NaN, "five_hour_pct": 1}`), 0o600)
	if _, amb := ReadUsage(nan, now, sleep); !amb {
		t.Fatal("a NaN literal is Python's to read: ambiguous")
	}
	one := filepath.Join(dir, "one.json")
	_ = os.WriteFile(one, []byte(`{"updated_at": 1999, "five_hour_pct": "", "seven_day_pct": "7"}`), 0o600)
	u, _ = ReadUsage(one, now, sleep)
	if u.FiveHour != nil || *u.SevenDay != 7 || !u.Fresh {
		t.Fatalf("one good field: %+v", u)
	}
}

func TestScanProcs(t *testing.T) {
	root := t.TempDir()
	mk := func(pid, ppid string, argv ...string) {
		d := filepath.Join(root, pid)
		_ = os.MkdirAll(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600)
		_ = os.WriteFile(filepath.Join(d, "stat"), []byte(pid+" (claude code) S "+ppid+" 1 1 0\n"), 0o600)
	}
	mk("100", "1", "/usr/bin/claude", "--session-id", "s-a")
	mk("101", "100", "/usr/bin/claude", "--resume", "s-child") // subagent of 100: not top level
	mk("200", "1", "node", "/x/claude", "--resume=s-b")
	mk("300", "1", "/usr/bin/vim", "claude.txt")
	mk("400", "1", "cursor-agent", "--workspace", root)
	_ = os.MkdirAll(filepath.Join(root, "self"), 0o700)
	p := ScanProcs(root)
	if !p.Known || p.Total != 3 || len(p.PIDs) != 3 || p.PIDs[0] != 100 {
		t.Fatalf("scan = %+v", p)
	}
	if !p.HasSession("s-a") || !p.HasSession("s-b") || p.HasSession("s-child") || p.HasSession("nope") {
		t.Fatal("HasSession wrong")
	}
	if !p.HasCursorProc(root) || p.HasCursorProc(filepath.Join(root, "other")) {
		t.Fatal("HasCursorProc wrong")
	}
	if n := p.Matching(map[string]bool{"s-a": true}, map[string]bool{ResolvePath(root): true}); n != 2 {
		t.Fatalf("Matching = %d, want 2", n)
	}
	if ScanProcs(filepath.Join(root, "nope")).Known {
		t.Fatal("unreadable proc dir must be unknown")
	}
}

func TestParseISO(t *testing.T) {
	for _, s := range []string{"2026-11-15T01:02:03+01:00", "2026-11-15 01:02:03+00:00", "2026-11-15T01:02:03Z", "2026-11-15T01:02:03.250000+01:00"} {
		if _, ok := ParseISO(s); !ok {
			t.Fatalf("ParseISO(%q) failed", s)
		}
	}
	for _, s := range []string{"", "2026-11-15T01:02:03", "garbage"} {
		if _, ok := ParseISO(s); ok {
			t.Fatalf("ParseISO(%q) must be false (naive or bad: Python raises)", s)
		}
	}
}

func TestCPUCount(t *testing.T) {
	p := filepath.Join(t.TempDir(), "online")
	for body, want := range map[string]int{"0-7\n": 8, "0-3,5\n": 5, "0\n": 1, "x\n": 3, "": 3} {
		_ = os.WriteFile(p, []byte(body), 0o600)
		if got := CPUCount(p, 3); got != want {
			t.Fatalf("CPUCount(%q) = %d, want %d", body, got, want)
		}
	}
	if CPUCount(filepath.Join(t.TempDir(), "none"), 3) != 3 {
		t.Fatal("missing file must fall back")
	}
}
