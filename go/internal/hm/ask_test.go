package hm

import (
	"testing"
	"time"
)

// The idle alert counts idle_alert_min minutes; 0 falls back to idle_alert_h hours.
func TestIdleAlertMinutes(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	now := time.Now()
	check := func(ago time.Duration, want bool) {
		t.Helper()
		if err := s.PutRun(&Run{Task: "T-0099", Project: "www", State: Merged, Dispatched: now.Add(-ago).Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
		c := r.OwnerChecks(now)
		if got := len(c) == 1 && c[0].Kind == "hivemill-idle"; got != want || len(c) > 1 {
			t.Fatalf("%s since the last dispatch: want alert %v, got %+v", ago, want, c)
		}
	}
	check(21*time.Minute, true)
	check(19*time.Minute, false)
	if err := s.SetCfg("idle_alert_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCfg("idle_alert_h", "2"); err != nil {
		t.Fatal(err)
	}
	check(21*time.Minute, false)
	check(121*time.Minute, true)
}
