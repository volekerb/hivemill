package hm

import (
	"math"
	"path/filepath"

	"factory/go/internal/kit/spend"
)

// SessUsage is one session's spend at API prices and its tokens, from its transcript (subagent transcripts included).
type SessUsage struct {
	USD        float64 `json:"usd"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
}

// Tokens is every token the session's requests carried.
func (u SessUsage) Tokens() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

func (u SessUsage) data() map[string]any {
	return map[string]any{"input_tokens": u.Input, "output_tokens": u.Output, "cache_read_tokens": u.CacheRead,
		"cache_write_tokens": u.CacheWrite}
}

// usageOf reads the usage a session.ended line carries (cost_usd plus the token counts in data).
func usageOf(e Event) (SessUsage, bool) {
	if e.CostUSD == nil {
		return SessUsage{}, false
	}
	n := func(k string) int64 { f, _ := e.Data[k].(float64); return int64(f) }
	return SessUsage{USD: *e.CostUSD, Input: n("input_tokens"), Output: n("output_tokens"), CacheRead: n("cache_read_tokens"),
		CacheWrite: n("cache_write_tokens")}, true
}

// SessionUsages is SessionCosts with the token counts: each session's $ (rounded like SessionCosts) and tokens.
func SessionUsages(userHome string, ids []string) map[string]SessUsage {
	out := map[string]SessUsage{}
	if len(ids) == 0 {
		return out
	}
	for k, b := range spend.SessionCosts(filepath.Join(userHome, ".claude", "projects"), ids) {
		out[k] = SessUsage{USD: math.Round(b.Cost*10000) / 10000, Input: b.Input, Output: b.Output, CacheRead: b.CacheRead,
			CacheWrite: b.CacheWrite}
	}
	return out
}

// endSessions writes one session.ended line per session of the run, with its $ and tokens, and returns the total $.
// The cost is cumulative (the whole transcript), so a later line for the same session supersedes an earlier one.
func (r *Runner) endSessions(run *Run) float64 {
	sess, _ := r.Store.Sessions(run.Task)
	ids := make([]string, len(sess))
	for i, s := range sess {
		ids[i] = s.SID
	}
	use := SessionUsages(r.Env.UserHome, ids)
	total := 0.0
	for _, s := range sess {
		u := use[s.SID]
		c := u.USD
		total += c
		_ = r.Log.Emit("session.ended", run, Event{Session: s.SID, Role: s.Role, Model: s.Model, Effort: s.Effort, CostUSD: &c,
			Data: u.data()})
	}
	return total
}
