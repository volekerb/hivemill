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
	write := func(body string) {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(body), 0o600)
	}
	block := func() string {
		c := Caps{Usage: ReadUsage(r.Env.UserHome, now), MemMB: 4096, MemMin: 1024}
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
