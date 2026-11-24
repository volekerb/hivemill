package spend

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"
)

// BytesPerToken is the estimate behind tool-output tokens: a tool_result of n bytes counts as n/4 tokens.
const BytesPerToken = 4

// RoleMetrics are the before/after numbers of one role (or of all sessions, in Metrics.Total).
// Sessions counts the sessions with a request in the window; the start-up median covers those whose first request
// is in the window too. Medians of an even count are the upper middle value, as in the by-role cost table.
type RoleMetrics struct {
	Role                string   `json:"role,omitempty"`
	Sessions            int      `json:"sessions"`
	Requests            int      `json:"requests"`
	MedianStartContext  int64    `json:"median_start_context_tokens"`
	ToolOutputMedian    int64    `json:"tool_output_tokens_median"`
	ToolOutputP90       int64    `json:"tool_output_tokens_p90"`
	RequestsPerMergedPR *float64 `json:"requests_per_merged_pr,omitempty"` // total only; absent (text: n/a) without merged PRs
}

// Metrics is the section "Before/after metrics" of the report.
type Metrics struct {
	Total  RoleMetrics   `json:"total"`
	ByRole []RoleMetrics `json:"by_role"`
}

var toolResultKey = []byte(`"tool_result"`)

// toolResultBytes is the size of the content of every tool_result block of a user line.
func toolResultBytes(raw []byte) int {
	var u struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &u) != nil || u.Type != "user" {
		return 0
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(u.Message.Content, &blocks) != nil {
		return 0
	}
	n := 0
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		var s string
		if json.Unmarshal(b.Content, &s) == nil {
			n += len(s)
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(b.Content, &parts) == nil {
			for _, p := range parts {
				n += len(p.Text)
			}
		}
	}
	return n
}

func hasToolResult(raw []byte) bool { return bytes.Contains(raw, toolResultKey) }

// percentile is the nearest-rank percentile (p in 1..100) of sorted v; 0 for no values.
func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	i := (len(v)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return v[i-1]
}

func sortedCopy(v []int64) []int64 {
	c := append([]int64(nil), v...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c
}

type sessionStat struct {
	role     string
	start    int64
	hasStart bool
	toolB    int64
	requests int
}

func summarize(role string, ss []sessionStat) RoleMetrics {
	m := RoleMetrics{Role: role}
	var starts, tools []int64
	for _, s := range ss {
		m.Requests += s.requests
		m.Sessions++
		tools = append(tools, s.toolB/BytesPerToken)
		if s.hasStart {
			starts = append(starts, s.start)
		}
	}
	starts, tools = sortedCopy(starts), sortedCopy(tools)
	if len(starts) > 0 {
		m.MedianStartContext = starts[len(starts)/2]
	}
	if len(tools) > 0 {
		m.ToolOutputMedian = tools[len(tools)/2]
	}
	m.ToolOutputP90 = percentile(tools, 90)
	return m
}

// buildMetrics groups the per-session numbers by role (the same role resolution as ByRole).
func buildMetrics(stats map[string]*sessionStat, requests int) *Metrics {
	by := map[string][]sessionStat{}
	var all []sessionStat
	for _, s := range stats {
		by[s.role] = append(by[s.role], *s)
		all = append(all, *s)
	}
	out := &Metrics{Total: summarize("", all), ByRole: []RoleMetrics{}}
	out.Total.Requests = requests
	for r, ss := range by {
		out.ByRole = append(out.ByRole, summarize(r, ss))
	}
	sort.Slice(out.ByRole, func(i, j int) bool {
		if out.ByRole[i].Requests != out.ByRole[j].Requests {
			return out.ByRole[i].Requests > out.ByRole[j].Requests
		}
		return out.ByRole[i].Role < out.ByRole[j].Role
	})
	return out
}

// SetMergedPRs on the metrics: requests / n, left out for n <= 0.
func (m *Metrics) setMergedPRs(n int) {
	m.Total.RequestsPerMergedPR = nil
	if n > 0 {
		v := float64(m.Total.Requests) / float64(n)
		m.Total.RequestsPerMergedPR = &v
	}
}

// ParseTime reads an RFC3339 time or a YYYY-MM-DD date (00:00 UTC).
func ParseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", s)
	return t.UTC(), err
}
