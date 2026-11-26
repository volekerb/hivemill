package hm

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestUsageAndDispatchCaps(t *testing.T) {
	r, s := testRunner(t, "")
	now := time.Unix(1795548325, 0)
	dir := filepath.Join(r.Env.UserHome, ".claude")
	write := func(body string) {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(body), 0o600)
	}
	block := func() string {
		c := Caps{Usage: ReadUsage(r.Env.UserHome, now), MemMB: 4096, MemMin: 1024, Active: 1} // a run is active: no probe
		return c.DispatchBlock(s, "www")
	}
	cases := []struct {
		name, body, want string
	}{
		{"missing reading", "", "no usage reading"},
		{"stale reading (11 min)", `{"five_hour_pct": "10", "seven_day_pct": "12", "updated_at": 1795547665}`, "660s old"},
		{"empty 5h field", `{"five_hour_pct": "", "seven_day_pct": "12", "updated_at": 1795548300}`, "5-hour field is empty"},
		{"5h over the threshold", `{"five_hour_pct": "95", "seven_day_pct": "12", "updated_at": 1795548300}`, "5h at 95%"},
		{"7d over the threshold", `{"five_hour_pct": "10", "seven_day_pct": "96", "updated_at": 1795548300}`, "7d at 96%"},
		{"OK", `{"five_hour_pct": "10", "seven_day_pct": "12", "updated_at": 1795548300}`, ""},
	}
	for _, c := range cases {
		_ = os.Remove(filepath.Join(dir, "usage-limits.json"))
		if c.body != "" {
			write(c.body)
		}
		got := block()
		if (c.want == "" && got != "") || (c.want != "" && !strings.Contains(got, c.want)) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	write(`{"five_hour_pct": "", "seven_day_pct": "12", "updated_at": 1795548300}`)
	_ = s.SetCfg("usage_require_5h", "off")
	if got := block(); got != "" {
		t.Errorf("usage_require_5h=off accepts an empty 5h field: %q", got)
	}
	write(`{"five_hour_pct": "10", "seven_day_pct": "12", "updated_at": 1795548300}`)
	c := Caps{Usage: ReadUsage(r.Env.UserHome, now), MemMB: 500, MemMin: 1024}
	if why := c.DispatchBlock(s, "www"); !strings.Contains(why, "memory") {
		t.Errorf("memory floor: %q", why)
	}
	c.MemMB = 4096
	c.TodayUSD, c.DayCap, c.HasDay = map[string]float64{"www": 21}, 20, true
	if why := c.DispatchBlock(s, "www"); !strings.Contains(why, "project www spent $21.00") {
		t.Errorf("daily budget: %q", why)
	}
	if why := c.DispatchBlock(s, "factory"); why != "" {
		t.Errorf("another project is not held by www's budget: %q", why)
	}
	c.WeekUSD, c.WeekCap, c.HasWeek = 60, 50, true
	if why := c.DispatchBlock(s, "factory"); !strings.Contains(why, "weekly_budget_usd") {
		t.Errorf("weekly budget: %q", why)
	}
	c.Usage = Usage{}
	if h := c.Header(s); !strings.Contains(h, "UNKNOWN: no usage reading") {
		t.Errorf("header %q", h)
	}
}

// The idle deadlock: a stale reading holds dispatch, and only a running session refreshes it.
func TestUsageProbe(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("usage_require_5h", "off")
	_, _ = s.db.Exec(`CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL, kind TEXT NOT NULL,
		ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, closed TEXT, sent TEXT)`)
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	usage := filepath.Join(dir, "usage-limits.json")
	stale := time.Now().Add(-time.Hour).Unix()
	write := func(sevenDay string) {
		_ = os.WriteFile(usage, []byte(`{"five_hour_pct": "", "seven_day_pct": "`+sevenDay+`", "updated_at": `+strconv.FormatInt(stale, 10)+`}`), 0o600)
	}
	alerts := func() int {
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-usage'`).Scan(&n)
		return n
	}
	probes := func() int {
		evs, _ := r.Log.Read()
		n := 0
		for _, e := range evs {
			if e.Event == "usage.probe" {
				n++
			}
		}
		return n
	}

	// idle and stale, last reading under the cap: the gate opens for exactly one run
	write("12")
	c := r.ReadCaps(time.Now())
	if why := c.DispatchBlock(s, "www"); why != "" || !c.Probe {
		t.Fatalf("idle + stale + under cap: %q probe %v", why, c.Probe)
	}
	// a probe run in flight: no second dispatch until a fresh reading arrives
	_ = s.PutRun(&Run{Task: "T-0009", State: Working})
	c = r.ReadCaps(time.Now())
	if why := c.DispatchBlock(s, "www"); !strings.Contains(why, "usage unknown") {
		t.Errorf("second dispatch during the probe: %q", why)
	}
	if alerts() != 0 {
		t.Error("a probe in flight is not an alert")
	}
	_ = s.PutRun(&Run{Task: "T-0009", State: Merged})
	// the fresh reading is over the cap: closed as normal
	_ = os.WriteFile(usage, []byte(`{"five_hour_pct": "", "seven_day_pct": "97", "updated_at": `+strconv.FormatInt(time.Now().Unix(), 10)+`}`), 0o600)
	c = r.ReadCaps(time.Now())
	if why := c.DispatchBlock(s, "www"); !strings.Contains(why, "7d at 97%") {
		t.Errorf("fresh over cap: %q", why)
	}
	// idle and stale, last reading over the cap: none, and the owner is told once
	write("97")
	_, _ = s.db.Exec(`UPDATE tasks SET status='todo'`)
	for i := 0; i < 2; i++ {
		if err := r.Start("T-0001"); err == nil || !strings.Contains(err.Error(), "usage unknown") {
			t.Fatalf("stale over cap must not dispatch: %v", err)
		}
	}
	if alerts() != 1 {
		t.Errorf("over cap: %d alerts, want 1", alerts())
	}
	// never any reading: none, plus an alert (the open feed row is deduped per kind)
	_, _ = s.db.Exec(`DELETE FROM owner_alert_feed`)
	_ = s.SetMeta("usage_alerted", "")
	_ = os.Remove(usage)
	if err := r.Start("T-0001"); err == nil || !strings.Contains(err.Error(), "no usage reading") {
		t.Fatalf("no reading must not dispatch: %v", err)
	}
	if alerts() != 1 {
		t.Errorf("no reading: %d alerts, want 1", alerts())
	}
	if probes() != 0 {
		t.Errorf("no probe event without a dispatch: %d", probes())
	}
}

