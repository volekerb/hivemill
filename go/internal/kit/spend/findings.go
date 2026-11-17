package spend

import (
	"fmt"
	"sort"
	"strings"
)

// findings turns the sums into the few things worth fixing, most expensive first. Every finding names its evidence
// and the fix from docs/token-discipline.md.
func findings(rep *Report, models, sessions map[string]*Bucket) []Finding {
	var out []Finding
	t := rep.Total
	if t.Requests == 0 {
		return nil
	}
	for name, m := range models {
		if strings.Contains(name, "fable") && m.Requests > 0 {
			out = append(out, Finding{Code: "fable", Level: "high",
				Text:   fmt.Sprintf("%s: %d requests, $%.0f (%.0f%% of the cost)", name, m.Requests, m.Cost, pct(m.Cost, t.Cost)),
				Advice: "Fable costs twice Opus. Run agents on Sonnet, Opus only for judgement; restart them, do not /model."})
		}
		if m.Unpriced > 0 {
			out = append(out, Finding{Code: "unpriced", Level: "medium",
				Text:   fmt.Sprintf("%s: %d requests have no price, so the totals miss them", name, m.Unpriced),
				Advice: "Add the model to go/internal/tokens/prices.go and web/prices.py."})
		}
	}
	var big []string
	for _, s := range sessions {
		if s.Requests >= 10 && s.AvgContext > BigContext {
			big = append(big, s.Key)
		}
	}
	if len(big) > 0 {
		sort.Slice(big, func(i, j int) bool { return sessions[big[i]].Cost > sessions[big[j]].Cost })
		cost := 0.0
		for _, k := range big {
			cost += sessions[k].Cost
		}
		names := big
		if len(names) > 5 {
			names = names[:5]
		}
		out = append(out, Finding{Code: "big-context", Level: "high",
			Text: fmt.Sprintf("%d sessions average more than %dk context per request, $%.0f in all (%s)", len(big), BigContext/1000, cost,
				strings.Join(short(names), ", ")),
			Advice: "Cap workers at a 200k window and compact at 60% (`hm-spend limits --apply`); end a session at PR ready."})
	}
	if share := float64(t.Tiny) / float64(t.Requests); share > ChatterShare {
		out = append(out, Finding{Code: "chatter", Level: "high",
			Text: fmt.Sprintf("%.0f%% of %d requests wrote under %d tokens: acks, status checks and waits, each re-reading the full context",
				100*share, t.Requests, TinyOutput),
			Advice: "Wait with one blocking command (gh pr checks --watch, hc wait), no acks, read a screen instead of asking an agent."})
	}
	if t.Cost > 0 && t.CacheWriteCost/t.Cost > CacheWriteShare {
		out = append(out, Finding{Code: "cache-writes", Level: "medium",
			Text: fmt.Sprintf("cache writes are $%.0f (%.0f%% of the cost) over %d sessions: every new session pays its start-up context",
				t.CacheWriteCost, pct(t.CacheWriteCost, t.Cost), rep.Sessions),
			Advice: "Start fewer short-lived agents; do mechanical steps (rebases, CI waits) in scripts, not in a new agent."})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level == "high" && out[j].Level != "high" })
	return out
}

func pct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * a / b
}

func short(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		if len(k) > 8 {
			k = k[:8]
		}
		out[i] = k
	}
	return out
}
