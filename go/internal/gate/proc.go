package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Defaults carried over from the Python source.
const (
	// UsageFreshS is usage(): a reading older than 3 h is stale.
	UsageFreshS = 3 * 3600
	// UsageRetryDelay is usage(): the sleep before the single retry of a file that may be read mid-write.
	UsageRetryDelay = 50 * time.Millisecond
	// MaxProcFile caps one /proc or usage file read. Python reads them whole; these files are KBs, so a cap this
	// large never truncates a real one and only keeps a runaway file from exhausting memory.
	MaxProcFile = 8 << 20
)

var (
	intRE   = regexp.MustCompile(`^[+-]?[0-9]+$`)
	floatRE = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
	specRE  = regexp.MustCompile(`(?i)^[+-]?(inf|infinity|nan)$`)
)

// PyInt is int(s) for the ASCII forms config and /proc files hold: surrounding whitespace, optional sign, digits.
// Anything else (non-ASCII digits, underscores, text) returns ErrUnparsable: Python either accepts a form Go does
// not or raises, and the caller must not guess.
func PyInt(s string) (int, error) {
	t := pyStrip(s)
	if !intRE.MatchString(t) {
		return 0, fmt.Errorf("int(%q): %w", s, ErrUnparsable)
	}
	n, err := strconv.Atoi(t)
	if err != nil {
		return 0, fmt.Errorf("int(%q): %w", s, ErrUnparsable)
	}
	return n, nil
}

// PyFloat is float(s) for plain decimal text; inf, nan and every other spelling return ErrUnparsable.
func PyFloat(s string) (float64, error) {
	t := pyStrip(s)
	if !floatRE.MatchString(t) {
		return 0, fmt.Errorf("float(%q): %w", s, ErrUnparsable)
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, fmt.Errorf("float(%q): %w", s, ErrUnparsable)
	}
	return f, nil
}

// FloorDiv is Python's // for ints.
func FloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// MemAvailableMB is mem_available_mb(): the MemAvailable line of a meminfo text in MB, 0 when there is none.
func MemAvailableMB(meminfo string) (int64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(line)
			if len(f) < 2 {
				return 0, fmt.Errorf("meminfo line %q: %w", line, ErrUnparsable)
			}
			n, err := PyInt(f[1])
			if err != nil {
				return 0, fmt.Errorf("meminfo line %q: %w", line, err)
			}
			return FloorDiv(int64(n), 1024), nil
		}
	}
	return 0, nil
}

// SwapUsedMB is the swap figure of `factory res`: (SwapTotal - SwapFree) // 1024 over the meminfo text; a missing
// line counts 0, like swap.get(key, 0).
func SwapUsedMB(meminfo string) (int64, error) {
	vals := map[string]int64{}
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "SwapTotal") && !strings.HasPrefix(line, "SwapFree") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0, fmt.Errorf("meminfo line %q: %w", line, ErrUnparsable)
		}
		n, err := PyInt(f[1])
		if err != nil {
			return 0, fmt.Errorf("meminfo line %q: %w", line, err)
		}
		vals[strings.Split(line, ":")[0]] = int64(n)
	}
	return FloorDiv(vals["SwapTotal"]-vals["SwapFree"], 1024), nil
}

// Load1 is os.getloadavg()[0]: the first field of a loadavg text.
func Load1(loadavg string) (float64, error) {
	f := strings.Fields(loadavg)
	if len(f) == 0 {
		return 0, fmt.Errorf("loadavg %q: %w", loadavg, ErrUnparsable)
	}
	v, err := PyFloat(f[0])
	if err != nil {
		return 0, fmt.Errorf("loadavg: %w", err)
	}
	return v, nil
}

// ReadFile reads path with the MaxProcFile cap.
func ReadFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	b, rerr := io.ReadAll(io.LimitReader(f, MaxProcFile+1))
	if cerr := f.Close(); cerr != nil && rerr == nil {
		rerr = cerr
	}
	if rerr != nil {
		return "", fmt.Errorf("read %s: %w", path, rerr)
	}
	if len(b) > MaxProcFile {
		return "", fmt.Errorf("%s is larger than %d bytes: %w", path, MaxProcFile, ErrUnparsable)
	}
	return string(b), nil
}

