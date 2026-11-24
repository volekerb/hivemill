package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageAndDispatchCaps(t *testing.T) {
	r, s := testRunner(t, "")
	now := time.Unix(1795548325, 0)
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	write := func(body string) { _ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(body), 0o600) }

	write(`{"five_hour_pct": "95", "seven_day_pct": "12", "updated_at": 1795548000}`)
	c := Caps{Usage: ReadUsage(r.Env.UserHome, now), MemMB: 4096, MemMin: 1024}
	if why := c.DispatchBlock(s, "www"); !strings.Contains(why, "5h at 95%") {
		t.Errorf("fresh 5h over cap: %q", why)
	}
	write(`{"five_hour_pct": "95", "seven_day_pct": "12", "updated_at": 1795500000}`) // 13 h old: stale
	c.Usage = ReadUsage(r.Env.UserHome, now)
	if why := c.DispatchBlock(s, "www"); why != "" {
		t.Errorf("a stale reading does not hold: %q", why)
	}
	write(`{"five_hour_pct": "", "seven_day_pct": "96", "updated_at": 1795548000}`)
	c.Usage = ReadUsage(r.Env.UserHome, now)
	if why := c.DispatchBlock(s, "www"); !strings.Contains(why, "7d at 96%") {
		t.Errorf("7d over cap: %q", why)
	}
	c.Usage = Usage{}
	c.MemMB = 500
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
	if h := c.Header(s); !strings.Contains(h, "usage 5h n/a / 7d n/a") || !strings.Contains(h, "mem 4.0 GB free") {
		t.Errorf("header %q", h)
	}
}
