package hm

import (
	"errors"
	"fmt"
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

func TestUsageNewestAcrossMachines(t *testing.T) {
	r, s, _ := placementRunner(t, nil)
	_ = s.SetCfg("session_machines", "local,coord-core,-oBad,w-down,w1")
	now := time.Unix(1795548325, 0)
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	reading := func(pct int, age int64) string {
		return fmt.Sprintf(`{"five_hour_pct": "%d", "seven_day_pct": "12", "updated_at": %d}`, pct, now.Unix()-age)
	}
	_ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(reading(10, 3600)), 0o600)
	var remote string
	var asked []string
	r.RemoteUsage = func(m string) ([]byte, error) {
		asked = append(asked, m)
		if m != "w1" {
			return nil, errors.New("down")
		}
		return []byte(remote), nil
	}
	cases := []struct {
		name, body string
		pct        float64
		age        int
	}{
		{"fresh remote", reading(20, 30), 20, 30},
		{"older remote", reading(20, 7200), 10, 3600},
		{"garbage remote", "not json", 10, 3600},
		{"remote without updated_at", `{"five_hour_pct": "20", "seven_day_pct": "12"}`, 10, 3600},
		{"remote far ahead", reading(20, -61), 10, 3600},
		{"remote 1 s ahead", reading(20, -1), 20, 0},
		{"remote 5 s ahead", reading(20, -5), 20, 0},
	}
	for _, c := range cases {
		remote, asked = c.body, nil
		r.usageFiles, r.usageRead = nil, false
		got := r.ReadCaps(now)
		if got.Usage.FiveHour == nil || *got.Usage.FiveHour != c.pct || got.Usage.AgeS != c.age {
			t.Errorf("%s: %+v, want 5h %v age %d", c.name, got.Usage, c.pct, c.age)
		}
		if why := got.UsageUnknown(s); (why == "") != (c.pct == 20) {
			t.Errorf("%s: UsageUnknown %q", c.name, why)
		}
		r.ReadCaps(now) // the same pass: no second read
		if strings.Join(asked, ",") != "w-down,w1" {
			t.Errorf("%s: read %v", c.name, asked)
		}
	}
	asked = nil
	if err := r.Pass(false); err != nil {
		t.Fatal(err)
	}
	r.ReadCaps(now)
	if strings.Join(asked, ",") != "w-down,w1" {
		t.Errorf("a new pass reads the machines again: %v", asked)
	}
	held := r.lock("pass", false) // another pass is running: the skipped one still drops the cache
	asked = nil
	if err := r.Pass(false); err != nil {
		t.Fatal(err)
	}
	r.ReadCaps(now)
	held.Close()
	if strings.Join(asked, ",") != "w-down,w1" {
		t.Errorf("a skipped pass reads the machines again: %v", asked)
	}
	// a local reading a few seconds ahead of now is not replaced by an older remote one
	_ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(reading(10, -5)), 0o600)
	remote = reading(20, 7200)
	r.usageFiles, r.usageRead = nil, false
	if got := r.ReadCaps(now); got.Usage.FiveHour == nil || *got.Usage.FiveHour != 10 || got.Usage.AgeS != 0 {
		t.Errorf("local slightly ahead: %+v", got.Usage)
	}
}

func TestProbeRunsLocal(t *testing.T) {
	r, s, calls := placementRunner(t, map[string]fakeWorker{"w1": {memMB: 9000, shared: true, coord: "c"}})
	setupRoles(t, r)
	withLocalMem(t, 8000)
	_ = s.SetCfg("session_machines", "w1")
	_ = s.SetCfg("usage_require_5h", "off")
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(fmt.Sprintf(
		`{"five_hour_pct": "", "seven_day_pct": "12", "updated_at": %d}`, time.Now().Add(-time.Hour).Unix())), 0o600)
	// a clone that exists already, so Start reaches the placement of the run
	task, err := s.Task("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "worktrees", task.Goal+"-"+task.ID, ".git", "info"), 0o755)
	r.Tools.Git = func(string, ...string) (int, string, string) { return 0, strings.Repeat("a", 40), "" }
	_, _ = s.db.Exec(`UPDATE tasks SET status='todo'`)
	// the probe runs here, so the local memory floor holds it before anything is started, a free worker or not
	low := r.ReadCaps(time.Now())
	low.MemMB, low.MemMin = 500, 1024
	if why := low.DispatchBlock(s, ""); !strings.HasPrefix(why, "memory:") {
		t.Errorf("probe below the local floor: %q", why)
	}
	startErr := r.Start("T-0001")
	run, err := s.Run("T-0001")
	if err != nil || run == nil {
		t.Fatalf("no run: %v, start: %v", err, startErr)
	}
	if run.Machine != "" {
		t.Errorf("the probe run is placed on %q", run.Machine)
	}
	ev, _ := os.ReadFile(r.Log.Path)
	if !strings.Contains(string(ev), "the usage probe runs only on the local core") || !strings.Contains(string(ev), `"event":"usage.probe"`) {
		t.Errorf("events: %s", ev)
	}
	if j := strings.Join(*calls, "\n"); strings.Contains(j, "--on w1") {
		t.Errorf("a probe never touches w1:\n%s", j)
	}
}
