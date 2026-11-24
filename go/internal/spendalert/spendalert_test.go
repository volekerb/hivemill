package spendalert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

var t0 = time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC)

// recorded builds hm-spend report JSON in the shape `hm-spend report --json` prints.
func recorded(total float64, sessions ...string) string {
	return fmt.Sprintf(`{"since":"2026-11-24T09:00:00Z","until":"2026-11-24T10:00:00Z","root":"/x","total":{"key":"all","requests":50,"cost_usd":%g},
"sessions":%d,"top_sessions":[%s],"findings":[{"code":"chatter","level":"high","text":"40%% of 50 requests wrote under 300 tokens","advice":"wait with one command"}]}`,
		total, len(sessions), strings.Join(sessions, ","))
}

func sess(key string, cost float64, avgCtx int64, tiny int) string {
	return fmt.Sprintf(`{"key":%q,"project":"p","requests":20,"tiny_requests":%d,"cost_usd":%g,"cache_write_cost_usd":0,"avg_context_tokens":%d}`,
		key, tiny, cost, avgCtx)
}

type env struct {
	e   *ft.Env
	now time.Time
	h1  string
	h24 string
	err error
}

func (v *env) run(t *testing.T) Result {
	t.Helper()
	db := v.e.RawDB()
	defer db.Close()
	res, err := Run(context.Background(), Deps{DB: db, Now: func() time.Time { return v.now },
		Spend: func(_ context.Context, h int) ([]byte, error) {
			if v.err != nil {
				return nil, v.err
			}
			if h == 1 {
				return []byte(v.h1), nil
			}
			return []byte(v.h24), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func newEnv(t *testing.T, h1total, h24total float64, top ...string) *env {
	e := ft.New(t)
	e.SeedAgent(ft.AgentRow{Name: "claude-boss", Role: "boss"})
	return &env{e: e, now: t0, h1: recorded(h1total, top...), h24: recorded(h24total)}
}

func alerts(v *env) int {
	return v.e.Count("SELECT COUNT(*) FROM messages WHERE kind='escalation' AND recipient='claude-boss'")
}

func TestUnderBothThresholdsNoAlert(t *testing.T) {
	v := newEnv(t, 3, 24*2, sess("aaaaaaaa11", 2, 1000, 0)) // 3 USD/h vs 2 USD/h average: under 2x and under 20
	if r := v.run(t); r.Alerted || alerts(v) != 0 {
		t.Fatalf("alerted: %+v", r)
	}
}

func TestOver20DollarsAlertsOnce(t *testing.T) {
	v := newEnv(t, 25, 24*24, sess("aaaaaaaa11", 20, 1000, 0)) // average 24/h, so only the 20/h rule fires
	if r := v.run(t); !r.Alerted || alerts(v) != 1 {
		t.Fatalf("want one alert: %+v", r)
	}
}

func TestOverTwiceTheAverageAlertsOnce(t *testing.T) {
	v := newEnv(t, 8, 24*2, sess("aaaaaaaa11", 8, 1000, 0)) // 8 > 2 x 2, under 20
	if r := v.run(t); !r.Alerted || alerts(v) != 1 {
		t.Fatalf("want one alert: %+v", r)
	}
}

func TestTwoRunsInOneHourSendOneAlert(t *testing.T) {
	v := newEnv(t, 30, 24, sess("aaaaaaaa11", 30, 1000, 0))
	v.run(t)
	v.now = t0.Add(20 * time.Minute) // past the 15 min throttle, same clock hour
	if r := v.run(t); r.Alerted || r.Skipped != "" {
		t.Fatalf("second run: %+v", r)
	}
	if n := alerts(v); n != 1 {
		t.Fatalf("%d alerts", n)
	}
	v.now = t0.Add(60 * time.Minute) // next clock hour: alerts again
	v.run(t)
	if n := alerts(v); n != 2 {
		t.Fatalf("%d alerts after the hour changed", n)
	}
}

func TestRunsAreThrottledToFifteenMinutes(t *testing.T) {
	v := newEnv(t, 30, 24, sess("aaaaaaaa11", 30, 1000, 0))
	v.run(t)
	v.now = t0.Add(5 * time.Minute)
	if r := v.run(t); r.Skipped != "throttled" {
		t.Fatalf("%+v", r)
	}
}

func TestBodyNamesTopThreeSessionsAgentsAndFindings(t *testing.T) {
	v := newEnv(t, 40, 24,
		sess("sess-big-1111", 15, 250000, 0),
		sess("sess-chat-222", 12, 1000, 15),
		sess("sess-cw-33333", 8, 1000, 0),
		sess("sess-fourth-4", 5, 1000, 0))
	v.e.SeedAgent(ft.AgentRow{Name: "claude-coder-t-1", Role: "coder"})
	v.e.SeedAgent(ft.AgentRow{Name: "claude-pm-t-2", Role: "pm"})
	for k, name := range map[string]string{"sess-big-1111": "claude-coder-t-1", "sess-chat-222": "claude-pm-t-2"} {
		db := v.e.RawDB()
		if _, err := db.Exec("UPDATE agents SET session_id=? WHERE name=?", k, name); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	v.run(t)
	body := v.e.SQL("SELECT body FROM messages WHERE kind='escalation'")[0][0]
	for _, want := range []string{"claude-coder-t-1", "big-context (avg 250k)", "claude-pm-t-2", "chatter (75%", "sess-cw-", "unknown agent",
		"hm-spend findings:", "[high] chatter"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "sess-fou") {
		t.Errorf("fourth session listed:\n%s", body)
	}
}

func TestSpendFailureIsOneEventAndNoAlert(t *testing.T) {
	for name, tc := range map[string]func(*env){
		"missing":  func(v *env) { v.err = errors.New("hm-spend is not installed") },
		"bad json": func(v *env) { v.h1, v.h24 = "not json", "{" },
	} {
		t.Run(name, func(t *testing.T) {
			v := newEnv(t, 0, 0)
			tc(v)
			v.run(t)
			v.now = t0.Add(20 * time.Minute) // runs again in the same hour
			v.run(t)
			if n := v.e.Count("SELECT COUNT(*) FROM events WHERE action='spend-alert-failed'"); n != 1 {
				t.Fatalf("%d failure events", n)
			}
			if alerts(v) != 0 {
				t.Fatal("alerted on a failure")
			}
			v.now = t0.Add(65 * time.Minute) // next hour: one more event, still no alert
			v.run(t)
			if n := v.e.Count("SELECT COUNT(*) FROM events WHERE action='spend-alert-failed'"); n != 2 || alerts(v) != 0 {
				t.Fatalf("%d events, %d alerts", n, alerts(v))
			}
		})
	}
}

func TestIdleBoxBelowFloorDoesNotAlert(t *testing.T) {
	v := newEnv(t, 0.5, 0) // 0.5 > 2 x 0 but under the 1 USD floor
	if r := v.run(t); r.Alerted {
		t.Fatalf("%+v", r)
	}
}
