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

// An answer from a Claude Code session is the director's: it does not approve a release.
func TestAskAnswerAgentSessionIsDirector(t *testing.T) {
	for _, c := range []struct {
		agentSession bool
		by           string
		allowed      bool
	}{{true, "director", false}, {false, "owner", true}} {
		r, s := testRunner(t, "")
		r.Env.AgentSession = c.agentSession
		id, err := r.AskAdd(Ask{Project: "www", Question: "zz-test release?"})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.AskClose(id, true, "release build 7"); err != nil {
			t.Fatal(err)
		}
		a, err := s.Ask(id)
		if err != nil {
			t.Fatal(err)
		}
		if a.AnsweredBy != c.by || ReleaseAllowed([]Ask{*a}, "7") != c.allowed {
			t.Fatalf("agent session %v: want answered by %q, release allowed %v, got %+v", c.agentSession, c.by, c.allowed, a)
		}
	}
}