// TmpUsage is tmp_usage(): used/total/pct and the filesystem type for the filesystem holding dir. fake is
// $FACTORY_TMP_STATVFS ("<total_bytes>,<free_bytes>") and replaces statvfs. nil when it cannot be read.
func TmpUsage(dir, fake, mountsPath string) *Tmp {
	var total, free int64
	if fake != "" {
		parts := strings.Split(fake, ",")
		if len(parts) != 2 {
			return nil
		}
		a, e1 := PyInt(parts[0])
		b, e2 := PyInt(parts[1])
		if e1 != nil || e2 != nil {
			return nil
		}
		total, free = int64(a), int64(b)
	} else {
		var err error
		if total, free, err = statfsBytes(dir); err != nil {
			return nil
		}
	}
	if total <= 0 {
		return nil
	}
	fstype := ""
	if mounts, err := ReadFile(mountsPath); err == nil {
		target := ResolvePath(dir)
		best := ""
		for _, line := range strings.Split(mounts, "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && (target == f[1] || strings.HasPrefix(target, strings.TrimRight(f[1], "/")+"/")) && len(f[1]) >= len(best) {
				best, fstype = f[1], f[2]
			}
		}
	}
	used := total - free
	return &Tmp{UsedMB: FloorDiv(used, 1048576), TotalMB: FloorDiv(total, 1048576), Pct: 100.0 * float64(used) / float64(total), FSType: fstype, FreeMB: FloorDiv(free, 1048576)}
}

// ResolvePath is Path(p).resolve() (non-strict): symlinks of the longest existing prefix are followed.
func ResolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	rest := ""
	for cur := abs; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// ParseISO reads the aware ISO-8601 timestamps the factory stores (datetime.fromisoformat): "T" or space between the
// date and the time, optional fraction, and a +HH:MM or Z offset. A naive or unrecognised text is ok=false.
func ParseISO(s string) (time.Time, bool) {
	s = strings.Replace(s, "Z", "+00:00", 1)
	for _, layout := range []string{"2006-01-02T15:04:05-07:00", "2006-01-02 15:04:05-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ---- usage file ------------------------------------------------------------------------------------------

// pyNum is float(v) for a decoded JSON value: state "ok", "bad" (Python raises TypeError/ValueError), "overflow"
// (OverflowError, which Python's caller does not catch) or "special" (inf/nan spellings Go does not model).
func pyNum(v any) (f float64, state string) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, "ok"
		}
		return 0, "ok"
	case json.Number:
		return numText(string(x))
	case string:
		t := pyStrip(x)
		if specRE.MatchString(t) || strings.Contains(t, "_") || hasNonASCII(t) {
			return 0, "special"
		}
		if !floatRE.MatchString(t) {
			return 0, "bad"
		}
		return numText(t)
	}
	return 0, "bad"
}

func numText(t string) (float64, string) {
	f, err := strconv.ParseFloat(t, 64)
	if err != nil && !math.IsInf(f, 0) {
		return 0, "bad"
	}
	if math.IsInf(f, 0) {
		if intRE.MatchString(t) {
			return 0, "overflow" // float(10**400) raises OverflowError
		}
		return f, "ok" // a float literal that overflows is inf in Python too
	}
	return f, "ok"
}

func pctField(v any) *float64 {
	f, st := pyNum(v)
	if st != "ok" || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return &f
}

func epoch(v any) *int64 {
	if v == nil {
		return nil
	}
	if _, isBool := v.(bool); isBool {
		return nil
	}
	f, st := pyNum(v)
	if st != "ok" || math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) >= 9.2e18 {
		return nil
	}
	n := int64(f)
	return &n
}

