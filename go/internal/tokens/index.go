package tokens

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Counters is the order of every usage row: input, output, cache_read, cache_write_5m, cache_write_1h, requests.
var Counters = [6]string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h", "requests"}

// IdleDropSeconds: a file idle this long drops its dedupe keys.
const IdleDropSeconds = 48 * 3600

// MaxReadBytes is the most bytes read from one file in one refresh (a var so tests can shrink it).
var MaxReadBytes int64 = 64 << 20

// TaskRE is TASK_RE.
var TaskRE = regexp.MustCompile(`worktrees[-/](G-\d+)-(T-\d+)`)

var plainID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Row is one (session, model, day, fast) aggregate; the JSON keys are Python's.
type Row struct {
	Session      string `json:"session"`
	Model        string `json:"model"`
	Day          string `json:"day"`
	Fast         bool   `json:"fast"`
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheRead    int64  `json:"cache_read"`
	CacheWrite5m int64  `json:"cache_write_5m"`
	CacheWrite1h int64  `json:"cache_write_1h"`
	Requests     int64  `json:"requests"`
}

// Counter returns counter i (see Counters).
func (r Row) Counter(i int) int64 {
	return [6]int64{r.Input, r.Output, r.CacheRead, r.CacheWrite5m, r.CacheWrite1h, r.Requests}[i]
}

func (r Row) usage() Usage {
	return Usage{r.Input, r.Output, r.CacheRead, r.CacheWrite5m, r.CacheWrite1h}
}

func (r Row) speed() string {
	if r.Fast {
		return "fast"
	}
	return ""
}

// Session is the per-session part of a snapshot; null JSON fields are nil pointers.
type Session struct {
	FirstTS   *string  `json:"first_ts"`
	LastTS    *string  `json:"last_ts"`
	AgentName *string  `json:"agent_name"`
	Project   string   `json:"project"`
	Cwd       *string  `json:"cwd"`
	CostState *float64 `json:"cost_state"`
}

// Snapshot is the TokenIndex snapshot, the same JSON as Python's TokenIndex.snapshot().
type Snapshot struct {
	Ready      bool                `json:"ready"`
	LastScan   *string             `json:"last_scan"`
	Files      int                 `json:"files"`
	BytesRead  int64               `json:"bytes_read"`
	BytesTotal int64               `json:"bytes_total"`
	Errors     int                 `json:"errors"`
	Rows       []Row               `json:"rows"`
	Sessions   map[string]*Session `json:"sessions"`
}

type fileState struct {
	session, project string
	sub              bool
	haveInode        bool
	inode            uint64
	offset           int64
	skipping         bool
	contrib          map[contribKey]*[6]int64
	keys             map[string]struct{}
	firstTS, lastTS  *string
	agentName, cwd   *string
	costState        *float64
}

type contribKey struct {
	model, day string
	fast       bool
}

func (fs *fileState) reset() {
	fs.offset = 0
	fs.skipping = false
	fs.contrib = map[contribKey]*[6]int64{}
	fs.keys = map[string]struct{}{}
	fs.firstTS, fs.lastTS, fs.agentName, fs.cwd, fs.costState = nil, nil, nil, nil, nil
}

// TokenIndex is the incremental index of Claude Code token usage from <root>/*/*.jsonl and subagent logs.
type TokenIndex struct {
	OnlySession string
	Root        string
	ExtraRoots  []string
	WorkersRoot string // "" = none

	mu       sync.Mutex // _refresh_lock
	swap     sync.Mutex // _swap_lock
	files    map[string]*fileState
	order    []string // insertion order of files, like a Python dict
	owner    map[string]map[string]string
	snap     *Snapshot
	errors   int
	bytesTot int64
	stop     chan struct{}
}

