package data

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HQRoles, BigContext and SidechainNote are the constants of web/context.py.
var HQRoles = []string{"boss", "director", "lead"}

const (
	BigContext    = 250_000
	SidechainNote = "Sidechain (subagent) requests are excluded from fixed_prefix, input_per_task, persistent and " +
		"per-pr input/output/cost; baseline only counts them (sidechain_requests)."
)

var taskRe = regexp.MustCompile(`worktrees[-/](G-\d+)-(T-\d+)`)

// ProjectsDir is context.projects_dir: $FACTORY_CLAUDE_PROJECTS, else ~/.claude/projects.
func ProjectsDir() string {
	if p := os.Getenv("FACTORY_CLAUDE_PROJECTS"); p != "" {
		return p
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	return filepath.Join(h, ".claude", "projects")
}

// ConnectRO is context.connect_ro (the same read-only connection as Connect, but the message names FACTORY_HOME).
func ConnectRO(path string) (*Conn, error) {
	c, err := Connect(path)
	if errors.Is(err, ErrNoDB) {
		return nil, fmt.Errorf("%w (set FACTORY_HOME)", err)
	}
	return c, err
}

// IsHQ is context.is_hq.
func IsHQ(role, name string) bool {
	return contains(HQRoles, role) || strings.HasPrefix(name, "boss") || strings.HasPrefix(name, "director") || strings.HasPrefix(name, "lead-")
}

// Attr is the agent a transcript session belongs to.
type Attr struct {
	Agent, Role string
	Task        *string
}

type agentRow struct {
	name, role          string
	goal, task, session *string
}

// agentRows is tokens._agent_rows.
func (c *Conn) agentRows() ([]agentRow, error) {
	var out []agentRow
	err := c.each("SELECT name, role, goal_id, task_id, session_id, started, ended FROM agents ORDER BY started", nil, func(r rowScanner) error {
		var a agentRow
		var goal, task, sess, started, ended sql.NullString
		if err := r.Scan(&a.name, &a.role, &goal, &task, &sess, &started, &ended); err != nil {
			return err
		}
		a.goal, a.task, a.session = nsp(goal), nsp(task), nsp(sess)
		out = append(out, a)
		return nil
	})
	return out, err
}

// eventSessions is tokens._event_sessions: session id -> agent name from the tick's session-change events.
func (c *Conn) eventSessions() map[string]string {
	out := map[string]string{}
	rows, err := c.Query("SELECT entity_id, detail FROM events WHERE entity='agent' AND (detail LIKE '%\"stored\"%' OR detail LIKE '%\"herdr\"%') ORDER BY id")
	if err != nil {
		return out // Python: sqlite3.Error means no sessions
	}
	defer rows.Close()
	for rows.Next() {
		var id, detail sql.NullString
		if rows.Scan(&id, &detail) != nil {
			continue
		}
		var d map[string]any
		if json.Unmarshal([]byte(detail.String), &d) != nil {
			continue
		}
		for _, k := range []string{"stored", "herdr"} {
			if s, ok := d[k].(string); ok {
				out[s] = id.String
			}
		}
	}
	return out
}

// Attribution is context.attribution: session id -> agent, from agents.session_id first, then the session events.
func (c *Conn) Attribution() (map[string]Attr, error) {
	agents, err := c.agentRows()
	if err != nil {
		return nil, err
	}
	byName := map[string]agentRow{}
	for _, a := range agents {
		byName[a.name] = a
	}
	out := map[string]Attr{}
	for sid, name := range c.eventSessions() {
		if a, ok := byName[name]; ok {
			out[sid] = Attr{a.name, a.role, a.task}
		}
	}
	for _, a := range agents {
		if a.session != nil && *a.session != "" {
			out[*a.session] = Attr{a.name, a.role, a.task}
		}
	}
	return out, nil
}

// ContextOf is context.context_of: input + cache_read + cache_creation tokens of one request.
func ContextOf(usage map[string]any) int64 {
	return pyInt(usage["input_tokens"]) + pyInt(usage["cache_read_input_tokens"]) + pyInt(usage["cache_creation_input_tokens"])
}

type session struct {
	first, wfirst *tokCtx
	n             int64
	last          *tokCtx
	max, total    int64
	hours         map[string]int64
	big           int64
}

type tokCtx struct {
	ts  string
	ctx int64
}

// ScanCounters are the counters of context.scan.
type ScanCounters struct {
	Files, MalformedLines, DuplicateRequests, SidechainRequests, EmptySessions, Requests, UnattributedSessions int64
}

func pathParts(p string) []string { return strings.Split(filepath.ToSlash(p), "/") }

// jsonlFiles is sorted(Path(root).rglob("*.jsonl")): sorted by path components, as Python orders Path objects.
func jsonlFiles(root string) []string {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") && len(d.Name()) > len(".jsonl")-1 {
			files = append(files, p)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		a, b := pathParts(files[i]), pathParts(files[j])
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return files
}

// forLines calls fn for every trimmed non-empty line of the file (split on \n like Python's file iteration).
func forLines(path string, fn func(raw []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fn(bytes.TrimSpace(line))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func decodeObj(raw []byte) (map[string]any, bool) {
	var d map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil || d == nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return d, true
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	}
	return true
}

func dedupeKey(d, msg map[string]any) (string, bool) {
	mid, rid := msg["id"], d["requestId"]
	if truthy(mid) && truthy(rid) {
		return "t:" + pyStr(mid) + "\x00" + pyStr(rid), true
	}
	if u, ok := d["uuid"]; ok && u != nil {
		return "u:" + pyStr(u), true
	}
	return "", false
}

// parseTokTS is tokens._parse_ts.
func parseTokTS(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	return ParseTS(s)
}

func usageOf(d map[string]any) (msg, u map[string]any) {
	msg, _ = d["message"].(map[string]any)
	if msg != nil {
		u, _ = msg["usage"].(map[string]any)
	}
	return
}

func sessionID(d map[string]any, path string) string {
	if s, ok := d["sessionId"].(string); ok {
		return s
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// Scan is context.scan: stream every transcript once; returns the per-session numbers and the counters.
func scan(root string, attr map[string]Attr, since, until string) (map[string]*session, ScanCounters) {
	sess := map[string]*session{}
	unattributed := map[string]bool{}
	var c ScanCounters
	seen := map[string]bool{}
	for _, path := range jsonlFiles(root) {
		c.Files++
		usageLines := 0
		err := forLines(path, func(raw []byte) {
			if len(raw) == 0 {
				return
			}
			if !(raw[0] == '{' && raw[len(raw)-1] == '}') {
				c.MalformedLines++
				return
			}
			d, ok := decodeObj(raw)
			if !ok {
				c.MalformedLines++
				return
			}
			if !bytes.Contains(raw, []byte(`"usage"`)) {
				return
			}
			msg, u := usageOf(d)
			if d["type"] != "assistant" || u == nil {
				return
			}
			usageLines++
			if key, ok := dedupeKey(d, msg); ok {
				if seen[key] {
					c.DuplicateRequests++
					return
				}
				seen[key] = true
			}
			t, ok := parseTokTS(d["timestamp"])
			if !ok {
				c.MalformedLines++
				return
			}
			if d["isSidechain"] == true {
				c.SidechainRequests++
				return
			}
			sid := sessionID(d, path)
			a, attributed := attr[sid]
			local := t.In(Loc)
			day := local.Format("2006-01-02")
			ctx := ContextOf(u)
			if !attributed {
				if (since == "" || day >= since) && (until == "" || day <= until) {
					unattributed[sid] = true
				}
				return
			}
			_ = a
			s := sess[sid]
			if s == nil {
				s = &session{hours: map[string]int64{}}
				sess[sid] = s
			}
			tu := t.UTC().Format("2006-01-02T15:04:05") + "+00:00"
			if s.first == nil || tu < s.first.ts {
				s.first = &tokCtx{tu, ctx}
			}
			if (since != "" && day < since) || (until != "" && day > until) {
				return
			}
			c.Requests++
			s.n++
			if s.wfirst == nil || tu < s.wfirst.ts {
				s.wfirst = &tokCtx{tu, ctx}
			}
			s.total += ctx
			if s.last == nil || tu >= s.last.ts {
				s.last = &tokCtx{tu, ctx}
			}
			if ctx > s.max {
				s.max = ctx
			}
			if ctx > BigContext {
				s.big++
			}
			hour := local.Format("2006-01-02T15") + ":00"
			if cur, ok := s.hours[hour]; !ok || ctx > cur {
				s.hours[hour] = ctx
			}
		})
		if err != nil {
			c.MalformedLines++
			continue
		}
		if usageLines == 0 {
			c.EmptySessions++
		}
	}
	c.UnattributedSessions = int64(len(unattributed))
	return sess, c
}

func pct(vals []int64, p float64) int64 {
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return s[i]
}

// Stats is context._stats.
type Stats struct {
	N      int   `json:"n"`
	Median int64 `json:"median"`
	P90    int64 `json:"p90"`
	Min    int64 `json:"min"`
	Max    int64 `json:"max"`
}

func statsOf(vals []int64) Stats {
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	var mid int64
	if n%2 == 1 {
		mid = s[n/2]
	} else {
		mid = floorDiv(s[n/2-1]+s[n/2], 2)
	}
	return Stats{n, mid, pct(s, 0.9), s[0], s[n-1]}
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// PerTaskStats is an input_per_task entry.
type PerTaskStats struct {
	N      int   `json:"n"`
	Median int64 `json:"median"`
	P90    int64 `json:"p90"`
}

// Persistent is one HQ session of Baseline.
type Persistent struct {
	Agent     string       `json:"agent"`
	Role      string       `json:"role"`
	Session   string       `json:"session"`
	Requests  int64        `json:"requests"`
	First     int64        `json:"first"`
	Last      int64        `json:"last"`
	Max       int64        `json:"max"`
	Over250k  int64        `json:"over_250k"`
	HourlyMax *OMap[int64] `json:"hourly_max"`
}

// BaselineWindow is the window block of Baseline.
type BaselineWindow struct {
	Since                *string `json:"since"`
	Until                *string `json:"until"`
	Files                int64   `json:"files"`
	MalformedLines       int64   `json:"malformed_lines"`
	DuplicateRequests    int64   `json:"duplicate_requests"`
	SidechainRequests    int64   `json:"sidechain_requests"`
	EmptySessions        int64   `json:"empty_sessions"`
	Requests             int64   `json:"requests"`
	UnattributedSessions int64   `json:"unattributed_sessions"`
}

// BaselineResult is context.baseline's result.
type BaselineResult struct {
	Window       BaselineWindow      `json:"window"`
	FixedPrefix  *OMap[Stats]        `json:"fixed_prefix"`
	InputPerTask *OMap[PerTaskStats] `json:"input_per_task"`
	Persistent   []Persistent        `json:"persistent"`
}

func inWindow(tu, since, until string) bool {
	t, _ := ParseTS(tu)
	day := t.In(Loc).Format("2006-01-02")
	return (since == "" || day >= since) && (until == "" || day <= until)
}

// Baseline is context.baseline: fixed prefix per role, input per task, HQ context over time. since and until are
// local days (YYYY-MM-DD) or empty.
func Baseline(root string, c *Conn, since, until string) (*BaselineResult, error) {
	attr, err := c.Attribution()
	if err != nil {
		return nil, err
	}
	sess, cnt := scan(root, attr, since, until)
	firstByRole := map[string][]int64{}
	perAgent := map[string]*struct {
		role  string
		total int64
	}{}
	var persistent []Persistent
	sids := make([]string, 0, len(sess))
	for sid := range sess {
		sids = append(sids, sid)
	}
	sort.Strings(sids)
	for _, sid := range sids {
		s, a := sess[sid], attr[sid]
		role := a.Role
		if role == "" {
			role = "unknown"
		}
		if s.first != nil && s.n > 0 && inWindow(s.first.ts, since, until) {
			firstByRole[role] = append(firstByRole[role], s.first.ctx)
		}
		if a.Task != nil && *a.Task != "" && s.n > 0 {
			e := perAgent[a.Agent]
			if e == nil {
				e = &struct {
					role  string
					total int64
				}{role: role}
				perAgent[a.Agent] = e
			}
			e.total += s.total
		}
		if s.n > 0 && IsHQ(a.Role, a.Agent) {
			hm := NewOMap[int64]()
			hk := make([]string, 0, len(s.hours))
			for h := range s.hours {
				hk = append(hk, h)
			}
			sort.Strings(hk)
			for _, h := range hk {
				hm.Set(h, s.hours[h])
			}
			persistent = append(persistent, Persistent{a.Agent, role, sid, s.n, s.wfirst.ctx, s.last.ctx, s.max, s.big, hm})
		}
	}
	byTask := map[string][]int64{}
	for _, e := range perAgent {
		byTask[e.role] = append(byTask[e.role], e.total)
	}
	sort.SliceStable(persistent, func(i, j int) bool {
		if persistent[i].Agent != persistent[j].Agent {
			return persistent[i].Agent < persistent[j].Agent
		}
		return persistent[i].Session < persistent[j].Session
	})
	res := &BaselineResult{FixedPrefix: NewOMap[Stats](), InputPerTask: NewOMap[PerTaskStats](), Persistent: persistent}
	if res.Persistent == nil {
		res.Persistent = []Persistent{}
	}
	res.Window = BaselineWindow{Since: nz(since), Until: nz(until), Files: cnt.Files, MalformedLines: cnt.MalformedLines,
		DuplicateRequests: cnt.DuplicateRequests, SidechainRequests: cnt.SidechainRequests, EmptySessions: cnt.EmptySessions,
		Requests: cnt.Requests, UnattributedSessions: cnt.UnattributedSessions}
	for _, r := range sortedKeys(firstByRole) {
		res.FixedPrefix.Set(r, statsOf(firstByRole[r]))
	}
	for _, r := range sortedKeys(byTask) {
		st := statsOf(byTask[r])
		res.InputPerTask.Set(r, PerTaskStats{st.N, st.Median, st.P90})
	}
	return res, nil
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Render is context.render: the text report of Baseline.
func (r *BaselineResult) Render() string {
	w := r.Window
	or := func(p *string, d string) string {
		if p == nil {
			return d
		}
		return *p
	}
	var out []string
	out = append(out, fmt.Sprintf("window: %s .. %s", or(w.Since, "start"), or(w.Until, "now")),
		fmt.Sprintf("  transcript files read: %d, requests counted: %d", w.Files, w.Requests),
		fmt.Sprintf("  unattributed sessions skipped: %d, empty sessions: %d", w.UnattributedSessions, w.EmptySessions),
		fmt.Sprintf("  sidechain requests (excluded): %d, duplicate requests: %d, malformed lines: %d", w.SidechainRequests, w.DuplicateRequests, w.MalformedLines),
		"", "1. fixed_prefix (context of a session's first request), tokens per role")
	out = append(out, fmt.Sprintf("  %-10s%5s%10s%10s%10s%10s", "role", "n", "median", "p90", "min", "max"))
	for _, k := range r.FixedPrefix.Keys {
		s := r.FixedPrefix.M[k]
		out = append(out, fmt.Sprintf("  %-10s%5d%10d%10d%10d%10d", k, s.N, s.Median, s.P90, s.Min, s.Max))
	}
	out = append(out, "", "2. input_per_task (sum of context over all requests of a task agent), per role")
	out = append(out, fmt.Sprintf("  %-10s%5s%12s%12s", "role", "n", "median", "p90"))
	for _, k := range r.InputPerTask.Keys {
		s := r.InputPerTask.M[k]
		out = append(out, fmt.Sprintf("  %-10s%5d%12d%12d", k, s.N, s.Median, s.P90))
	}
	out = append(out, "", "3. persistent (HQ sessions)")
	for _, p := range r.Persistent {
		out = append(out, fmt.Sprintf("  %s session %s: requests=%d first=%d last=%d max=%d over_250k=%d", p.Agent, p.Session, p.Requests, p.First, p.Last, p.Max, p.Over250k))
		var hs []string
		for _, h := range p.HourlyMax.Keys {
			hs = append(hs, fmt.Sprintf("%s=%d", h, p.HourlyMax.M[h]))
		}
		out = append(out, "    hourly max: "+strings.Join(hs, ", "))
	}
	return strings.Join(out, "\n") + "\n"
}

// Merged is one merged task of per-pr.
type Merged struct {
	Task          string       `json:"task"`
	Goal          string       `json:"goal"`
	PRURL         string       `json:"pr_url"`
	Merged        string       `json:"merged"`
	Sessions      int          `json:"sessions"`
	Input         int64        `json:"input"`
	Output        int64        `json:"output"`
	Cost          *Num         `json:"cost"`
	Rejects       int64        `json:"rejects"`
	RejectsByRole *OMap[int64] `json:"rejects_by_role"`
}

// MergedTasks is context.merged_tasks: merged tasks (done + pr_url + judge merge verdict), most recent merge
// first, the day window and last applied. last <= 0 means no limit.
func (c *Conn) MergedTasks(since, until string, last int) ([]Merged, error) {
	type lat struct {
		t        time.Time
		goal, pr string
	}
	latest := map[string]lat{}
	var order []string
	err := c.each("SELECT t.id, t.goal_id, t.pr_url, v.created FROM tasks t JOIN verdicts v ON v.task_id=t.id "+
		"WHERE t.status='done' AND t.pr_url IS NOT NULL AND t.pr_url<>'' AND v.role='judge' AND lower(v.verdict)='merge'", nil, func(r rowScanner) error {
		var id, goal, pr, created string
		if err := r.Scan(&id, &goal, &pr, &created); err != nil {
			return err
		}
		t, ok := ParseTS(created)
		if !ok {
			return nil
		}
		cur, had := latest[id]
		if !had {
			order = append(order, id)
		}
		if !had || t.After(cur.t) {
			latest[id] = lat{t, goal, pr}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []Merged{}
	for _, id := range order {
		l := latest[id]
		day := l.t.In(Loc).Format("2006-01-02")
		if (since != "" && day < since) || (until != "" && day > until) {
			continue
		}
		out = append(out, Merged{Task: id, Goal: l.goal, PRURL: l.pr, Merged: l.t.UTC().Format("2006-01-02T15:04:05") + "+00:00",
			Cost: FloatP(0), RejectsByRole: NewOMap[int64]()})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Merged != out[j].Merged {
			return out[i].Merged > out[j].Merged
		}
		return out[i].Task > out[j].Task
	})
	if last > 0 && last < len(out) {
		out = out[:last]
	}
	return out, nil
}

type sessUse struct {
	id            string
	input, output int64
	cost          float64
	unpriced      bool
}

// scanUsage is context.scan_usage.
func scanUsage(root string, attr map[string]Attr, taskIDs map[string]bool, hqSince, hqUntil string) (map[string][]*sessUse, int64) {
	perTask := map[string][]*sessUse{}
	var hqInput int64
	seen := map[string]bool{}
	cwdTask := map[string]string{}
	for _, path := range jsonlFiles(root) {
		_ = forLines(path, func(raw []byte) {
			if len(raw) == 0 || !(raw[0] == '{' && raw[len(raw)-1] == '}') || !bytes.Contains(raw, []byte(`"usage"`)) {
				return
			}
			d, ok := decodeObj(raw)
			if !ok {
				return
			}
			msg, u := usageOf(d)
			if d["type"] != "assistant" || u == nil || d["isSidechain"] == true {
				return
			}
			if key, ok := dedupeKey(d, msg); ok {
				if seen[key] {
					return
				}
				seen[key] = true
			}
			t, ok := parseTokTS(d["timestamp"])
			if !ok {
				return
			}
			sid := sessionID(d, path)
			var task string
			hasTask := false
			hq := false
			if a, ok := attr[sid]; !ok {
				if _, got := cwdTask[sid]; !got {
					cwd, _ := d["cwd"].(string)
					if m := taskRe.FindStringSubmatch(cwd); m != nil {
						cwdTask[sid] = m[2]
					} else {
						cwdTask[sid] = ""
					}
				}
				task, hasTask = cwdTask[sid], cwdTask[sid] != ""
			} else {
				hq = IsHQ(a.Role, a.Agent)
				if !hq && a.Task != nil && *a.Task != "" {
					task, hasTask = *a.Task, true
				}
			}
			ctx := ContextOf(u)
			if hq {
				day := t.In(Loc).Format("2006-01-02")
				if (hqSince == "" || day >= hqSince) && (hqUntil == "" || day <= hqUntil) {
					hqInput += ctx
				}
				return
			}
			if !hasTask || !taskIDs[task] {
				return
			}
			cc, _ := u["cache_creation"].(map[string]any)
			w1h := pyInt(cc["ephemeral_1h_input_tokens"])
			w5m := pyInt(cc["ephemeral_5m_input_tokens"])
			totalW := pyInt(u["cache_creation_input_tokens"])
			if w5m+w1h < totalW {
				w5m = totalW - w1h
			}
			model, _ := msg["model"].(string)
			speed, _ := u["speed"].(string)
			cost, priced := PriceCost(model, map[string]int64{"input": pyInt(u["input_tokens"]), "output": pyInt(u["output_tokens"]),
				"cache_read": pyInt(u["cache_read_input_tokens"]), "cache_write_5m": w5m, "cache_write_1h": w1h}, speed)
			var s *sessUse
			for _, x := range perTask[task] {
				if x.id == sid {
					s = x
				}
			}
			if s == nil {
				s = &sessUse{id: sid}
				perTask[task] = append(perTask[task], s)
			}
			s.input += ctx
			s.output += pyInt(u["output_tokens"])
			if !priced {
				s.unpriced = s.unpriced || ctx > 0 || pyInt(u["output_tokens"]) > 0
			} else {
				s.cost += cost
			}
		})
	}
	return perTask, hqInput
}

// PerPRWindow is the window block of PerPR.
type PerPRWindow struct {
	Since   *string `json:"since"`
	Until   *string `json:"until"`
	Last    *int    `json:"last"`
	HQSince *string `json:"hq_since"`
}

// PerPRSummary is the summary block of PerPR.
type PerPRSummary struct {
	N              int    `json:"n"`
	MedianInput    *Num   `json:"median_input"`
	P75Input       *int64 `json:"p75_input"`
	MedianSessions *Num   `json:"median_sessions"`
	MedianCost     *Num   `json:"median_cost"`
	Rejects        int64  `json:"rejects"`
	HQInput        int64  `json:"hq_input"`
	HQInputPerPR   *int64 `json:"hq_input_per_pr"`
}

// PerPRResult is context.per_pr's result.
type PerPRResult struct {
	Window  PerPRWindow  `json:"window"`
	Summary PerPRSummary `json:"summary"`
	Tasks   []Merged     `json:"tasks"`
}

// numMedian is statistics.median over Nums (ints and floats mixed, as per-pr costs are).
func numMedian(vals []Num) (Num, bool) {
	if len(vals) == 0 {
		return Num{}, false
	}
	s := append([]Num(nil), vals...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].V < s[j].V })
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return Float((s[n/2-1].V + s[n/2].V) / 2), true
}

func localDayOf(ts string) string {
	t, ok := ParseTS(ts)
	if !ok {
		return ""
	}
	return t.In(Loc).Format("2006-01-02")
}

// PerPR is context.per_pr (the SC5 metric): tokens per merged PR and HQ input per merged PR. last <= 0 means all.
func PerPR(root string, c *Conn, since, until string, last int) (*PerPRResult, error) {
	merges, err := c.MergedTasks(since, until, last)
	if err != nil {
		return nil, err
	}
	hqSince := since
	if last > 0 && since == "" && len(merges) > 0 {
		hqSince = localDayOf(merges[len(merges)-1].Merged)
	}
	attr, err := c.Attribution()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, m := range merges {
		ids[m.Task] = true
	}
	perTask, hqInput := scanUsage(root, attr, ids, hqSince, until)
	var inputs []int64
	var sessions []int64
	var costs []Num
	var rejects int64
	for i := range merges {
		m := &merges[i]
		ss := perTask[m.Task]
		m.Sessions = len(ss)
		unpriced := false
		var costVals []float64
		for _, s := range ss {
			m.Input += s.input
			m.Output += s.output
			unpriced = unpriced || s.unpriced
			costVals = append(costVals, s.cost)
		}
		if unpriced {
			m.Cost = nil
		} else if len(ss) == 0 {
			m.Cost = NumP(Int(0)) // Python: round(sum(<empty>), 4) is the int 0
		} else {
			m.Cost = FloatP(pyRoundN(sumFloat(costVals), 4))
		}
		m.RejectsByRole = NewOMap[int64]()
		err := c.each("SELECT role, COUNT(*) n FROM verdicts WHERE task_id=? AND lower(verdict) IN ('reject','fail') GROUP BY role ORDER BY role", []any{m.Task}, func(r rowScanner) error {
			var role string
			var n int64
			if err := r.Scan(&role, &n); err != nil {
				return err
			}
			m.RejectsByRole.Set(role, n)
			m.Rejects += n
			return nil
		})
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, m.Input)
		sessions = append(sessions, int64(m.Sessions))
		if m.Cost != nil {
			costs = append(costs, *m.Cost)
		}
		rejects += m.Rejects
	}
	n := len(merges)
	res := &PerPRResult{Tasks: merges}
	if res.Tasks == nil {
		res.Tasks = []Merged{}
	}
	res.Window = PerPRWindow{Since: nz(since), Until: nz(until), HQSince: nz(hqSince)}
	if last > 0 {
		res.Window.Last = &last
	}
	s := PerPRSummary{N: n, Rejects: rejects, HQInput: hqInput}
	if n > 0 {
		if m, ok := median(inputs); ok {
			s.MedianInput = NumP(m)
		}
		p := pct(inputs, 0.75)
		s.P75Input = &p
		v := pyRound(float64(hqInput) / float64(n))
		s.HQInputPerPR = &v
	}
	if m, ok := median(sessions); ok {
		s.MedianSessions = NumP(m)
	}
	if m, ok := numMedian(costs); ok {
		s.MedianCost = m.roundTo(4)
	}
	res.Summary = s
	return res, nil
}

func (n Num) roundTo(d int) *Num {
	if !n.IsFloat {
		return NumP(n)
	}
	return FloatP(pyRoundN(n.V, d))
}

// RenderPerPR is context.render_per_pr: the text report of PerPR.
func (r *PerPRResult) Render() string {
	w, s := r.Window, r.Summary
	or := func(p *string, d string) string {
		if p == nil {
			return d
		}
		return *p
	}
	head := fmt.Sprintf("merged tasks: %s .. %s", or(w.Since, "start"), or(w.Until, "now"))
	if w.Last != nil {
		head += fmt.Sprintf(", last %d", *w.Last)
	}
	out := []string{head, fmt.Sprintf("  %-8s%-7s%-22s%5s%13s%10s%9s%5s  pr", "task", "goal", "merged (UTC)", "sess", "input", "output", "$", "rej")}
	for _, m := range r.Tasks {
		cost := "n/a"
		if m.Cost != nil {
			cost = fmt.Sprintf("%.2f", m.Cost.V)
		}
		rej := strconv.FormatInt(m.Rejects, 10)
		for _, k := range m.RejectsByRole.Keys {
			rej += fmt.Sprintf(" %s=%d", k, m.RejectsByRole.M[k])
		}
		out = append(out, fmt.Sprintf("  %-8s%-7s%-22s%5d%13d%10d%9s%5s  %s", m.Task, m.Goal, m.Merged, m.Sessions, m.Input, m.Output, cost, rej, m.PRURL))
	}
	str := func(n *Num) string {
		if n == nil {
			return "None"
		}
		return n.String()
	}
	i64 := func(p *int64) string {
		if p == nil {
			return "None"
		}
		return strconv.FormatInt(*p, 10)
	}
	out = append(out, "", "summary",
		fmt.Sprintf("  n merged tasks: %d", s.N),
		fmt.Sprintf("  input tokens per merged PR: median %s, p75 %s", str(s.MedianInput), i64(s.P75Input)),
		fmt.Sprintf("  sessions per merged PR: median %s", str(s.MedianSessions)),
		fmt.Sprintf("  $ per merged PR: median %s", str(s.MedianCost)),
		fmt.Sprintf("  rejects: %d", s.Rejects),
		fmt.Sprintf("  HQ input per merged PR: %s (HQ input in window %d / %d merged tasks)", i64(s.HQInputPerPR), s.HQInput, s.N))
	return strings.Join(out, "\n") + "\n"
}

// ParseDay is context._day: a YYYY-MM-DD date.
func ParseDay(s string) (string, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", fmt.Errorf("%q is not a YYYY-MM-DD date", s)
	}
	return t.Format("2006-01-02"), nil
}

// ParsePositive is context._positive: a positive integer.
func ParsePositive(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%q is not a positive integer", s)
	}
	return n, nil
}

// RunContextCLI is context.main: `baseline` or `per-pr` with --since, --until, --last (per-pr) and --json.
// It returns the exit code: 0, or 2 for a usage error or a missing database.
func RunContextCLI(args []string, stdout, stderr io.Writer) int {
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "web.context: %s\nusage: context {baseline|per-pr} [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--last N] [--json]\n%s\n", msg, SidechainNote)
		return 2
	}
	if len(args) == 0 || (args[0] != "baseline" && args[0] != "per-pr") {
		return usage("a command is required: baseline or per-pr")
	}
	cmd := args[0]
	var since, until string
	last := 0
	asJSON := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		val := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case a == "--json":
			asJSON = true
		case a == "--since" || a == "--until":
			v, ok := val()
			if !ok {
				return usage("argument " + a + ": expected one argument")
			}
			d, err := ParseDay(v)
			if err != nil {
				return usage("argument " + a + ": " + err.Error())
			}
			if a == "--since" {
				since = d
			} else {
				until = d
			}
		case a == "--last" && cmd == "per-pr":
			v, ok := val()
			if !ok {
				return usage("argument --last: expected one argument")
			}
			n, err := ParsePositive(v)
			if err != nil {
				return usage("argument --last: " + err.Error())
			}
			last = n
		default:
			return usage("unrecognized arguments: " + a)
		}
	}
	c, err := ConnectRO(DBPath())
	if err != nil {
		fmt.Fprintf(stderr, "web.context: %v\n", err)
		return 2
	}
	defer c.Close()
	var rep any
	var text string
	if cmd == "per-pr" {
		r, err := PerPR(ProjectsDir(), c, since, until, last)
		if err != nil {
			fmt.Fprintf(stderr, "web.context: %v\n", err)
			return 2
		}
		rep, text = r, r.Render()
	} else {
		r, err := Baseline(ProjectsDir(), c, since, until)
		if err != nil {
			fmt.Fprintf(stderr, "web.context: %v\n", err)
			return 2
		}
		rep, text = r, r.Render()
	}
	if asJSON {
		b, err := DumpsIndent1(rep)
		if err != nil {
			fmt.Fprintf(stderr, "web.context: %v\n", err)
			return 2
		}
		stdout.Write(append(b, '\n'))
	} else {
		io.WriteString(stdout, text)
	}
	return 0
}

