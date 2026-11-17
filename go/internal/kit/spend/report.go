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
	first          time.Time
}

func (b *Bucket) add(r Request) {
	cost, ok := tokens.Cost(r.Model, r.Usage, r.Speed)
	wcost, _ := tokens.Cost(r.Model, tokens.Usage{CacheWrite5m: r.Usage.CacheWrite5m, CacheWrite1h: r.Usage.CacheWrite1h}, r.Speed)
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
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Root     string    `json:"root"`
	Total    Bucket    `json:"total"`
	Sessions int       `json:"sessions"`
	ByModel  []Bucket  `json:"by_model"`
	Top      []Bucket  `json:"top_sessions"`
	Findings []Finding `json:"findings"`
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
		return readFile(path, func(r Request) {
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

func readFile(path string, emit func(Request), seen map[string]bool) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // a transcript that vanished or is unreadable is skipped, not fatal
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		raw, err := br.ReadBytes('\n')
		if len(raw) > 0 && len(raw) <= maxLineBytes && bytes.Contains(raw, usageKey) {
			var l line
			// "<synthetic>" marks messages Claude Code writes itself (no model call, zero tokens): not a request.
			if json.Unmarshal(raw, &l) == nil && l.Message.Usage != nil && l.Message.ID != "" && l.Message.Model != "" &&
				l.Message.Model != "<synthetic>" && !l.Timestamp.IsZero() {
				key := l.Message.ID + "\x00" + l.RequestID
				if !seen[key] {
					seen[key] = true
					u := l.Message.Usage
					w1, w5 := int64(0), u.CacheCreation
					if u.Detail != nil {
						w1 = u.Detail.OneHour
						w5 = u.CacheCreation - w1
						if w5 < 0 {
							w5 = 0
						}
					}
					emit(Request{Model: l.Message.Model, Speed: u.Speed, At: l.Timestamp.UTC(), Usage: tokens.Usage{
						Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite5m: w5, CacheWrite1h: w1}})
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil
		}
	}
}
