// Package spend measures where Claude tokens go on one machine and applies context limits to its agents. It reads
// Claude Code transcripts (~/.claude/projects/**/*.jsonl) and settings files only: no factory DB, no network.
// docs/token-discipline.md explains the numbers and the limits.
package spend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"factory/go/internal/tokens"
)

// Thresholds behind the findings. A turn that writes fewer than TinyOutput tokens is "tiny" (an ack, a status check,
// a wait); every such turn still re-reads the whole context.
const (
	TinyOutput       = 300
	BigContext       = 200_000 // a session's average context above this is flagged
	ChatterShare     = 0.30    // tiny turns above this share of all requests are flagged
	CacheWriteShare  = 0.30    // cache writes above this share of the cost are flagged
	maxLineBytes     = 64 << 20
	unpricedFamily   = "unpriced"
	defaultTopTokens = 10
)

// Request is one billed model call from a transcript.
type Request struct {
	Session string
	Project string
	Model   string
	Speed   string
	Usage   tokens.Usage
	At      time.Time
	First   bool // the first request of its transcript file: the session's start-up
}

// Context is the prompt size of the request: everything the model read.
func (r Request) Context() int64 {
	return r.Usage.Input + r.Usage.CacheRead + r.Usage.CacheWrite5m + r.Usage.CacheWrite1h
}

// Bucket sums requests of one model, session or project.
type Bucket struct {
	Key            string  `json:"key"`
	Project        string  `json:"project,omitempty"`
	Model          string  `json:"model,omitempty"`
	Requests       int     `json:"requests"`
	Tiny           int     `json:"tiny_requests"`
	Output         int64   `json:"output_tokens"`
	CacheRead      int64   `json:"cache_read_tokens"`
	CacheWrite     int64   `json:"cache_write_tokens"`
	Cost           float64 `json:"cost_usd"`
	CacheWriteCost float64 `json:"cache_write_cost_usd"`
	Unpriced       int     `json:"unpriced_requests"`
	ContextSum     int64   `json:"-"`
	ContextMax     int64   `json:"max_context_tokens"`
	StartContext   int64   `json:"start_context_tokens"`
	AvgContext     int64   `json:"avg_context_tokens"`
	SpawnCost      float64 `json:"spawn_usd"`
	PollingCost    float64 `json:"polling_usd"`
	WorkCost       float64 `json:"work_usd"`
	first          time.Time
}

func (b *Bucket) add(r Request) {
	cost, wcost, spawn, polling, work, ok := Buckets(r)
	if !ok {
		b.Unpriced++
	}
	ctx := r.Context()
	if b.Requests == 0 || r.At.Before(b.first) {
		b.first, b.StartContext = r.At, ctx
	}
	b.Requests++
	if r.Usage.Output < TinyOutput {
		b.Tiny++
	}
	b.Output += r.Usage.Output
	b.CacheRead += r.Usage.CacheRead
	b.CacheWrite += r.Usage.CacheWrite5m + r.Usage.CacheWrite1h
	b.Cost += cost
	b.CacheWriteCost += wcost
	b.SpawnCost += spawn
	b.PollingCost += polling
	b.WorkCost += work
	b.ContextSum += ctx
	if ctx > b.ContextMax {
		b.ContextMax = ctx
	}
	b.AvgContext = b.ContextSum / int64(b.Requests)
}

// Finding is one thing worth fixing, with the evidence.
type Finding struct {
	Code   string `json:"code"`
	Level  string `json:"level"` // high | medium
	Text   string `json:"text"`
	Advice string `json:"advice"`
}

// Report is the result of one scan.
type Report struct {
	Since    time.Time    `json:"since"`
	Until    time.Time    `json:"until"`
	Root     string       `json:"root"`
	Total    Bucket       `json:"total"`
	Sessions int          `json:"sessions"`
	ByModel  []Bucket     `json:"by_model"`
	Top      []Bucket     `json:"top_sessions"`
	Findings []Finding    `json:"findings"`
	ByRole   []RoleBucket `json:"by_role"`
	// MergedPRs is the --merged-prs value; PerMergedPR is Total.Cost / MergedPRs, set only when MergedPRs > 0.
	MergedPRs   int      `json:"merged_prs"`
	PerMergedPR *float64 `json:"per_merged_pr_usd,omitempty"`
}

// Roles are the factory roles hm-spend recognises in a session's kickoff message.
var Roles = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer",
	"researcher", "scout", "retro", "evaluator"}