// NewTokenIndex mirrors TokenIndex(root, extra_roots, workers_root, only_session). An empty root means
// <home>/.claude/projects, and then workersRoot "" means the default WORKERS_ROOT (defaultWorkersRoot); with an
// explicit root, workers are scanned only when workersRoot is given.
func NewTokenIndex(root string, extraRoots []string, workersRoot, onlySession string) *TokenIndex {
	explicit := root != ""
	if !explicit {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".claude", "projects")
	}
	if workersRoot == "" && !explicit {
		workersRoot = defaultWorkersRoot()
	}
	return &TokenIndex{OnlySession: onlySession, Root: root, ExtraRoots: extraRoots, WorkersRoot: workersRoot,
		files: map[string]*fileState{}, owner: map[string]map[string]string{},
		snap: &Snapshot{Rows: []Row{}, Sessions: map[string]*Session{}}}
}

// defaultWorkersRoot is scout.workerbox.WORKERS_ROOT: <home>/.local/state/factory/workers.
func defaultWorkersRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "factory", "workers")
}

type discovered struct {
	path, session, project string
	sub                    bool
	confine                string
}

func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// glob1 lists the non-hidden names in dir ending in suffix (Python glob "*<suffix>"), sorted.
func glob1(dir, suffix string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !isHidden(n) && strings.HasSuffix(n, suffix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// discoverRoot is _discover_root: every transcript under root (confine "" = none; with sid only that session).
func discoverRoot(root, confine, sid string) []discovered {
	var out []discovered
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, pn := range names {
		proj := filepath.Join(root, pn)
		if !isDir(proj) {
			continue
		}
		var found []discovered
		if sid != "" {
			if st, err := os.Stat(filepath.Join(proj, sid+".jsonl")); err == nil && st != nil {
				found = append(found, discovered{filepath.Join(proj, sid+".jsonl"), sid, pn, false, confine})
			}
			for _, n := range glob1(filepath.Join(proj, sid, "subagents"), ".jsonl") {
				found = append(found, discovered{filepath.Join(proj, sid, "subagents", n), sid, pn, true, confine})
			}
		} else {
			for _, n := range glob1(proj, ".jsonl") {
				found = append(found, discovered{filepath.Join(proj, n), strings.TrimSuffix(n, ".jsonl"), pn, false, confine})
			}
			subs, _ := os.ReadDir(proj)
			var dirs []string
			for _, e := range subs {
				if !isHidden(e.Name()) {
					dirs = append(dirs, e.Name())
				}
			}
			sort.Strings(dirs)
			for _, d := range dirs {
				for _, n := range glob1(filepath.Join(proj, d, "subagents"), ".jsonl") {
					found = append(found, discovered{filepath.Join(proj, d, "subagents", n), d, pn, true, confine})
				}
			}
		}
		for _, it := range found {
			if confine == "" || inside(it.path, confine) {
				out = append(out, it)
			}
		}
	}
	return out
}

// realpath is os.path.realpath: absolute, symlinks resolved.
func realpath(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return filepath.Abs(r)
}

func within(real, confine string) bool {
	return real == confine || strings.HasPrefix(real, strings.TrimSuffix(confine, "/")+"/")
}

// inside is _inside: a regular file with one link whose realpath is inside confine.
func inside(path, confine string) bool {
	real, err := realpath(path)
	if err != nil {
		return false
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Nlink > 1 {
		return false
	}
	return within(real, confine)
}

// openConfined is _open_confined: open without following a symlink at the last component, then re-check on the open
// fd that it is a regular, single-link file whose real location is inside confine. nil when any check fails.
func openConfined(path, confine string) *os.File {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink > 1 {
		syscall.Close(fd)
		return nil
	}
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil || !within(real, confine) {
		syscall.Close(fd)
		return nil
	}
	return os.NewFile(uintptr(fd), path)
}

// workerRoots is _worker_roots: (projects dir, confine) for each <workers_root>/<agent>/claude/projects.
func (ix *TokenIndex) workerRoots() [][2]string {
	if ix.WorkersRoot == "" {
		return nil
	}
	base, err := realpath(ix.WorkersRoot)
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir(ix.WorkersRoot)
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, e := range ents {
		agent := filepath.Join(ix.WorkersRoot, e.Name())
		proj := filepath.Join(agent, "claude", "projects")
		if !isDir(proj) {
			continue
		}
		realAgent, err := realpath(agent)
		if err != nil || !within(realAgent, base) || realAgent == base {
			continue
		}
		out = append(out, [2]string{proj, filepath.Join(realAgent, "claude", "projects")})
	}
	return out
}

func (ix *TokenIndex) discover() []discovered {
	out := discoverRoot(ix.Root, "", ix.OnlySession)
	for _, r := range ix.ExtraRoots {
		out = append(out, discoverRoot(r, "", ix.OnlySession)...)
	}
	for _, pc := range ix.workerRoots() {
		out = append(out, discoverRoot(pc[0], pc[1], ix.OnlySession)...)
	}
	return out
}

// Refresh reads the unread tail of every transcript and publishes a new snapshot.
func (ix *TokenIndex) Refresh() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var bytesRead int64
	now := Now()
	seen := map[string]bool{}
	for _, d := range ix.discover() {
		seen[d.path] = true
		var f *os.File
		var st os.FileInfo
		var err error
		if d.confine != "" {
			f = openConfined(d.path, d.confine)
			if f == nil {
				ix.errors++
				delete(seen, d.path)
				continue
			}
			st, err = f.Stat()
		} else {
			st, err = os.Stat(d.path)
		}
		if err != nil {
			if f != nil {
				f.Close()
			}
			continue
		}
		fs := ix.files[d.path]
		if fs == nil {
			fs = &fileState{session: d.session, project: d.project, sub: d.sub}
			fs.reset()
			ix.files[d.path] = fs
			ix.order = append(ix.order, d.path)
		}
		ino := st.Sys().(*syscall.Stat_t).Ino
		if fs.haveInode && (ino != fs.inode || st.Size() < fs.offset) {
			ix.forgetKeys(d.path, fs)
			fs.reset()
		}
		fs.inode, fs.haveInode = ino, true
		if st.Size() > fs.offset {
			bytesRead += ix.read(d.path, fs, st.Size(), f)
		}
		if len(fs.keys) > 0 && now.Sub(st.ModTime()).Seconds() > IdleDropSeconds {
			ix.forgetKeys(d.path, fs)
		}
		if f != nil {
			f.Close()
		}
	}
	kept := ix.order[:0]
	for _, p := range ix.order {
		if !seen[p] {
			ix.forgetKeys(p, ix.files[p])
			delete(ix.files, p)
			continue
		}
		kept = append(kept, p)
	}
	ix.order = kept
	ix.bytesTot += bytesRead
	snap := ix.build(bytesRead, now)
	ix.swap.Lock()
	ix.snap = snap
	ix.swap.Unlock()
}

func (ix *TokenIndex) forgetKeys(path string, fs *fileState) {
	if owner := ix.owner[fs.session]; owner != nil {
		for k := range fs.keys {
			if owner[k] == path {
				delete(owner, k)
			}
		}
	}
	fs.keys = map[string]struct{}{}
}

func readAt(path string, f *os.File, off, want int64) ([]byte, error) {
	if f == nil {
		fh, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		f = fh
	}
	buf := make([]byte, want)
	n, err := f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// read is _read: at most MaxReadBytes of the unread tail; a bigger tail is consumed over several passes.
func (ix *TokenIndex) read(path string, fs *fileState, size int64, f *os.File) int64 {
	want := min(size-fs.offset, MaxReadBytes)
	data, err := readAt(path, f, fs.offset, want)
	if err != nil {
		ix.errors++
		return 0
	}
	if fs.skipping {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			fs.offset += int64(len(data))
			return int64(len(data))
		}
		fs.skipping = false
		fs.offset += int64(nl) + 1
		return int64(nl) + 1
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		if size-fs.offset <= MaxReadBytes {
			return 0 // only a partial line so far; re-read next pass
		}
		fmt.Fprintf(os.Stderr, "web.tokens: %s: line longer than %d bytes at offset %d, skipped\n", path, MaxReadBytes, fs.offset)
		ix.errors++
		fs.skipping = true
		fs.offset += int64(len(data))
		return int64(len(data))
	}
	owner := ix.owner[fs.session]
	if owner == nil {
		owner = map[string]string{}
		ix.owner[fs.session] = owner
	}
	pos := 0
	for pos <= end {
		nl := bytes.IndexByte(data[pos:end], '\n')
		if nl < 0 {
			nl = end
		} else {
			nl += pos
		}
		line := data[pos:nl]
		pos = nl + 1
		var kind string
		switch {
		case bytes.Contains(line, []byte(`"usage"`)):
			kind = "usage"
		case bytes.Contains(line, []byte(`"agent-name"`)):
			kind = "agent-name"
		case bytes.Contains(line, []byte(`"cost-state"`)):
			kind = "cost-state"
		default:
			continue
		}
		d, ok := decodeLine(line)
		if !ok {
			ix.errors++
			continue
		}
		m, isMap := d.(map[string]any)
		if !isMap {
			continue
		}
		t, _ := m["type"].(string)
		if t == "assistant" && kind == "usage" {
			ix.usage(path, fs, owner, m)
		} else if name, isStr := m["agentName"].(string); t == "agent-name" && isStr {
			if fs.agentName == nil {
				fs.agentName = &name
			}
		} else if t == "cost-state" {
			if total, isNum := m["totalCostUSD"].(json.Number); isNum {
				if v, err := total.Float64(); err == nil {
					fs.costState = &v
				}
			}
		}
	}
	fs.offset += int64(end) + 1
	return int64(end) + 1
}

// decodeLine is json.loads: numbers stay json.Number so an int is told from a float.
func decodeLine(line []byte) (any, bool) {
	if !json.Valid(line) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

// pyInt is _int: a JSON integer (not a float, not a bool), else 0.
func pyInt(v any) int64 {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return 0
	}
	i, err := n.Int64()
	if err != nil {
		return 0
	}
	return i
}

// truthy is Python truthiness for a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func (ix *TokenIndex) usage(path string, fs *fileState, owner map[string]string, d map[string]any) {
	msg, _ := d["message"].(map[string]any)
	if msg == nil {
		return
	}
	u, _ := msg["usage"].(map[string]any)
	if u == nil {
		return
	}
	mid, rid := msg["id"], d["requestId"]
	var key string
	haveKey := false
	if truthy(mid) && truthy(rid) {
		key, haveKey = fmt.Sprintf("t:%v\x00%v", mid, rid), true
	} else if uu, has := d["uuid"]; has && uu != nil {
		key, haveKey = fmt.Sprintf("u:%v", uu), true
	}
	if haveKey {
		if _, dup := owner[key]; dup {
			return
		}
		owner[key] = path
		fs.keys[key] = struct{}{}
	}
	ts, _ := d["timestamp"].(string)
	if t, ok := parseTS(ts); ok {
		tu := t.UTC().Format("2006-01-02T15:04:05") + "+00:00"
		if fs.firstTS == nil || tu < *fs.firstTS {
			fs.firstTS = &tu
		}
		if fs.lastTS == nil || tu > *fs.lastTS {
			fs.lastTS = &tu
		}
	}
	if cwd, ok := d["cwd"].(string); fs.cwd == nil && ok {
		fs.cwd = &cwd
	}
	cc, _ := u["cache_creation"].(map[string]any)
	w1h := pyInt(cc["ephemeral_1h_input_tokens"])
	w5m := pyInt(cc["ephemeral_5m_input_tokens"])
	totalW := pyInt(u["cache_creation_input_tokens"])
	if w5m+w1h < totalW { // no / partial split: the remainder is billed as 5m
		w5m = totalW - w1h
	}
	model := "unknown"
	if m, ok := msg["model"].(string); ok {
		model = m
	}
	speed, _ := u["speed"].(string)
	k := contribKey{model, localDay(ts), speed == "fast"}
	row := fs.contrib[k]
	if row == nil {
		row = &[6]int64{}
		fs.contrib[k] = row
	}
	row[0] += pyInt(u["input_tokens"])
	row[1] += pyInt(u["output_tokens"])
	row[2] += pyInt(u["cache_read_input_tokens"])
	row[3] += w5m
	row[4] += w1h
	row[5]++ // iterations[] is deliberately ignored: it repeats these totals
}

func (ix *TokenIndex) build(bytesRead int64, now time.Time) *Snapshot {
	type aggKey struct {
		session string
		contribKey
	}
	agg := map[aggKey]*[6]int64{}
	sessions := map[string]*Session{}
	mainCost := map[string]bool{}
	for _, p := range ix.order {
		fs := ix.files[p]
		s := sessions[fs.session]
		if s == nil {
			s = &Session{Project: fs.project}
			sessions[fs.session] = s
		}
		if fs.firstTS != nil && (s.FirstTS == nil || *fs.firstTS < *s.FirstTS) {
			s.FirstTS = fs.firstTS
			if fs.cwd != nil && *fs.cwd != "" {
				s.Cwd = fs.cwd
			}
		}
		if fs.lastTS != nil && (s.LastTS == nil || *fs.lastTS > *s.LastTS) {
			s.LastTS = fs.lastTS
		}
		if s.Cwd == nil {
			s.Cwd = fs.cwd
		}
		if fs.agentName != nil && *fs.agentName != "" && (!fs.sub || s.AgentName == nil) {
			s.AgentName = fs.agentName
		}
		if fs.costState != nil && (!fs.sub || !mainCost[fs.session]) {
			s.CostState = fs.costState
			mainCost[fs.session] = mainCost[fs.session] || !fs.sub
		}
		for k, c := range fs.contrib {
			a := agg[aggKey{fs.session, k}]
			if a == nil {
				a = &[6]int64{}
				agg[aggKey{fs.session, k}] = a
			}
			for i, v := range c {
				a[i] += v
			}
		}
	}
	keys := make([]aggKey, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.session != b.session {
			return a.session < b.session
		}
		if a.model != b.model {
			return a.model < b.model
		}
		if a.day != b.day {
			return a.day < b.day
		}
		return !a.fast && b.fast
	})
	rows := make([]Row, 0, len(keys))
	for _, k := range keys {
		v := agg[k]
		rows = append(rows, Row{k.session, k.model, k.day, k.fast, v[0], v[1], v[2], v[3], v[4], v[5]})
	}
	scan := isoSeconds(now.In(zone))
	return &Snapshot{Ready: true, LastScan: &scan, Files: len(ix.files), BytesRead: bytesRead, BytesTotal: ix.bytesTot,
		Errors: ix.errors, Rows: rows, Sessions: sessions}
}