// ParseUsage is one attempt of usage() over the file text and the clock. ok=false is an exception inside the try
// block (bad JSON, not an object, no usable percentage, bad updated_at): usage() then retries once. ambiguous=true
// means the text holds NaN/Infinity, which Python's json accepts and Go's does not; the caller delegates.
func ParseUsage(data []byte, now time.Time) (u Usage, ok, ambiguous bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var top any
	if err := dec.Decode(&top); err != nil {
		return Usage{}, false, bytes.Contains(data, []byte("NaN")) || bytes.Contains(data, []byte("Infinity"))
	}
	if dec.More() {
		return Usage{}, false, false // json.loads: "Extra data"
	}
	m, isObj := top.(map[string]any)
	if !isObj {
		return Usage{}, false, false
	}
	age := 0.0
	var updated *int64
	if raw, has := m["updated_at"]; has {
		f, st := pyNum(raw)
		switch st {
		case "special":
			return Usage{}, false, true
		case "ok":
			if math.IsInf(f, 0) {
				return Usage{}, false, true // Python: age // 60 is nan and int() raises
			}
			age = float64(now.UnixNano())/1e9 - f
			updated = epoch(raw)
		default:
			return Usage{}, false, false
		}
	} else {
		age = float64(now.UnixNano()) / 1e9
	}
	for _, k := range []string{"five_hour_pct", "seven_day_pct"} {
		if _, st := pyNum(m[k]); st == "special" {
			return Usage{}, false, true
		}
	}
	for _, k := range []string{"five_hour_pct", "seven_day_pct"} {
		if _, st := pyNum(m[k]); st == "overflow" {
			return Usage{}, false, false // the OverflowError escapes _pct_field into the retry
		}
	}
	for _, k := range []string{"five_hour_resets_at", "seven_day_resets_at"} {
		if _, st := pyNum(m[k]); st == "overflow" {
			return Usage{}, false, true // _epoch raises OverflowError in Python
		}
	}
	five, seven := pctField(m["five_hour_pct"]), pctField(m["seven_day_pct"])
	if five == nil && seven == nil {
		return Usage{}, false, false
	}
	return Usage{FiveHour: five, SevenDay: seven, Fresh: age < UsageFreshS, AgeMin: int(math.Floor(age / 60)), UpdatedAt: updated,
		FiveResetsAt: epoch(m["five_hour_resets_at"]), SevenResetsAt: epoch(m["seven_day_resets_at"])}, true, false
}

// ReadUsage is usage(): up to two attempts, sleeping UsageRetryDelay between them. A missing file is a stale reading with
// no percentages (age -1); a file that stays unreadable is Unreadable (the gate neither opens nor closes on it).
func ReadUsage(path string, now func() time.Time, sleep func(time.Duration)) (u Usage, ambiguous bool) {
	for attempt := 0; attempt < 2; attempt++ {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			if len(b) > MaxProcFile {
				break
			}
			if u, ok, amb := ParseUsage(b, now()); amb {
				return Usage{}, true
			} else if ok {
				return u, false
			}
		case os.IsNotExist(err):
			return Usage{AgeMin: -1}, false // no data is not 0%: Text says "stale since never"
		}
		if attempt == 0 {
			sleep(UsageRetryDelay)
		}
	}
	return Usage{AgeMin: -1, Unreadable: true}, false
}

// ---- host process scan -----------------------------------------------------------------------------------

// Proc is one host process as HostFacts.Processes reports it. Ppid is -1 when unknown (Linux: unreadable stat).
// Start is the zero time where the host scan does not read it (Linux /proc reader); Uid is -1 when unknown.
type Proc struct {
	Pid   int
	Ppid  int
	Uid   int
	Start time.Time
	Argv  []string
}

// ProcScan is total_claude_procs(): the top-level claude and cursor-agent processes. Known=false: /proc unreadable.
type ProcScan struct {
	Known bool
	Total int
	PIDs  []int
	Argv  map[int][]string
}

func pyBase(x string) string {
	if i := strings.LastIndex(x, "/"); i >= 0 {
		return x[i+1:]
	}
	return x
}

// ArgvSessions is _argv_sessions: the ids named by --resume/--session-id (separate or =value).
func ArgvSessions(argv []string) []string {
	var out []string
	for _, flag := range []string{"--resume", "--session-id"} {
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == flag {
				out = append(out, argv[i+1])
			}
		}
		for _, x := range argv {
			if strings.HasPrefix(x, flag+"=") {
				out = append(out, x[len(flag)+1:])
			}
		}
	}
	return out
}

// ArgvWorkspace is _argv_workspace: the first --workspace value.
func ArgvWorkspace(argv []string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--workspace" {
			return argv[i+1], true
		}
	}
	for _, x := range argv {
		if strings.HasPrefix(x, "--workspace=") {
			return x[len("--workspace="):], true
		}
	}
	return "", false
}