// RoleOther is the role of a session whose kickoff names no known role.
const RoleOther = "other"

var kickoffRe = regexp.MustCompile(`the factory ([a-z-]+)\.`)

// RoleFromKickoff returns the role named in a factory kickoff ("You are `claude-pm-t-1775`, the factory pm."), or "".
func RoleFromKickoff(text string) string {
	for _, m := range kickoffRe.FindAllStringSubmatch(text, -1) {
		for _, r := range Roles {
			if m[1] == r {
				return r
			}
		}
	}
	return ""
}

// RoleBucket sums the sessions of one role. PerMergedPR is Cost / merged PRs when --merged-prs is given.
type RoleBucket struct {
	Role         string   `json:"role"`
	Sessions     int      `json:"sessions"`
	Requests     int      `json:"requests"`
	Cost         float64  `json:"cost_usd"`
	SpawnCost    float64  `json:"spawn_usd"`
	PollingCost  float64  `json:"polling_usd"`
	WorkCost     float64  `json:"work_usd"`
	StartContext int64    `json:"median_start_context_tokens"`
	PerMergedPR  *float64 `json:"per_merged_pr_usd,omitempty"`
}

// SetMergedPRs adds the per-merged-PR figures for n > 0 merged PRs; n <= 0 leaves them out.
func (r *Report) SetMergedPRs(n int) {
	r.MergedPRs = n
	if n <= 0 {
		return
	}
	v := r.Total.Cost / float64(n)
	r.PerMergedPR = &v
	for i := range r.ByRole {
		w := r.ByRole[i].Cost / float64(n)
		r.ByRole[i].PerMergedPR = &w
	}
}

func byRole(sessions map[string]*Bucket, role, parent map[string]string) []RoleBucket {
	roles := map[string]*RoleBucket{}
	starts := map[string][]int64{}
	for key, s := range sessions {
		r := role[key]
		if p, ok := parent[key]; ok {
			r = ""
			if _, known := sessions[p]; known || role[p] != "" {
				r = role[p]
			}
		}
		if r == "" {
			r = RoleOther
		}
		b := roles[r]
		if b == nil {
			b = &RoleBucket{Role: r}
			roles[r] = b
		}
		b.Sessions++
		b.Requests += s.Requests
		b.Cost += s.Cost
		b.SpawnCost += s.SpawnCost
		b.PollingCost += s.PollingCost
		b.WorkCost += s.WorkCost
		starts[r] = append(starts[r], s.StartContext)
	}
	out := make([]RoleBucket, 0, len(roles))
	for r, b := range roles {
		st := starts[r]
		sort.Slice(st, func(i, j int) bool { return st[i] < st[j] })
		b.StartContext = st[len(st)/2]
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Role < out[j].Role
	})
	return out
}

// Scan reads every transcript under root changed since `since` and sums the requests made in [since, until).
// A request is counted once even when it appears in several files (resumed or forked sessions repeat lines).
func Scan(root string, since, until time.Time, top int) (*Report, error) {
	if top <= 0 {
		top = defaultTopTokens
	}
	rep := &Report{Since: since.UTC(), Until: until.UTC(), Root: root, Total: Bucket{Key: "all"}}
	models := map[string]*Bucket{}
	sessions := map[string]*Bucket{}
	seen := map[string]bool{}
	role := map[string]string{}   // session -> role from its kickoff message
	parent := map[string]string{} // subagent session -> parent session
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		project := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		session := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		// Subagent transcripts live in <project>/<parent session>/subagents/agent-*.jsonl.
		if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) == 4 && parts[2] == "subagents" {
			parent[session] = parts[1]
		}
		return readFile(path, func(text string) bool {
			if role[session] == "" {
				role[session] = RoleFromKickoff(text)
			}
			return role[session] == ""
		}, func(r Request) {
			if r.At.Before(since) || !r.At.Before(until) {
				return
			}
			r.Session, r.Project = session, project
			rep.Total.add(r)
			m := models[r.Model]
			if m == nil {
				m = &Bucket{Key: r.Model}
				models[r.Model] = m
			}
			m.add(r)
			s := sessions[session]
			if s == nil {
				s = &Bucket{Key: session, Project: project, Model: r.Model}
				sessions[session] = s
			}
			s.add(r)
		}, seen)
	})
	if err != nil {
		return nil, err
	}
	rep.Sessions = len(sessions)
	rep.ByModel = sorted(models, 0)
	rep.Top = sorted(sessions, top)
	rep.Findings = findings(rep, models, sessions)
	rep.ByRole = byRole(sessions, role, parent)
	return rep, nil
}

