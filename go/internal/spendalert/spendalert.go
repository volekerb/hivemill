// Package spendalert is the deterministic token-spend spike alert (T-1824, owner order M41393). Every 15 minutes it
// reads `hm-spend report --hours 1 --json` and `--hours 24`; when the last hour cost more than $20 or more than twice
// the trailing 24 h hourly average it sends ONE escalation per clock hour to claude-boss naming the top 3 sessions,
// their agents and findings. No LLM, no network. A failing hm-spend writes one event per hour and never an alert.
package spendalert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"factory/go/internal/kit/spend"
)

const (
	// HourlyLimitUSD alerts on its own: more than this in the last hour.
	HourlyLimitUSD = 20.0
	// Ratio alerts when the last hour is more than this multiple of the 24 h hourly average.
	Ratio = 2.0
	// MinSpikeUSD keeps an idle box quiet: the ratio rule needs at least this much in the last hour.
	MinSpikeUSD = 1.0
	// Every is the minimum gap between two checks.
	Every = 15 * time.Minute

	Recipient = "claude-boss"
	runKey    = "spend-alert:run"
	hourKey   = "spend-alert:hour:" // + clock hour
	failKey   = "spend-alert:fail:" // + clock hour
	topN      = 3
)

// Deps are the process boundaries; tests replace them.
type Deps struct {
	DB    *sql.DB
	Now   func() time.Time
	Spend func(ctx context.Context, hours int) ([]byte, error) // stdout of `hm-spend report --hours N --json`
}

// Result says what one Run did, for the tick line and tests.
type Result struct {
	Skipped string // "" when a check ran; otherwise why not ("throttled")
	Alerted bool
	Failed  bool
	Hour1   float64
	Avg24   float64
}

