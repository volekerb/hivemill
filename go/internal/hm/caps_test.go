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
