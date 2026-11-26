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
	PollingUSD float64 `json:"polling_usd"` // spend.Bucket.PollingCost: tiny-output requests, never a transcript's first
	// WorkflowAgents is how many Workflow agents the session ran; their spend and tokens are in the fields above.
	WorkflowAgents int `json:"workflow_agents,omitempty"`
}

// Tokens is every token the session's requests carried.
func (u SessUsage) Tokens() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

func (u SessUsage) data() map[string]any {
	d := map[string]any{"input_tokens": u.Input, "output_tokens": u.Output, "cache_read_tokens": u.CacheRead,
		"cache_write_tokens": u.CacheWrite, "polling_usd": u.PollingUSD}
	if u.WorkflowAgents > 0 {
		d["workflow_agents"] = u.WorkflowAgents
	}
	return d
}

// usageOf reads the usage a session.ended line carries (cost_usd plus the token counts and polling_usd in data; a line
// written before polling_usd counts as 0).
func usageOf(e Event) (SessUsage, bool) {
	if e.CostUSD == nil {
		return SessUsage{}, false
	}
	n := func(k string) int64 { f, _ := e.Data[k].(float64); return int64(f) }
	polling, _ := e.Data["polling_usd"].(float64)
	return SessUsage{USD: *e.CostUSD, PollingUSD: polling, Input: n("input_tokens"), Output: n("output_tokens"), CacheRead: n("cache_read_tokens"),
		CacheWrite: n("cache_write_tokens"), WorkflowAgents: int(n("workflow_agents"))}, true
}

// SessionUsages is SessionCosts with the token counts: each session's $ (rounded like SessionCosts), tokens and polling $.
func SessionUsages(userHome string, ids []string) map[string]SessUsage {
	out := map[string]SessUsage{}
	if len(ids) == 0 {
		return out
	}
	for k, b := range spend.SessionCosts(filepath.Join(userHome, ".claude", "projects"), ids) {
		out[k] = SessUsage{USD: math.Round(b.Cost*10000) / 10000, PollingUSD: math.Round(b.PollingCost*10000) / 10000, Input: b.Input, Output: b.Output, CacheRead: b.CacheRead,
			CacheWrite: b.CacheWrite, WorkflowAgents: b.WorkflowAgents}
	}
	return out
}

// closeSessions ends the run's judge and task terminals (by name, on whichever machine they run, as `run stop` does)
// and logs session.closed for each one still listed live. The worktree stays. Save a terminal run before calling it:
// the task terminal may be the one whose Stop hook runs this process, so it closes last and is logged before the kill.
func (r *Runner) closeSessions(run *Run) {
	list, ok := Terminals(r.Tools)
	end := func(name, role, machine string) {
		if name == "" {
			return
		}
		if ok && alive(list, ok, name) {
			_ = r.Log.Emit("session.closed", run, Event{Role: role, Data: map[string]any{"agent": name, "machine": machineLabel(machine)}})
		}
		r.Close(name)
	}
	for _, j := range run.Judges {
		end(j, "judge", "") // the judge stays on the local core
	}
	end(run.Agent, "task", run.Machine)
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