// Prices is web/prices.py PRICES (USD per million tokens), PricesAsOf its date.
var Prices = map[string]map[string]float64{
	"claude-opus-5-5":   {"input": 4.00, "output": 20.00, "cache_read": 0.20, "cache_write_5m": 5.00, "cache_write_1h": 8.00},
	"claude-sonnet-5-5": {"input": 2.00, "output": 10.00, "cache_read": 0.10, "cache_write_5m": 2.50, "cache_write_1h": 4.00},
	"claude-haiku-5-5":  {"input": 0.10, "output": 0.50, "cache_read": 0.01, "cache_write_5m": 0.125, "cache_write_1h": 0.20},
}

// PricesAsOf is the date the price table was checked.
const PricesAsOf = "2026-10-25"

var priceFields = []string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h"}

// PriceCost is prices.cost: the estimated USD for usage, false for a model without a price. Same arithmetic order
// as Python so the float result is identical.
func PriceCost(model string, usage map[string]int64, speed string) (float64, bool) {
	p, ok := Prices[model]
	if !ok {
		return 0, false
	}
	mult := 1.0
	if speed == "fast" && model == "claude-opus-5-5" {
		mult = 2.0
	}
	var sum float64
	for _, f := range priceFields {
		sum += p[f] * float64(usage[f])
	}
	return mult * sum / 1_000_000, true
}