func sorted(m map[string]*Bucket, limit int) []Bucket {
	out := make([]Bucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

type line struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64  `json:"input_tokens"`
			Output        int64  `json:"output_tokens"`
			CacheRead     int64  `json:"cache_read_input_tokens"`
			CacheCreation int64  `json:"cache_creation_input_tokens"`
			Speed         string `json:"speed"`
			Detail        *struct {
				OneHour  int64 `json:"ephemeral_1h_input_tokens"`
				FiveMins int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

var usageKey = []byte(`"usage"`)

// maxKickoffLines is how many user messages are tried for a role before a session counts as "other".
const maxKickoffLines = 10

var userKey = []byte(`"type":"user"`)

// userText is the text of a user line: its content string, or its text blocks joined.
func userText(raw []byte) (string, bool) {
	var u struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &u) != nil || u.Type != "user" {
		return "", false
	}
	var s string
	if json.Unmarshal(u.Message.Content, &s) == nil {
		return s, true
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(u.Message.Content, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), true
}

// ParseRequest decodes one transcript line into a billed request and its dedup key (message id + request id).
// ok is false for anything that is not a billed model call: no usage, no id, the "<synthetic>" model Claude Code
// writes itself (zero tokens), or no timestamp. Session, Project and First are left for the caller.
func ParseRequest(raw []byte) (r Request, key string, ok bool) {
	if len(raw) == 0 || len(raw) > maxLineBytes || !bytes.Contains(raw, usageKey) {
		return Request{}, "", false
	}
	var l line
	if json.Unmarshal(raw, &l) != nil || l.Message.Usage == nil || l.Message.ID == "" || l.Message.Model == "" ||
		l.Message.Model == "<synthetic>" || l.Timestamp.IsZero() {
		return Request{}, "", false
	}
	u := l.Message.Usage
	w1, w5 := int64(0), u.CacheCreation
	if u.Detail != nil {
		w1 = u.Detail.OneHour
		w5 = u.CacheCreation - w1
		if w5 < 0 {
			w5 = 0
		}
	}
	return Request{Model: l.Message.Model, Speed: u.Speed, At: l.Timestamp.UTC(), Usage: tokens.Usage{
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite5m: w5, CacheWrite1h: w1}}, l.Message.ID + "\x00" + l.RequestID, true
}

// UserText is the text of a user transcript line (its content string, or its text blocks joined), or ok=false.
func UserText(raw []byte) (string, bool) {
	if len(raw) > maxLineBytes || !bytes.Contains(raw, userKey) {
		return "", false
	}
	return userText(raw)
}

// MaxKickoffLines is how many user messages are tried for a role before a session counts as "other".
const MaxKickoffLines = maxKickoffLines

// Buckets splits the cost of one request into spawn, polling and work by the same rule as a report: the cache
// write of a file's first request is spawn and the rest of it work; a turn with output under TinyOutput is polling;
// anything else is work. cost is the whole request at API prices; ok is false when the model has no price.
func Buckets(r Request) (cost, cacheWrite, spawn, polling, work float64, ok bool) {
	cost, ok = tokens.Cost(r.Model, r.Usage, r.Speed)
	cacheWrite, _ = tokens.Cost(r.Model, tokens.Usage{CacheWrite5m: r.Usage.CacheWrite5m, CacheWrite1h: r.Usage.CacheWrite1h}, r.Speed)
	switch {
	case r.First:
		spawn, work = cacheWrite, cost-cacheWrite
	case r.Usage.Output < TinyOutput:
		polling = cost
	default:
		work = cost
	}
	return
}

// readFile emits the requests of one transcript. user receives the user messages in order (up to maxKickoffLines)
// until it returns false.
func readFile(path string, user func(string) bool, emit func(Request), seen map[string]bool) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // a transcript that vanished or is unreadable is skipped, not fatal
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	wantUser, users, first := user != nil, 0, true
	for {
		raw, err := br.ReadBytes('\n')
		if wantUser {
			if text, ok := UserText(raw); ok {
				users++
				wantUser = user(text) && users < maxKickoffLines
			}
		}
		if r, key, ok := ParseRequest(raw); ok && !seen[key] {
			seen[key] = true
			r.First = first
			first = false
			emit(r)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil
		}
	}
}