// Run does one check. It returns an error only for a database failure; an hm-spend failure is an event.
func Run(ctx context.Context, d Deps) (Result, error) {
	now := d.Now().UTC()
	var res Result
	if last, ok, err := getThrottle(ctx, d.DB, runKey); err != nil {
		return res, err
	} else if ok && now.Sub(last) < Every {
		res.Skipped = "throttled"
		return res, nil
	}
	if err := setThrottle(ctx, d.DB, runKey, now); err != nil {
		return res, err
	}
	hour := now.Format("2006-01-02T15")

	h1, err1 := fetch(ctx, d, 1)
	h24, err24 := fetch(ctx, d, 24)
	if err := errors.Join(err1, err24); err != nil {
		res.Failed = true
		if _, done, e := getThrottle(ctx, d.DB, failKey+hour); e != nil || done {
			return res, e
		}
		if e := setThrottle(ctx, d.DB, failKey+hour, now); e != nil {
			return res, e
		}
		_, e := d.DB.ExecContext(ctx, `INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)`,
			now.Format(time.RFC3339), "tick", "spend", "-", "spend-alert-failed", clip(err.Error(), 300))
		return res, e
	}
	res.Hour1, res.Avg24 = h1.Total.Cost, h24.Total.Cost/24
	if !spike(res.Hour1, res.Avg24) {
		return res, nil
	}
	if _, done, err := getThrottle(ctx, d.DB, hourKey+hour); err != nil || done {
		return res, err
	}
	subject := fmt.Sprintf("token spend spike: $%.2f in the last hour", res.Hour1)
	body := Body(h1, res.Hour1, res.Avg24, sessionAgents(ctx, d.DB, h1))
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)`,
		"tick", Recipient, "escalation", subject, body, now.Format(time.RFC3339)); err != nil {
		return res, err // not deduped: the next run retries the send
	}
	res.Alerted = true
	return res, setThrottle(ctx, d.DB, hourKey+hour, now)
}

func spike(hour1, avg24 float64) bool {
	return hour1 > HourlyLimitUSD || (hour1 >= MinSpikeUSD && hour1 > Ratio*avg24)
}

func fetch(ctx context.Context, d Deps, hours int) (*spend.Report, error) {
	out, err := d.Spend(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("hm-spend report --hours %d: %w", hours, err)
	}
	var r spend.Report
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("hm-spend report --hours %d: bad JSON: %w", hours, err)
	}
	return &r, nil
}

// SessionFindings names what is wrong with one session, with the same thresholds hm-spend uses for its findings.
func SessionFindings(b spend.Bucket) []string {
	var out []string
	if b.Requests >= 10 && b.AvgContext > spend.BigContext {
		out = append(out, fmt.Sprintf("big-context (avg %dk)", b.AvgContext/1000))
	}
	if b.Requests > 0 && float64(b.Tiny)/float64(b.Requests) > spend.ChatterShare {
		out = append(out, fmt.Sprintf("chatter (%d%% of %d requests under %d tokens)", 100*b.Tiny/b.Requests, b.Requests, spend.TinyOutput))
	}
	if b.Cost > 0 && b.CacheWriteCost/b.Cost > spend.CacheWriteShare {
		out = append(out, fmt.Sprintf("cache writes (%.0f%% of cost)", 100*b.CacheWriteCost/b.Cost))
	}
	if len(out) == 0 {
		out = append(out, "no finding (plain volume)")
	}
	return out
}

// Body is the escalation text: the numbers, the top 3 sessions with agent and findings, the report-level findings.
func Body(r *spend.Report, hour1, avg24 float64, agents map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Last hour $%.2f (%d requests, %d sessions); trailing 24 h average $%.2f/h. Limits: $%.0f/h or %gx the 24 h average.\n\nTop sessions:\n",
		hour1, r.Total.Requests, r.Sessions, avg24, HourlyLimitUSD, Ratio)
	for i, s := range r.Top {
		if i == topN {
			break
		}
		agent := agents[s.Key]
		if agent == "" {
			agent = "unknown agent (project " + s.Project + ")"
		}
		fmt.Fprintf(&sb, "%d. %s  session %s  $%.2f  %d requests: %s\n", i+1, agent, short(s.Key), s.Cost, s.Requests,
			strings.Join(SessionFindings(s), "; "))
	}
	if len(r.Top) == 0 {
		sb.WriteString("(hm-spend listed no sessions)\n")
	}
	if len(r.Findings) > 0 {
		sb.WriteString("\nhm-spend findings:\n")
		for _, f := range r.Findings {
			fmt.Fprintf(&sb, "- [%s] %s: %s\n  fix: %s\n", f.Level, f.Code, f.Text, f.Advice)
		}
	}
	sb.WriteString("\nRun `hm-spend report --hours 1` for the full table.")
	return sb.String()
}

func sessionAgents(ctx context.Context, db *sql.DB, r *spend.Report) map[string]string {
	out := map[string]string{}
	for i, s := range r.Top {
		if i == topN {
			break
		}
		var name string
		// a subagent transcript has its own session id; its agent is unknown and Body says so
		if db.QueryRowContext(ctx, `SELECT name FROM agents WHERE session_id=? ORDER BY started DESC LIMIT 1`, s.Key).Scan(&name) == nil {
			out[s.Key] = name
		}
	}
	return out
}

func short(k string) string {
	if len(k) > 8 {
		return k[:8]
	}
	return k
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n]
	}
	return s
}

func getThrottle(ctx context.Context, db *sql.DB, key string) (time.Time, bool, error) {
	var ts string
	err := db.QueryRowContext(ctx, `SELECT ts FROM throttle WHERE key=?`, key).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, perr := time.Parse(time.RFC3339, ts)
	if perr != nil {
		return time.Time{}, false, nil // unreadable stamp counts as unset: the check runs again
	}
	return t, true, nil
}

func setThrottle(ctx context.Context, db *sql.DB, key string, t time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO throttle(key,ts) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts`,
		key, t.UTC().Format(time.RFC3339))
	return err
}

// ExecSpend runs the real hm-spend (HIVEMILL_BIN_DIR, ~/.local/share/hivemill/bin, then PATH) with a 60 s limit.
func ExecSpend(ctx context.Context, hours int) ([]byte, error) {
	bin := ""
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("HIVEMILL_BIN_DIR"), filepath.Join(home, ".local", "share", "hivemill", "bin")} {
		if d == "" {
			continue
		}
		if p := filepath.Join(d, "hm-spend"); fileExec(p) {
			bin = p
			break
		}
	}
	if bin == "" {
		p, err := exec.LookPath("hm-spend")
		if err != nil {
			return nil, errors.New("hm-spend is not installed; build it: cd go && go build -o ~/.local/share/hivemill/bin/hm-spend ./cmd/hm-spend")
		}
		bin = p
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "report", "--hours", fmt.Sprint(hours), "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", bin, err)
	}
	return out, nil
}

func fileExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}