func isCursorAgentArgv(argv []string) bool {
	for i, x := range argv {
		if i >= 3 {
			break
		}
		switch pyBase(x) {
		case "cursor-agent":
			return true
		case "agent":
			if w, ok := ArgvWorkspace(argv); ok && w != "" {
				return true
			}
		}
	}
	return false
}

// ReadProcRoot lists the processes under a /proc-style directory root. An unreadable root is an error. A process
// whose cmdline cannot be read is listed with no argv; one whose stat cannot be parsed gets Ppid -1.
func ReadProcRoot(root string) ([]Proc, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !isDigits(e.Name()) {
			continue
		}
		p := Proc{Pid: pid, Ppid: -1, Uid: -1}
		if raw, err := os.ReadFile(filepath.Join(root, e.Name(), "cmdline")); err == nil && len(raw) <= MaxProcFile {
			for _, part := range bytes.Split(raw, []byte{0}) {
				if len(part) > 0 {
					p.Argv = append(p.Argv, strings.ToValidUTF8(string(part), "�"))
				}
			}
		}
		if st, err := os.ReadFile(filepath.Join(root, e.Name(), "stat")); err == nil {
			if pp, ok := statPPid(string(st)); ok {
				p.Ppid = pp
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// ScanProcs is _scan_host_agent_procs + total_claude_procs over the proc directory root.
func ScanProcs(root string) ProcScan {
	procs, err := ReadProcRoot(root)
	if err != nil {
		return ProcScan{}
	}
	return ScanFrom(procs)
}

// ScanFrom counts the top-level claude and cursor-agent processes of a process list (any host).
func ScanFrom(procs []Proc) ProcScan {
	found := map[int]Proc{}
	for _, p := range procs {
		isClaude := false
		for i, x := range p.Argv {
			if i < 3 && pyBase(x) == "claude" {
				isClaude = true
			}
		}
		if !isClaude && !isCursorAgentArgv(p.Argv) {
			continue
		}
		found[p.Pid] = p
	}
	out := ProcScan{Known: true, Argv: map[int][]string{}}
	for pid, p := range found {
		if _, parentCounted := found[p.Ppid]; p.Ppid >= 0 && parentCounted {
			continue
		}
		out.PIDs = append(out.PIDs, pid)
		out.Argv[pid] = p.Argv
	}
	sort.Ints(out.PIDs)
	out.Total = len(out.PIDs)
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// statPPid is int(stat[stat.rindex(")") + 1:].split()[1]).
func statPPid(stat string) (int, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	n, err := PyInt(f[1])
	return n, err == nil
}

// HasSession is bool(claude_procs(session, argvs)).
func (p ProcScan) HasSession(session string) bool {
	for _, argv := range p.Argv {
		if inList(ArgvSessions(argv), session) {
			return true
		}
	}
	return false
}

// HasCursorProc is bool(cursor_procs(workspace, argvs)).
func (p ProcScan) HasCursorProc(workspace string) bool {
	want := ResolvePath(workspace)
	for _, argv := range p.Argv {
		if w, ok := ArgvWorkspace(argv); ok && w != "" && ResolvePath(w) == want {
			return true
		}
	}
	return false
}

// Matching is the count of top-level processes that carry a known session id or workspace.
func (p ProcScan) Matching(sessions, workspaces map[string]bool) int {
	n := 0
	for _, argv := range p.Argv {
		hit := false
		for _, s := range ArgvSessions(argv) {
			if sessions[s] {
				hit = true
			}
		}
		if !hit {
			if w, ok := ArgvWorkspace(argv); ok && w != "" && workspaces[ResolvePath(w)] {
				hit = true
			}
		}
		if hit {
			n++
		}
	}
	return n
}

// CPUCount is os.cpu_count(): the CPUs online per the "0-3,5" list of onlinePath, else fallback (the CPUs this
// process may run on). Python counts online CPUs, not the affinity mask, so a taskset-limited process still sees all.
func CPUCount(onlinePath string, fallback int) int {
	s, err := ReadFile(onlinePath)
	if err != nil {
		return fallback
	}
	n := 0
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, e1 := strconv.Atoi(lo)
		if e1 != nil {
			return fallback
		}
		if !isRange {
			n++
			continue
		}
		b, e2 := strconv.Atoi(hi)
		if e2 != nil || b < a {
			return fallback
		}
		n += b - a + 1
	}
	if n < 1 {
		return fallback
	}
	return n
}