// Snapshot returns an independent copy of the last published aggregates; it never waits for a running Refresh.
func (ix *TokenIndex) Snapshot() *Snapshot {
	ix.swap.Lock()
	s := ix.snap
	ix.swap.Unlock()
	return s.Clone()
}

// Clone deep-copies the snapshot.
func (s *Snapshot) Clone() *Snapshot {
	c := *s
	c.Rows = append([]Row{}, s.Rows...)
	c.Sessions = make(map[string]*Session, len(s.Sessions))
	for k, v := range s.Sessions {
		sv := *v
		c.Sessions[k] = &sv
	}
	return &c
}

// LoadSnapshot decodes a snapshot JSON written by Python (json.dumps(idx.snapshot())) or by Go.
func LoadSnapshot(data []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("tokens: snapshot JSON: %w", err)
	}
	if s.Rows == nil {
		s.Rows = []Row{}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
	return &s, nil
}

// StartBackground refreshes every interval until Stop; a second call while running does nothing.
func (ix *TokenIndex) StartBackground(interval time.Duration) {
	ix.swap.Lock()
	defer ix.swap.Unlock()
	if ix.stop != nil {
		return
	}
	stop := make(chan struct{})
	ix.stop = stop
	go func() {
		for {
			ix.Refresh()
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
		}
	}()
}

// Stop ends StartBackground.
func (ix *TokenIndex) Stop() {
	ix.swap.Lock()
	defer ix.swap.Unlock()
	if ix.stop != nil {
		close(ix.stop)
		ix.stop = nil
	}
}