// A session on another machine refreshes only its own usage file: the newest reading of all machines counts.
func TestUsageNewestAcrossMachines(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("session_machines", "local,w-down,w1")
	file := func(sevenDay string, at time.Time) string {
		return `{"five_hour_pct": "10", "seven_day_pct": "` + sevenDay + `", "updated_at": ` + strconv.FormatInt(at.Unix(), 10) + `}`
	}
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	local := func(body string) { _ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(body), 0o600) }
	now := time.Now()
	local(file("12", now.Add(-time.Hour)))
	remote, reads := file("34", now.Add(-time.Minute)), []string{}
	r.Tools.Sh = func(_ string, timeout time.Duration, _ []string, argv ...string) (int, string) {
		if timeout != 5*time.Second || strings.Join(argv[:3], " ") != "hc exec --on" {
			t.Errorf("remote read: timeout %s argv %q", timeout, argv)
		}
		reads = append(reads, argv[3])
		if argv[3] != "w1" {
			return 124, "timed out"
		}
		return 0, remote + "\nwarning: {on stderr}"
	}
	c := r.ReadCaps(now)
	if why := c.UsageUnknown(s); why != "" || c.Usage.SevenDay == nil || *c.Usage.SevenDay != 34 {
		t.Fatalf("stale local + fresh remote: %q %+v", why, c.Usage)
	}
	r.ReadCaps(now)
	if got := strings.Join(reads, ","); got != "w-down,w1" {
		t.Errorf("one read per machine until the next pass, local not read over hc: %s", got)
	}
	local(file("56", now))
	if c = r.ReadCaps(now); *c.Usage.SevenDay != 56 {
		t.Errorf("a newer local reading wins: %+v", c.Usage)
	}
	local(file("12", now.Add(-time.Hour)))
	remote = "no such file"
	if err := r.Pass(false); err != nil {
		t.Fatal(err)
	}
	if c = r.ReadCaps(now); c.UsageUnknown(s) == "" || *c.Usage.SevenDay != 12 || len(reads) != 4 {
		t.Errorf("a pass reads again and an unreadable machine is skipped: %+v reads %v", c.Usage, reads)
	}
}

// The probe run must refresh the coordinator's reading, so it is not placed on another machine.
func TestProbeRunsLocal(t *testing.T) {
	r, s, calls := placementRunner(t, map[string]fakeWorker{"w1": {memMB: 9000, shared: true, coord: "c", cloneHead: strings.Repeat("a", 40)}})
	setupRoles(t, r)
	withLocalMem(t, 8000)
	_ = s.SetCfg("session_machines", "w1")
	_ = s.SetCfg("usage_require_5h", "off")
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(`{"seven_day_pct": "12", "updated_at": `+
		strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)+`}`), 0o600)
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { return 1, "unreachable" }
	r.Tools.Git = func(_ string, args ...string) (int, string, string) {
		if args[0] == "rev-parse" {
			return 0, strings.Repeat("a", 40), ""
		}
		return 0, "", ""
	}
	task, err := s.Task("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec(`UPDATE tasks SET status='todo'`)
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "worktrees", task.Goal+"-T-0001", ".git"), 0o755)
	if err := r.Start("T-0001"); err != nil {
		t.Fatal(err)
	}
	run, _ := s.Run("T-0001")
	if run == nil || run.Machine != "" {
		t.Fatalf("the probe run is local: %+v", run)
	}
	if j := strings.Join(*calls, "\n"); strings.Contains(j, "--on w1") || !strings.Contains(j, "--on coord-core sh ") {
		t.Errorf("no call to w1, the session starts on the core:\n%s", j)
	}
}
