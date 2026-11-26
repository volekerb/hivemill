package hm

import (
	"testing"
	"time"
)

// The idle alert limit is idle_alert_min (default 20); with 0 or empty it is idle_alert_h again.
func TestIdleAlertMinutes(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	now := time.Now()
	idle := func(d time.Duration) bool {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE goals SET created=?`, now.Add(-d).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		c := r.OwnerChecks(now)
		return len(c) == 1 && c[0].Kind == "hivemill-idle"
	}
	if !idle(21 * time.Minute) {
		t.Error("21 min without a dispatch: no alert")
	}
	if idle(19 * time.Minute) {
		t.Error("19 min without a dispatch: alert")
	}
	_ = s.SetCfg("idle_alert_h", "2")
	for _, v := range []string{"0", ""} {
		if err := s.SetCfg("idle_alert_min", v); err != nil {
			t.Fatal(err)
		}
		if idle(21 * time.Minute) {
			t.Errorf("idle_alert_min %q: alert at 21 min, want the 2 h fallback", v)
		}
		if !idle(121 * time.Minute) {
			t.Errorf("idle_alert_min %q: no alert after the 2 h fallback", v)
		}
	}
}
